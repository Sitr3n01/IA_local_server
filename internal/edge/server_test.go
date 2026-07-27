package edge

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	testInferenceToken = "inference-test-token-000000000000"
	testAdminToken     = "admin-test-token-00000000000000000"
	testRouterToken    = "router-test-token-0000000000000000"
)

func testConfig(upstream string) Config {
	cfg := DefaultConfig()
	cfg.UpstreamURL = upstream
	cfg.InferenceToken = testInferenceToken
	cfg.AdminToken = testAdminToken
	cfg.RouterToken = testRouterToken
	cfg.LogOutput = io.Discard
	cfg.QueueWait = 100 * time.Millisecond
	return cfg
}

func newTestServer(t *testing.T, upstream http.Handler) (*Server, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(upstream)
	t.Cleanup(backend.Close)
	server, err := New(testConfig(backend.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server, backend
}

func dataRequest(t *testing.T, handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:8090"+path, bytes.NewReader(body))
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, recorder.Body.String())
	}
	return response.Error.Code
}

func TestModelsIsStaticAndSideEffectFree(t *testing.T) {
	var upstreamRequests atomic.Int64
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))

	recorder := dataRequest(t, server.DataHandler(), http.MethodGet, "/v1/models", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if upstreamRequests.Load() != 0 {
		t.Fatalf("GET /v1/models reached upstream %d times", upstreamRequests.Load())
	}
	if !strings.Contains(recorder.Body.String(), `"id":"local-coding"`) {
		t.Fatalf("response does not contain allowlisted model: %s", recorder.Body.String())
	}
	if recorder.Header().Get("X-Request-Id") == "" {
		t.Fatal("X-Request-Id is missing")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("data plane unexpectedly enabled CORS")
	}
}

func TestProxyDecodesZstdAndDoesNotForwardClientCredentials(t *testing.T) {
	var gotBody []byte
	var gotAuthorization, gotCookie, gotEncoding string
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		gotEncoding = r.Header.Get("Content-Encoding")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	body := []byte(`{"model":"local-coding","input":"hello"}`)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(body, nil)
	encoder.Close()

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/v1/responses", bytes.NewReader(compressed))
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Cookie", "session=must-not-leak")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "zstd")
	recorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("upstream body = %q, want %q", gotBody, body)
	}
	if gotAuthorization != "Bearer "+testRouterToken {
		t.Fatalf("upstream Authorization = %q, want router credential", gotAuthorization)
	}
	if gotCookie != "" {
		t.Fatalf("client Cookie leaked upstream: %q", gotCookie)
	}
	if gotEncoding != "" {
		t.Fatalf("decoded request retained Content-Encoding: %q", gotEncoding)
	}
}

func TestProxyDecodesGzip(t *testing.T) {
	var gotBody []byte
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	body := []byte(`{"model":"local-coding","messages":[]}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(body)
	_ = writer.Close()

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/v1/chat/completions", &compressed)
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("upstream body = %q, want %q", gotBody, body)
	}
}

func TestChatCompletionsWrappedFunctionToolReachesUpstream(t *testing.T) {
	var gotBody []byte
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	body := []byte(`{
		"model":"local-coding",
		"messages":[{"role":"user","content":"synthetic"}],
		"tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object","properties":{}}}}],
		"tool_choice":{"type":"function","function":{"name":"read_file"}}
	}`)
	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("upstream body changed:\n%s", gotBody)
	}
}

func TestUnknownModelFailsClosed(t *testing.T) {
	var upstreamRequests atomic.Int64
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"cloud-model","input":"secret"}`))
	if recorder.Code != http.StatusNotFound || errorCode(t, recorder) != "model_not_found" {
		t.Fatalf("unexpected response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if upstreamRequests.Load() != 0 {
		t.Fatalf("unknown model reached upstream %d times", upstreamRequests.Load())
	}
}

func TestResponsesRejectsStatefulFeatures(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unsupported request reached upstream")
	}))
	tests := []string{
		`{"model":"local-coding","store":true}`,
		`{"model":"local-coding","background":true}`,
		`{"model":"local-coding","previous_response_id":"resp_123"}`,
	}
	for _, body := range tests {
		recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(body))
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "unsupported_feature" {
			t.Errorf("body %s: status=%d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestUnsupportedToolTypesFailBeforeUpstream(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unsupported tool request reached upstream")
	}))
	for _, path := range []string{"/v1/responses", "/v1/chat/completions"} {
		recorder := dataRequest(t, server.DataHandler(), http.MethodPost, path, []byte(`{
			"model":"local-coding",
			"tools":[{"type":"custom","name":"apply_patch"}]
		}`))
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "unsupported_feature" {
			t.Errorf("path %s: status=%d response=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestInvalidNamespacedUpstreamResponseFailsBeforeSuccessStatus(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{invalid`)
	}))
	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{
		"model":"local-coding",
		"input":"hello",
		"tools":[{"type":"namespace","name":"local","tools":[{"type":"function","name":"health","parameters":{"type":"object"}}]}]
	}`))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "upstream_unavailable" {
		t.Fatalf("status=%d body=%s, want deterministic 503", recorder.Code, recorder.Body.String())
	}
}

func TestAuthenticationAndHostValidation(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	missingAuth := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/v1/models", nil)
	missingAuth.Host = "127.0.0.1:8090"
	missingRecorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(missingRecorder, missingAuth)
	if missingRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want 401", missingRecorder.Code)
	}

	badHost := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/v1/models", nil)
	badHost.Host = "attacker.example:8090"
	badHost.Header.Set("Authorization", "Bearer "+testInferenceToken)
	badHostRecorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(badHostRecorder, badHost)
	if badHostRecorder.Code != http.StatusForbidden || errorCode(t, badHostRecorder) != "invalid_host" {
		t.Fatalf("bad host response: status=%d body=%s", badHostRecorder.Code, badHostRecorder.Body.String())
	}
}

func TestControlPlaneHealthPublicStatusAndAdminAuth(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler := server.ControlHandler()

	live := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8091/livez", nil)
	live.Host = "127.0.0.1:8091"
	liveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(liveRecorder, live)
	if liveRecorder.Code != http.StatusOK {
		t.Fatalf("livez status = %d", liveRecorder.Code)
	}

	status := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8091/api/v1/status", nil)
	status.Host = "127.0.0.1:8091"
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, status)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("unauthenticated read-only status = %d, want 200", statusRecorder.Code)
	}

	if strings.Contains(statusRecorder.Body.String(), testAdminToken) {
		t.Fatal("administrative token leaked into status response")
	}

	admin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091/api/v1/models/local-coding:load", nil)
	admin.Host = "127.0.0.1:8091"
	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, admin)
	if adminRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated administrative operation = %d, want 401", adminRecorder.Code)
	}
}

func TestMetadataLogsDoNotContainUnknownPathOrCredentials(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	var logs bytes.Buffer
	cfg := testConfig(backend.URL)
	cfg.LogOutput = &logs
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/secret-in-path", nil)
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	recorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(recorder, request)
	if strings.Contains(logs.String(), "secret-in-path") || strings.Contains(logs.String(), testInferenceToken) {
		t.Fatalf("metadata log leaked request data: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"path":"unknown"`) {
		t.Fatalf("unknown path was not sanitized: %s", logs.String())
	}
}

func TestStreamingFlushesAndPropagatesCancellation(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	firstChunk := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":[]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer backend.Close()
	server, err := New(testConfig(backend.URL))
	if err != nil {
		t.Fatal(err)
	}
	edgeHTTP := httptest.NewServer(server.DataHandler())
	defer edgeHTTP.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeHTTP.URL+"/v1/responses", strings.NewReader(`{"model":"local-coding","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := edgeHTTP.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstChunk:
	case <-time.After(time.Second):
		t.Fatal("upstream did not write first SSE chunk")
	}
	buffer := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, buffer); err != nil {
		t.Fatalf("read flushed SSE chunk: %v", err)
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation was not propagated upstream")
	}
}
