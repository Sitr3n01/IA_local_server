package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func anthropicErrorType(t *testing.T, body string) string {
	t.Helper()
	var response struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode Anthropic error: %v; body=%s", err, body)
	}
	if response.Type != "error" {
		t.Fatalf("response type=%q, want error; body=%s", response.Type, body)
	}
	return response.Error.Type
}

func anthropicRequest(t *testing.T, handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:8090"+path, bytes.NewReader(body))
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testClaudeGatewayToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAnthropicMessagesTranslatesTextSystemAndToolsWithoutSelfProxy(t *testing.T) {
	var path string
	var gotAuthorization, gotCookie string
	var upstream map[string]any
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		path = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &upstream); err != nil {
			t.Fatalf("decode canonical request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","choices":[{"message":{"content":"Use the tool.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Sao Paulo\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`)
	}))
	clientModel := claudeExternalModelID("local-coding")
	body := []byte(`{
		"model":"` + clientModel + `",
		"max_tokens":64,
		"system":"system rules",
		"messages":[{"role":"user","content":"weather?"}],
		"tools":[{"name":"weather","description":"lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]
	}`)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/v1/messages", bytes.NewReader(body))
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Authorization", "Bearer "+testClaudeGatewayToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "must-not-leak")
	recorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q, want canonical chat path", path)
	}
	if gotAuthorization != "Bearer "+testRouterToken || gotCookie != "" {
		t.Fatalf("credentials crossed boundary: authorization=%q cookie=%q", gotAuthorization, gotCookie)
	}
	if upstream["model"] != "local-coding" || upstream["stream"] != false {
		t.Fatalf("canonical model/stream = %#v", upstream)
	}
	messages, ok := upstream["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("canonical messages = %#v", upstream["messages"])
	}
	var response struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input struct {
				City string `json:"city"`
			} `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "message" || response.Model != clientModel || response.StopReason != "tool_use" || len(response.Content) != 2 || response.Content[1].Type != "tool_use" || response.Content[1].ID != "call_1" || response.Content[1].Name != "weather" || response.Content[1].Input.City != "Sao Paulo" || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 5 {
		t.Fatalf("unexpected Anthropic response: %+v", response)
	}
}

func TestAnthropicMessagesFailsClosedForUnknownModelAndToolHistory(t *testing.T) {
	var calls atomic.Int64
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools"`)) {
			t.Errorf("tool definitions reached a toolless model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_text","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	unknown := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"unknown","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`))
	if unknown.Code != http.StatusNotFound || anthropicErrorType(t, unknown.Body.String()) != "not_found_error" {
		t.Fatalf("unknown response status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	server.cfg.Models[0].Capabilities.FunctionCalling = false
	tools := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"x","input_schema":{"type":"object"}}]}`))
	if tools.Code != http.StatusOK {
		t.Fatalf("text fallback status=%d body=%s", tools.Code, tools.Body.String())
	}
	callsAfterTextFallback := calls.Load()
	toolHistory := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{
		"model":"local-coding","max_tokens":4,
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"x","input":{}}]}
		],
		"tools":[{"name":"x","input_schema":{"type":"object"}}]
	}`))
	if toolHistory.Code != http.StatusBadRequest || anthropicErrorType(t, toolHistory.Body.String()) != "invalid_request_error" {
		t.Fatalf("tool history response status=%d body=%s", toolHistory.Code, toolHistory.Body.String())
	}
	if calls.Load() != callsAfterTextFallback {
		t.Fatalf("tool history reached upstream: before=%d after=%d", callsAfterTextFallback, calls.Load())
	}
}

func TestAnthropicMessagesAcceptsClaudeDesktopBetaTransportQueryOnly(t *testing.T) {
	var calls atomic.Int64
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_beta","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	body := []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`)

	accepted := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages?beta=true", body)
	if accepted.Code != http.StatusOK {
		t.Fatalf("Claude Desktop beta query status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	for _, path := range []string{
		"/v1/messages?beta=false",
		"/v1/messages?beta=true&beta=true",
		"/v1/messages?beta=true&cursor=secret",
		"/v1/messages?cursor=secret",
	} {
		rejected := anthropicRequest(t, server.DataHandler(), http.MethodPost, path, body)
		if rejected.Code != http.StatusBadRequest || anthropicErrorType(t, rejected.Body.String()) != "invalid_request_error" {
			t.Errorf("%s: status=%d body=%s", path, rejected.Code, rejected.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want only the accepted beta query", calls.Load())
	}
}

func TestAnthropicMessagesValidatesBodyBeforeInferenceAdmission(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("malformed request reached upstream")
	}))
	server.gate = newGate(1, 0, DefaultQueueWait)
	release, err := server.gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":`))
	if response.Code != http.StatusBadRequest || anthropicErrorType(t, response.Body.String()) != "invalid_request_error" {
		t.Fatalf("malformed queued request status=%d body=%s", response.Code, response.Body.String())
	}
	if snapshot := server.gate.snapshot(); snapshot.Rejected != 0 {
		t.Fatalf("malformed request reached inference gate: %+v", snapshot)
	}
}

func TestDecodeAnthropicRequestDropsEnvelopeExtensionsAndRejectsKnownTypeErrors(t *testing.T) {
	converted, err := decodeAnthropicRequest([]byte(`{
		"model":"local-coding",
		"max_tokens":8,
		"messages":[{"role":"user","content":"hello"}],
		"metadata":{"user_id":"desktop"},
		"thinking":{"type":"disabled"},
		"output_config":{"effort":"low"},
		"tool_choice":{"type":"auto"}
	}`))
	if err != nil {
		t.Fatalf("Claude Desktop envelope extensions were rejected: %v", err)
	}
	for _, key := range []string{"metadata", "thinking", "output_config", "tool_choice"} {
		if bytes.Contains(converted.body, []byte(`"`+key+`"`)) {
			t.Errorf("unimplemented extension %q was forwarded: %s", key, converted.body)
		}
	}

	for _, body := range []string{
		`{"model":"local-coding","max_tokens":"8","messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"local-coding","max_tokens":8,"stream":"yes","messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"local-coding","max_tokens":8,"messages":{"role":"user","content":"hello"}}`,
	} {
		if _, err := decodeAnthropicRequest([]byte(body)); err == nil {
			t.Errorf("known field type error was accepted: %s", body)
		}
	}
}

func TestDecodeAnthropicRequestSupportsMidConversationSystemMessages(t *testing.T) {
	converted, err := decodeAnthropicRequest([]byte(`{
		"model":"local-coding",
		"max_tokens":8,
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"system","content":[{"type":"text","text":"current task context"}]}
		]
	}`))
	if err != nil {
		t.Fatalf("mid-conversation system message was rejected: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(converted.body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 2 || payload.Messages[1].Role != "system" || payload.Messages[1].Content != "current task context" {
		t.Fatalf("canonical messages=%+v", payload.Messages)
	}

	_, err = decodeAnthropicRequest([]byte(`{"model":"local-coding","max_tokens":8,"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hello"}]}`))
	if err == nil || !strings.Contains(err.Error(), "top-level system") {
		t.Fatalf("leading system message error=%v", err)
	}
}

func TestAnthropicSurfaceRequiresDedicatedGatewayCredential(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	withInference := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`))
	if withInference.Code != http.StatusUnauthorized || anthropicErrorType(t, withInference.Body.String()) != "authentication_error" {
		t.Fatalf("inference token reached Anthropic surface: status=%d body=%s", withInference.Code, withInference.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/v1/chat/completions", strings.NewReader(`{"model":"local-coding","messages":[{"role":"user","content":"hello"}]}`))
	request.Host = "127.0.0.1:8090"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testClaudeGatewayToken)
	response := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "invalid_api_key" {
		t.Fatalf("gateway token reached OpenAI surface: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAnthropicMessagesStreamsIncrementally(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello \"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	recorder := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream status/content-type = %d/%q; body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	for _, event := range []string{"event: message_start", "event: content_block_start", "text_delta", "event: content_block_stop", `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(recorder.Body.String(), event) {
			t.Errorf("stream missing %q:\n%s", event, recorder.Body.String())
		}
	}
}
