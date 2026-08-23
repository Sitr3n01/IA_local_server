package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	modelControlPrefix = "/api/v1/models/"
	maintenancePrefix  = "/api/v1/maintenance:"
)

var errNonEmptyControlBody = errors.New("model control requests must have an empty body")

func parseModelControlPath(escapedPath string) (modelID, operation string, matched bool, err error) {
	if !strings.HasPrefix(escapedPath, modelControlPrefix) {
		return "", "", false, nil
	}
	remainder := strings.TrimPrefix(escapedPath, modelControlPrefix)
	for _, candidate := range []string{"load", "unload", "switch"} {
		suffix := ":" + candidate
		if !strings.HasSuffix(remainder, suffix) {
			continue
		}
		escapedID := strings.TrimSuffix(remainder, suffix)
		if escapedID == "" {
			return "", "", true, errors.New("model ID is missing")
		}
		id, unescapeErr := url.PathUnescape(escapedID)
		if unescapeErr != nil || id == "" {
			return "", "", true, errors.New("model ID is invalid")
		}
		return id, candidate, true, nil
	}
	return "", "", true, errors.New("model operation is unknown")
}

// parseMaintenancePath recognises the two narrow maintenance verbs. The prefix
// is matched before the verb so an unknown verb fails as an unknown route
// rather than silently falling through to the generic switch.
func parseMaintenancePath(escapedPath string) (operation string, matched bool, err error) {
	if !strings.HasPrefix(escapedPath, maintenancePrefix) {
		return "", false, nil
	}
	remainder := strings.TrimPrefix(escapedPath, maintenancePrefix)
	if remainder == "drain" || remainder == "resume" {
		return remainder, true, nil
	}
	return "", true, errors.New("maintenance operation is unknown")
}

// controlResult is the single success contract shared by every administrative
// transport. Its JSON shape is the one cia-mcp-admin and the tray already
// parse, so the named-pipe transport reuses it byte for byte.
type controlResult struct {
	Operation   string `json:"operation"`
	Model       string `json:"model,omitempty"`
	Status      string `json:"status"`
	ActiveModel string `json:"active_model"`
}

func (s *Server) handleModelControl(w http.ResponseWriter, r *http.Request, modelID, operation string) {
	if err := ensureEmptyBody(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "model control requests require an empty body", "")
		return
	}
	s.metrics.httpAdminMutations.Add(1)
	w.Header().Set("X-CIA-Admin-Transport", "http-deprecated")

	result, failure := s.performModelControl(r.Context(), modelID, operation)
	if failure != nil {
		s.writeError(w, failure.Status, failure.Code, failure.Message, failure.Param)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// performModelControl is the transport-neutral administrative operation. HTTP
// and the DACL-protected named pipe both enter here, so admission policy,
// capacity refusal, and the router calls cannot drift apart between them.
func (s *Server) performModelControl(ctx context.Context, modelID, operation string) (controlResult, *payloadError) {
	model, allowed := s.modelByID(modelID)
	if !allowed {
		return controlResult{}, &payloadError{
			Status:  http.StatusNotFound,
			Code:    "model_not_found",
			Message: "requested model is not available",
			Param:   "model",
		}
	}

	// Draining does not relax this: beginControl still requires an idle gate, so
	// a control operation issued mid-drain is refused with 409 until the last
	// active and queued request has finished. Once drained, it proceeds.
	endControl, available := s.gate.beginControl()
	if !available {
		return controlResult{}, &payloadError{
			Status:  http.StatusConflict,
			Code:    "inference_busy",
			Message: "model control is unavailable while inference is active or queued",
		}
	}
	defer endControl()

	activeModel := ""
	if operation == "load" || operation == "switch" {
		capacity, running := s.capacityFor(ctx, model)
		if !capacity.Available {
			return controlResult{}, &payloadError{
				Status:  http.StatusServiceUnavailable,
				Code:    "insufficient_capacity",
				Message: capacityMessage(capacity.Reason),
				Param:   "model",
			}
		}
		activeModel = s.activeModel(running)
		if operation == "load" && activeModel != "" && activeModel != modelID {
			return controlResult{}, &payloadError{
				Status:  http.StatusConflict,
				Code:    "model_conflict",
				Message: "another model is already loaded; use switch",
				Param:   "model",
			}
		}
	}

	// Once an authenticated operation is admitted, finish it independently of
	// the client connection. In particular, a switch must not stop after unload
	// merely because the panel window closed or its HTTP timeout elapsed.
	operationCtx, cancelOperation := context.WithTimeout(context.Background(), s.cfg.HeaderTimeout)
	defer cancelOperation()

	started := time.Now()
	var err error
	switch operation {
	case "load":
		err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
	case "unload":
		err = s.routerOperation(operationCtx, http.MethodPost, "/api/models/unload/"+url.PathEscape(modelID))
	case "switch":
		if activeModel == modelID {
			err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
		} else if err = s.routerOperation(operationCtx, http.MethodPost, "/api/models/unload"); err == nil {
			err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
		}
	default:
		return controlResult{}, &payloadError{
			Status:  http.StatusNotFound,
			Code:    "unknown_path",
			Message: "route not found",
		}
	}
	s.metrics.recordModelOperation(operation, time.Since(started), err == nil)
	if err != nil {
		s.metrics.upstreamFailures.Add(1)
		return controlResult{}, &payloadError{
			Status:  http.StatusServiceUnavailable,
			Code:    "upstream_unavailable",
			Message: "local model control operation failed",
		}
	}

	running, _ := s.runningModels(operationCtx)
	return controlResult{
		Operation:   operation,
		Model:       modelID,
		Status:      "completed",
		ActiveModel: s.activeModel(running),
	}, nil
}

func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request, operation string) {
	if err := ensureEmptyBody(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "maintenance requests require an empty body", "")
		return
	}
	s.metrics.httpAdminMutations.Add(1)
	w.Header().Set("X-CIA-Admin-Transport", "http-deprecated")
	s.writeJSON(w, http.StatusOK, s.performMaintenance(operation))
}

// performMaintenance is the transport-neutral drain/resume operation. Both verbs
// are idempotent, so a repeated drain reports the original start time and a
// repeated resume is a no-op.
func (s *Server) performMaintenance(operation string) map[string]any {
	var snapshot maintenanceSnapshot
	switch operation {
	case "drain":
		snapshot = s.gate.drain(time.Now())
	default:
		snapshot = s.gate.resume()
	}
	return map[string]any{
		"operation":   operation,
		"status":      "completed",
		"maintenance": snapshot,
	}
}

func ensureEmptyBody(r *http.Request) error {
	if r.ContentLength > 0 {
		return errNonEmptyControlBody
	}
	if r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return err
	}
	if len(data) != 0 {
		return errNonEmptyControlBody
	}
	return nil
}

func (s *Server) routerOperation(ctx context.Context, method, path string) error {
	request, err := s.newRouterRequest(ctx, method, path)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("router operation returned status %d", response.StatusCode)
	}
	return nil
}

func (s *Server) newRouterRequest(ctx context.Context, method, path string) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return nil, errors.New("invalid router path")
	}
	target := strings.TrimSuffix(s.cfg.UpstreamURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cia-edge/"+s.cfg.Version)
	if s.cfg.RouterToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.cfg.RouterToken)
	}
	return request, nil
}
