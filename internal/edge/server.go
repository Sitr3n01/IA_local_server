package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/adminpipe"
)

const maxHeaderBytes = 64 << 10

// Server is a loopback-only, stateless OpenAI-compatible edge. It owns no
// model lifecycle state; llama-swap remains the single lifecycle authority.
type Server struct {
	cfg      Config
	upstream *url.URL
	client   *http.Client
	allowed  map[string]struct{}
	gate     *gate
	// bodies bounds requests retaining a decoded body, including queue waits.
	// Reading before inference admission must not create an unbounded buffer.
	bodies       chan struct{}
	events       *eventStore
	metrics      *metrics
	startedAt    time.Time
	memoryStatus func() (memorySnapshot, error)
	// gpuMemory is observability only; nothing in the request path consults it.
	// It is a field rather than a direct call so tests can supply a snapshot
	// without a GPU, exactly as memoryStatus does.
	gpuMemory func() (gpuMemorySnapshot, error)
	gpuCache  gpuMemoryCache
	// memoryConsumers names the applications holding host memory in a
	// capacity refusal. Like gpuMemory it never decides admission.
	memoryConsumers func(limit int, byCommit bool) []memoryConsumer
	// inference holds numbers about admitted requests for /api/v1/inference:
	// token counts, timings, status. Never a prompt, a completion or a header.
	inference *inferenceLog
}

func New(cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	allowed := make(map[string]struct{}, len(cfg.Models))
	for _, model := range cfg.Models {
		allowed[model.ID] = struct{}{}
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          cfg.MaxActive + cfg.MaxQueue + 2,
		MaxIdleConnsPerHost:   cfg.MaxActive + 1,
		MaxConnsPerHost:       cfg.MaxActive + 1,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: cfg.HeaderTimeout,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("upstream redirects are disabled")
		},
	}
	return &Server{
		cfg:             cfg,
		upstream:        upstream,
		client:          client,
		allowed:         allowed,
		gate:            newGate(cfg.MaxActive, cfg.MaxQueue, cfg.QueueWait),
		bodies:          make(chan struct{}, cfg.MaxActive+cfg.MaxQueue),
		events:          newEventStore(cfg.LogOutput),
		metrics:         newMetrics(),
		startedAt:       time.Now(),
		memoryStatus:    systemMemoryStatus,
		gpuMemory:       gpuMemoryStatus,
		memoryConsumers: topMemoryConsumers,
		inference:       newInferenceLog(time.Now),
	}, nil
}

// publicModel resolves provider.public_model out of the allowlist. Readiness and
// the headline capacity figure are about this model specifically; using
// Models[0] made manifest array order silently load-bearing.
func (s *Server) publicModel() Model {
	if model, ok := s.modelByID(s.cfg.PublicModelID); ok {
		return model
	}
	// Config.Validate rejects a public ID outside the allowlist, so this is
	// unreachable in a validated server; failing closed beats indexing blindly.
	return Model{ID: s.cfg.PublicModelID}
}

func (s *Server) DataHandler() http.Handler {
	return s.observe(http.HandlerFunc(s.serveData))
}

func (s *Server) ControlHandler() http.Handler {
	return s.observe(http.HandlerFunc(s.serveControl))
}

func (s *Server) Run(ctx context.Context) error {
	dataServer := &http.Server{
		Addr:              s.cfg.DataAddr,
		Handler:           s.DataHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	controlServer := &http.Server{
		Addr:              s.cfg.ControlAddr,
		Handler:           s.ControlHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	dataListener, err := net.Listen("tcp", s.cfg.DataAddr)
	if err != nil {
		return fmt.Errorf("listen on data address: %w", err)
	}
	controlListener, err := net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		_ = dataListener.Close()
		return fmt.Errorf("listen on control address: %w", err)
	}

	errCh := make(chan error, 3)
	go func() { errCh <- dataServer.Serve(dataListener) }()
	go func() { errCh <- controlServer.Serve(controlListener) }()

	// The administrative pipe is a third listener with no network exposure at
	// all. Failing to create it is fatal on purpose: the usual cause is another
	// local process already holding the name, which is exactly the condition the
	// transport exists to detect.
	if s.cfg.AdminPipe != "" {
		pipeListener, pipeErr := adminpipe.Listen(s.cfg.AdminPipe)
		if pipeErr != nil {
			_ = dataListener.Close()
			_ = controlListener.Close()
			return fmt.Errorf("listen on administrative pipe: %w", pipeErr)
		}
		pipeCtx, cancelPipe := context.WithCancel(ctx)
		defer cancelPipe()
		defer pipeListener.Close()
		s.logEvent("admin_pipe_listening", map[string]any{"pipe": pipeListener.Name(), "dacl": pipeListener.SecurityDescriptor()})
		go func() {
			serveErr := pipeListener.Serve(pipeCtx, s.AdminHandler(), func(err error) {
				s.logEvent("admin_pipe_error", map[string]any{"error": err.Error()})
			})
			if pipeCtx.Err() == nil {
				errCh <- serveErr
			}
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		dataErr := dataServer.Shutdown(shutdownCtx)
		controlErr := controlServer.Shutdown(shutdownCtx)
		if dataErr != nil {
			return fmt.Errorf("shutdown data server: %w", dataErr)
		}
		if controlErr != nil {
			return fmt.Errorf("shutdown control server: %w", controlErr)
		}
		return nil
	case err := <-errCh:
		_ = dataServer.Close()
		_ = controlServer.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// logEvent writes one sanitized structured line. Callers pass metadata only:
// the fields here are addresses, security descriptors, and error strings this
// process produced, never a header, body, prompt, or credential.
func (s *Server) logEvent(name string, fields map[string]any) {
	if s.cfg.LogOutput == nil {
		return
	}
	line := map[string]any{
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
		"service": "cia-edge",
		"event":   name,
	}
	for key, value := range fields {
		line[key] = value
	}
	_ = json.NewEncoder(s.cfg.LogOutput).Encode(line)
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := requestID()
		w.Header().Set("X-Request-Id", id)
		recorder := &statusWriter{ResponseWriter: w}
		s.metrics.requests.Add(1)
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		// Reading the telemetry must not write to the event log: the monitor
		// polls it every second, which would evict every real request from the
		// hundred-entry ring within two minutes. Failures are still recorded.
		if r.URL.Path == inferenceTelemetryPath && status < http.StatusBadRequest {
			return
		}
		s.events.add(event{
			Time:       time.Now().UTC().Format(time.RFC3339Nano),
			RequestID:  id,
			Method:     safeMethod(r.Method),
			Path:       safeRoute(r.URL.Path),
			Status:     status,
			DurationMS: time.Since(started).Milliseconds(),
		})
	})
}

func (s *Server) serveData(w http.ResponseWriter, r *http.Request) {
	if !validRequestHost(r.Host) {
		s.writeError(w, http.StatusForbidden, "invalid_host", "request Host must be loopback", "")
		return
	}

	switch r.URL.Path {
	case "/v1/models":
		inferenceAuthorized := s.authorized(r, s.cfg.InferenceToken)
		claudeAuthorized := s.authorized(r, s.cfg.ClaudeGatewayToken)
		if !inferenceAuthorized && !claudeAuthorized {
			s.writeInferenceAuthError(w)
			return
		}
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		if err := validateModelsQuery(r.URL.Query()); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "")
			return
		}
		if claudeAuthorized {
			s.writeJSON(w, http.StatusOK, struct {
				Object string               `json:"object"`
				Data   []claudeCatalogModel `json:"data"`
			}{Object: "list", Data: claudeCatalogModels(s.cfg.Models)})
			return
		}
		s.writeJSON(w, http.StatusOK, struct {
			Object string  `json:"object"`
			Data   []Model `json:"data"`
		}{Object: "list", Data: append([]Model(nil), s.cfg.Models...)})
	case "/v1/responses", "/v1/chat/completions":
		if r.URL.RawQuery != "" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported", "")
			return
		}
		if !s.authorized(r, s.cfg.InferenceToken) {
			s.writeInferenceAuthError(w)
			return
		}
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleInference(w, r)
	case "/v1/messages":
		if err := validateMessagesQuery(r.URL.Query()); err != nil {
			s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		if !s.authorized(r, s.cfg.ClaudeGatewayToken) {
			s.metrics.authFailures.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="cia-claude-gateway"`)
			s.writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid Claude gateway credential")
			return
		}
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleAnthropicMessages(w, r)
	default:
		s.writeError(w, http.StatusNotFound, "unknown_path", "route not found", "")
	}
}

// validateModelsQuery accepts the one pagination parameter used by Claude
// Desktop's gateway discovery request. The catalog is deliberately small and
// static, so the value does not change the response; validating it rather than
// silently ignoring arbitrary parameters keeps the rest of the data plane
// fail-closed.
func validateModelsQuery(values url.Values) error {
	if len(values) == 0 {
		return nil
	}
	limits, ok := values["limit"]
	if !ok || len(values) != 1 || len(limits) != 1 {
		return errors.New("only one models limit parameter is supported")
	}
	limit, err := strconv.Atoi(limits[0])
	if err != nil || limit < 1 || limit > 1000 {
		return errors.New("models limit must be an integer from 1 through 1000")
	}
	return nil
}

// validateMessagesQuery accepts the beta transport flag used by Claude
// Desktop's third-party Cowork client. Beta features themselves remain
// expressed in the anthropic-beta header; the query flag only selects the
// client's beta Messages transport. All other query shapes fail closed.
func validateMessagesQuery(values url.Values) error {
	if len(values) == 0 {
		return nil
	}
	betas, ok := values["beta"]
	if !ok || len(values) != 1 || len(betas) != 1 || betas[0] != "true" {
		return errors.New("only beta=true is supported for messages")
	}
	return nil
}

const claudeDesktopCompatibilityTier = "sonnet"

// claudeCatalogModel is a Claude Desktop compatibility projection of a CIA
// model. Desktop 1.37937 accepts anthropic_family_tier during discovery but
// later drops gateway IDs containing known non-Anthropic family names. The
// opaque, deterministic ID is therefore a wire alias only. The real identity
// remains visible in DisplayName and CIARealModelID and is restored before
// admission, logging, and routing.
type claudeCatalogModel struct {
	ID                    string       `json:"id"`
	CIARealModelID        string       `json:"cia_real_model_id"`
	Object                string       `json:"object"`
	OwnedBy               string       `json:"owned_by"`
	DisplayName           string       `json:"display_name,omitempty"`
	Capabilities          Capabilities `json:"capabilities,omitempty"`
	AnthropicFamilyTier   string       `json:"anthropic_family_tier"`
	MaximumInputTokenHint *int         `json:"max_input_tokens,omitempty"`
}

func claudeCatalogModels(models []Model) []claudeCatalogModel {
	result := make([]claudeCatalogModel, 0, len(models))
	for _, model := range models {
		result = append(result, claudeCatalogModel{
			ID:                    claudeExternalModelID(model.ID),
			CIARealModelID:        model.ID,
			Object:                model.Object,
			OwnedBy:               model.OwnedBy,
			DisplayName:           model.DisplayName,
			Capabilities:          model.Capabilities,
			AnthropicFamilyTier:   claudeDesktopCompatibilityTier,
			MaximumInputTokenHint: model.ContextTokens,
		})
	}
	return result
}

func claudeExternalModelID(realModelID string) string {
	digest := sha256.Sum256([]byte(realModelID))
	return "claude-local-" + hex.EncodeToString(digest[:])
}

// claudeRealModelID accepts direct real IDs for non-Desktop Anthropic clients
// and the generated Desktop wire alias. It detects any theoretical alias
// collision and fails closed instead of selecting an arbitrary model.
func claudeRealModelID(models []Model, requested string) (string, bool) {
	for _, model := range models {
		if model.ID == requested {
			return model.ID, true
		}
	}
	matched := ""
	for _, model := range models {
		if claudeExternalModelID(model.ID) != requested {
			continue
		}
		if matched != "" && matched != model.ID {
			return "", false
		}
		matched = model.ID
	}
	return matched, matched != ""
}

func (s *Server) writeInferenceAuthError(w http.ResponseWriter) {
	s.metrics.authFailures.Add(1)
	w.Header().Set("WWW-Authenticate", `Bearer realm="cia-edge"`)
	s.writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid inference credential", "")
}

func (s *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", "")
			return
		}
	}
	releaseBody, err := s.reserveBody(r.Context())
	if err != nil {
		s.writeGateError(w, err)
		return
	}
	defer releaseBody()

	body, err := decodeRequestBody(r, s.cfg.MaxWireBytes, s.cfg.MaxDecodedBytes, s.cfg.MaxRatio)
	if err != nil {
		s.metrics.invalidRequests.Add(1)
		s.writeBodyError(w, err)
		return
	}
	model, err := validatePayload(r.URL.Path, body)
	if err != nil {
		s.metrics.invalidRequests.Add(1)
		var validation *payloadError
		if errors.As(err, &validation) {
			s.writeError(w, validation.Status, validation.Code, validation.Message, validation.Param)
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a JSON object", "")
		return
	}
	if r.URL.Path == "/v1/responses" {
		body, err = normalizeResponsesAuthorityMessages(body)
		if err != nil {
			s.metrics.invalidRequests.Add(1)
			var validation *payloadError
			if errors.As(err, &validation) {
				s.writeError(w, validation.Status, validation.Code, validation.Message, validation.Param)
				return
			}
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a JSON object", "")
			return
		}
	} else {
		body, err = normalizeChatAuthorityMessages(body)
		if err != nil {
			s.metrics.invalidRequests.Add(1)
			var validation *payloadError
			if errors.As(err, &validation) {
				s.writeError(w, validation.Status, validation.Code, validation.Message, validation.Param)
				return
			}
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a JSON object", "")
			return
		}
	}
	body, namespaceRewrite, err := normalizeNamespacedTools(r.URL.Path, body)
	if err != nil {
		s.metrics.invalidRequests.Add(1)
		var validation *payloadError
		if errors.As(err, &validation) {
			s.writeError(w, validation.Status, validation.Code, validation.Message, validation.Param)
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a JSON object", "")
		return
	}
	modelConfig, ok := s.modelByID(model)
	if !ok {
		s.metrics.invalidRequests.Add(1)
		s.writeError(w, http.StatusNotFound, "model_not_found", "requested model is not available", "model")
		return
	}
	if err := validateCapabilities(r.URL.Path, body, modelConfig.Capabilities); err != nil {
		s.metrics.invalidRequests.Add(1)
		s.writeError(w, err.Status, err.Code, err.Message, err.Param)
		return
	}
	release, err := s.gate.acquire(r.Context())
	if err != nil {
		s.writeGateError(w, err)
		return
	}
	admitted := time.Now()
	defer func() {
		s.metrics.inferenceDuration.observe(time.Since(admitted))
		release()
	}()
	capacity, ok := s.requireCapacity(w, r.Context(), modelConfig)
	if !ok {
		return
	}
	track := s.inference.begin(modelConfig, r.URL.Path, requestStreams(body), !capacity.ModelRunning)
	r = r.WithContext(withInferenceTrack(r.Context(), track))
	defer func() { track.end(responseStatus(w), r.Context().Err() != nil) }()

	if err := s.proxy(w, r, body, namespaceRewrite); err != nil {
		s.metrics.upstreamFailures.Add(1)
		if !headersWritten(w) {
			if errors.Is(err, context.Canceled) {
				s.writeError(w, 499, "client_closed_request", "request was canceled", "")
				return
			}
			s.writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "local inference runtime is unavailable", "")
		}
	}
}

// reserveBody refuses excess requests before reading them. Hold the reservation
// through inference so at most MaxActive+MaxQueue requests retain decoded data.
func (s *Server) reserveBody(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.gate.control.Lock()
	defer s.gate.control.Unlock()
	if err := s.gate.admissionError(); err != nil {
		return nil, err
	}
	select {
	case s.bodies <- struct{}{}:
		return func() { <-s.bodies }, nil
	default:
		s.gate.rejected.Add(1)
		return nil, errQueueFull
	}
}

func (s *Server) proxy(w http.ResponseWriter, incoming *http.Request, body []byte, namespaceRewrite *namespaceRewrite) error {
	target := *s.upstream
	target.Path = incoming.URL.Path
	request, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", incoming.Header.Get("Accept"))
	request.Header.Set("X-Request-Id", w.Header().Get("X-Request-Id"))
	request.Header.Set("User-Agent", "cia-edge/"+s.cfg.Version)
	if s.cfg.RouterToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.cfg.RouterToken)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	upstreamBody := observeInferenceBody(incoming.Context(), response)

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	isSSE := strings.HasPrefix(contentType, "text/event-stream")
	if namespaceRewrite != nil && response.StatusCode >= 200 && response.StatusCode < 300 && !isSSE {
		translated, err := translateBufferedResponse(upstreamBody, namespaceRewrite)
		if err != nil {
			return err
		}
		copyResponseHeader(w.Header(), response.Header, "Content-Type")
		copyResponseHeader(w.Header(), response.Header, "Cache-Control")
		copyResponseHeader(w.Header(), response.Header, "X-Accel-Buffering")
		w.WriteHeader(response.StatusCode)
		_, err = w.Write(translated)
		return err
	}

	copyResponseHeader(w.Header(), response.Header, "Content-Type")
	copyResponseHeader(w.Header(), response.Header, "Cache-Control")
	copyResponseHeader(w.Header(), response.Header, "X-Accel-Buffering")
	w.WriteHeader(response.StatusCode)
	if namespaceRewrite != nil && response.StatusCode >= 200 && response.StatusCode < 300 && isSSE {
		return copyTranslatedSSE(w, upstreamBody, namespaceRewrite)
	}

	buffer := make([]byte, 32<<10)
	for {
		read, readErr := upstreamBody.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	if !validRequestHost(r.Host) {
		s.writeError(w, http.StatusForbidden, "invalid_host", "request Host must be loopback", "")
		return
	}
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported", "")
		return
	}
	if modelID, operation, matched, err := parseModelControlPath(r.URL.EscapedPath()); matched {
		if !requireMethod(w, r, http.MethodPost) || !s.requireAdmin(w, r) {
			return
		}
		if err != nil {
			s.writeError(w, http.StatusNotFound, "unknown_path", "route not found", "")
			return
		}
		s.handleModelControl(w, r, modelID, operation)
		return
	}
	if operation, matched, err := parseMaintenancePath(r.URL.EscapedPath()); matched {
		if !requireMethod(w, r, http.MethodPost) || !s.requireAdmin(w, r) {
			return
		}
		if err != nil {
			s.writeError(w, http.StatusNotFound, "unknown_path", "route not found", "")
			return
		}
		s.handleMaintenance(w, r, operation)
		return
	}

	switch r.URL.Path {
	case "/livez":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "cia-edge"})
	case "/readyz":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		capacity, _ := s.capacityFor(r.Context(), s.publicModel())
		upstreamReachable := s.upstreamReachable(r.Context())
		// A drained provider is deliberately not ready: readiness is the signal
		// anything gating traffic consults, and during maintenance it must say
		// so. /livez stays up, which is what the cutover transaction watches.
		maintenance := s.gate.maintenance(time.Now())
		ready := upstreamReachable && capacity.Available && !maintenance.Draining
		status := http.StatusOK
		state := "ready"
		if !ready {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		if maintenance.Draining {
			w.Header().Set("Retry-After", maintenanceRetryAfterS)
		}
		s.writeJSON(w, status, map[string]any{
			"status":             state,
			"service":            "cia-edge",
			"upstream_reachable": upstreamReachable,
			"maintenance":        maintenance,
		})
	case "/metrics":
		if !requireMethod(w, r, http.MethodGet) || !s.requireAdmin(w, r) {
			return
		}
		s.writeMetrics(w)
	case "/api/v1/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		s.writeStatus(w, r)
	case inferenceTelemetryPath:
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		s.writeInferenceTelemetry(w)
	default:
		s.writeError(w, http.StatusNotFound, "unknown_path", "route not found", "")
	}
}

func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request) {
	running, runningErr := s.runningModels(r.Context())
	memory, metricErr := s.memoryStatus()
	capacity := capacityFrom(s.publicModel(), s.cfg.Models, running, runningErr, memory, metricErr)
	modelStatuses := make([]map[string]any, 0, len(s.cfg.Models))
	runtimes := make([]RuntimeSummary, 0, len(s.cfg.Models))
	seenRuntimes := make(map[string]struct{}, len(s.cfg.Models))
	for _, model := range s.cfg.Models {
		modelCapacity := capacityFrom(model, s.cfg.Models, running, runningErr, memory, metricErr)
		routerState, active := running[model.ID]
		// A weights file that is gone or the wrong size outranks a memory
		// verdict: no amount of headroom serves a model that is not on disk.
		artifact := checkArtifact(model.ArtifactPath, model.ArtifactBytes)
		available, reason := modelCapacity.Available, modelCapacity.Reason
		if refused := artifact.refuse(); refused != "" {
			available, reason = false, refused
		}
		modelStatuses = append(modelStatuses, map[string]any{
			"id": model.ID, "available": available,
			"active": active, "process_state": processState(routerState, active),
			"reason": reason, "capacity": modelCapacity, "artifact": artifact,
			"runtime": model.Runtime, "context_tokens": model.ContextTokens,
			"profile": model.Profile,
			// Reported together on purpose: a checkpoint configuration is only
			// meaningful on a runtime shown to restore checkpoints on this
			// architecture, and the pair is what says whether the deployment is
			// actually getting incremental reuse or only asking for it.
			"checkpoints": map[string]any{
				"configured":          model.Checkpoints.Configured(),
				"ctx_checkpoints":     model.Checkpoints.Count,
				"checkpoint_min_step": model.Checkpoints.MinStep,
				"runtime_capable":     model.Runtime.CheckpointCapable,
			},
		})
		if model.Runtime.ID == "" {
			continue
		}
		if _, seen := seenRuntimes[model.Runtime.ID]; seen {
			continue
		}
		seenRuntimes[model.Runtime.ID] = struct{}{}
		runtimes = append(runtimes, model.Runtime)
	}
	upstreamReachable := s.upstreamReachable(r.Context())
	activeModel := s.activeModel(running)
	maintenance := s.gate.maintenance(time.Now())
	ready := upstreamReachable && capacity.Available && !maintenance.Draining
	payload := map[string]any{
		"service":        "cia-edge",
		"version":        s.cfg.Version,
		"ready":          ready,
		"uptime_seconds": int64(time.Since(s.startedAt) / time.Second),
		"upstream":       map[string]any{"url": s.cfg.UpstreamURL, "reachable": upstreamReachable},
		"models":         append([]Model(nil), s.cfg.Models...),
		"runtimes":       runtimes,

		"active_model": activeModel,
		"gate":         s.gate.snapshot(),
		"capacity":     capacity,
		// Deliberately not folded into capacity: capacity is a manifest-versus-
		// budget decision that gates admission, while this is a live driver
		// reading that gates nothing. Merging them would make a noisy sample
		// look like grounds for a 503.
		"gpu_memory":     s.gpuPressure(activeModel),
		"maintenance":    maintenance,
		"model_statuses": modelStatuses,
		"recent_events":  s.events.recent(),
	}
	// Release identity is reported only when the deployment transaction
	// installed a manifest for it. It is a sanitized subset by construction:
	// no path, hash inventory, or credential can reach this response.
	if s.cfg.Release != nil {
		payload["deployment"] = map[string]any{
			"environment":      s.cfg.Release.Environment,
			"release":          s.cfg.Release.Release,
			"version":          s.cfg.Release.Version,
			"commit":           s.cfg.Release.Commit,
			"source_dirty":     s.cfg.Release.SourceDirty,
			"previous_release": s.cfg.Release.PreviousRelease,
			"status":           s.cfg.Release.Status,
			"created_utc":      s.cfg.Release.CreatedUTC,
			"healthy":          ready,
		}
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *Server) writeMetrics(w http.ResponseWriter) {
	gate := s.gate.snapshot()
	maintenance := s.gate.maintenance(time.Now())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	writeCounter(w, "cia_edge_requests_total", "HTTP requests observed on either plane.", s.metrics.requests.Load())
	writeCounter(w, "cia_edge_auth_failures_total", "Requests refused for an invalid inference or administrative credential.", s.metrics.authFailures.Load())
	writeCounter(w, "cia_edge_invalid_requests_total", "Requests refused by body decoding or payload validation.", s.metrics.invalidRequests.Load())
	writeCounter(w, "cia_edge_upstream_failures_total", "Upstream router calls that failed.", s.metrics.upstreamFailures.Load())
	writeGauge(w, "cia_edge_active_requests", "Inference requests holding an admission slot.", gate.Active)
	writeGauge(w, "cia_edge_queued_requests", "Inference requests waiting for an admission slot.", gate.Queued)
	writeCounter(w, "cia_edge_queue_rejections_total", "Requests refused because the bounded queue was full.", gate.Rejected)
	writeCounter(w, "cia_edge_queue_timeouts_total", "Requests refused after waiting for the queue timeout.", gate.TimedOut)
	writeGauge(w, "cia_edge_uptime_seconds", "Seconds since this edge process started serving.", int64(time.Since(s.startedAt)/time.Second))

	// Maintenance lifecycle: 0 running, 1 draining with work in flight,
	// 2 drained and safe to stop. The cutover transaction polls this.
	writeGauge(w, "cia_edge_maintenance_state", "Drain lifecycle: 0 running, 1 draining, 2 drained.", int64(maintenanceLevel(maintenance.State)))
	writeCounter(w, "cia_edge_maintenance_rejections_total", "Inference requests refused because the provider was draining.", maintenance.Rejected)

	writeCounter(w, "cia_edge_model_loads_total", "Administrative model load operations attempted.", s.metrics.modelLoads.Load())
	writeCounter(w, "cia_edge_model_unloads_total", "Administrative model unload operations attempted.", s.metrics.modelUnloads.Load())
	writeCounter(w, "cia_edge_model_switches_total", "Administrative model switch operations attempted.", s.metrics.modelSwitches.Load())
	writeCounter(w, "cia_edge_model_load_failures_total", "Administrative load or switch operations that failed upstream.", s.metrics.modelLoadFailures.Load())
	writeCounter(w, "cia_edge_admin_http_mutations_total", "Administrative mutations accepted over the deprecated HTTP control plane.", s.metrics.httpAdminMutations.Load())
	writeCounter(w, "cia_edge_admin_pipe_mutations_total", "Administrative mutations accepted over the DACL-protected named pipe.", s.metrics.pipeAdminMutations.Load())

	s.gate.queueWait.write(w, "cia_edge_queue_wait_seconds", "Time an admitted request spent waiting for a slot. Requests admitted immediately are not observed.")
	s.metrics.inferenceDuration.write(w, "cia_edge_inference_duration_seconds", "Wall time an admitted inference request held its slot.")
	s.metrics.modelLoadDuration.write(w, "cia_edge_model_load_duration_seconds", "Wall time of an administrative load or switch operation.")

	// Adapter memory, so the degradation that raises no error is alertable. The
	// budget is the public model's declared device VRAM rather than the active
	// model's: this handler has no request context to ask the router which model
	// is loaded, and provider.max_loaded_models is 1, so on a single-runtime
	// deployment the two are the same. Gauges are omitted entirely rather than
	// emitted as zero when the probe is unavailable, because a scrape that
	// records 0 MiB dedicated would look like an idle GPU.
	pressure := s.gpuPressure(s.cfg.PublicModelID)
	if pressure.DedicatedMiB != nil {
		writeFloatGauge(w, "cia_edge_gpu_dedicated_mib", "Dedicated adapter memory currently in use.", "%.0f", *pressure.DedicatedMiB)
		writeFloatGauge(w, "cia_edge_gpu_shared_mib", "Shared system memory currently used by the adapter.", "%.0f", *pressure.SharedMiB)
	}
	if pressure.Occupancy != nil {
		writeFloatGauge(w, "cia_edge_gpu_occupancy_ratio", "Dedicated adapter memory in use over the declared device budget.", "%.4f", *pressure.Occupancy)
	}
	writeGauge(w, "cia_edge_gpu_memory_pressure", "Adapter memory verdict: 0 unknown, rising with pressure.", int64(gpuPressureLevel(pressure.State)))

	// One constant series identifying the installed release. Every label value
	// is manifest-bounded and validated at load, so cardinality is exactly one
	// for the lifetime of the process.
	writeReleaseInfo(w, s.cfg.Release)
}

func (s *Server) upstreamReachable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(probeCtx, "tcp", s.upstream.Host)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.authorized(r, s.cfg.AdminToken) {
		return true
	}
	s.metrics.authFailures.Add(1)
	w.Header().Set("WWW-Authenticate", `Bearer realm="cia-edge-admin"`)
	s.writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid administrative credential", "")
	return false
}

func (s *Server) authorized(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	provided := strings.TrimPrefix(value, "Bearer ")
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// processState reports where the router's process for a model is in its life:
// "starting" is a model being loaded, which is otherwise indistinguishable from
// a slow prompt. The router's word is passed through only when it is one of
// its known states, so the status never carries text the edge did not vet.
func processState(routerState string, running bool) string {
	if !running {
		return "stopped"
	}
	switch routerState {
	case "starting", "ready", "stopping", "shutdown", "stopped":
		return routerState
	default:
		return "unknown"
	}
}

func validRequestHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
	return false
}

type payloadError struct {
	Status  int
	Code    string
	Message string
	Param   string
}

func (e *payloadError) Error() string { return e.Code }

func validatePayload(path string, body []byte) (string, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return "", errors.New("invalid JSON object")
	}
	modelRaw, ok := payload["model"]
	if !ok {
		return "", &payloadError{Status: http.StatusBadRequest, Code: "missing_model", Message: "model is required", Param: "model"}
	}
	var model string
	if err := json.Unmarshal(modelRaw, &model); err != nil || strings.TrimSpace(model) == "" {
		return "", &payloadError{Status: http.StatusBadRequest, Code: "invalid_model", Message: "model must be a non-empty string", Param: "model"}
	}
	if path == "/v1/responses" {
		if enabledField(payload, "store") {
			return "", unsupportedField("store")
		}
		if enabledField(payload, "background") {
			return "", unsupportedField("background")
		}
		if value, present := payload["previous_response_id"]; present && string(value) != "null" {
			return "", unsupportedField("previous_response_id")
		}
	}
	return model, nil
}

func enabledField(payload map[string]json.RawMessage, field string) bool {
	value, present := payload[field]
	if !present {
		return false
	}
	var enabled bool
	return json.Unmarshal(value, &enabled) == nil && enabled
}

func unsupportedField(field string) *payloadError {
	return &payloadError{
		Status:  http.StatusBadRequest,
		Code:    "unsupported_feature",
		Message: field + " is not supported by the stateless local provider",
		Param:   field,
	}
}

func (s *Server) writeGateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errQueueFull):
		w.Header().Set("Retry-After", "1")
		s.writeError(w, http.StatusTooManyRequests, "queue_full", "local inference queue is full", "")
	case errors.Is(err, errQueueTimeout):
		w.Header().Set("Retry-After", "1")
		s.writeError(w, http.StatusTooManyRequests, "queue_timeout", "timed out waiting for local inference capacity", "")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.writeError(w, 499, "client_closed_request", "request was canceled while queued", "")
	case errors.Is(err, errControlBusy):
		s.writeError(w, http.StatusServiceUnavailable, "model_control_in_progress", "model control operation is in progress", "")
	case errors.Is(err, errDraining):
		w.Header().Set("Retry-After", maintenanceRetryAfterS)
		s.writeError(w, http.StatusServiceUnavailable, "maintenance_draining", "local inference is draining for maintenance", "")
	default:
		s.writeError(w, http.StatusServiceUnavailable, "admission_unavailable", "local inference admission is unavailable", "")
	}
}

func (s *Server) writeBodyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWireTooLarge), errors.Is(err, errDecodedTooLarge), errors.Is(err, errCompressionRatio):
		s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds configured limits", "")
	case errors.Is(err, errUnsupportedEncoding):
		s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_content_encoding", "Content-Encoding must be identity, gzip, or zstd", "")
	default:
		s.writeError(w, http.StatusBadRequest, "invalid_compressed_body", "request body could not be decoded", "")
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message, param string) {
	writeAPIError(w, status, code, message, param)
}

func writeAPIError(w http.ResponseWriter, status int, code, message, param string) {
	type apiError struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param,omitempty"`
		Code    string `json:"code"`
	}
	writeJSONResponse(w, status, map[string]any{
		"error": apiError{Message: message, Type: "invalid_request_error", Param: param, Code: code},
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONResponse(w, status, value)
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func copyResponseHeader(destination, source http.Header, name string) {
	if value := source.Get(name); value != "" {
		destination.Set(name, value)
	}
}

func headersWritten(w http.ResponseWriter) bool {
	if recorder, ok := w.(*statusWriter); ok {
		return recorder.status != 0
	}
	return false
}
