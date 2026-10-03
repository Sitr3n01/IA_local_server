package edge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCapabilitiesRefuseUnqualifiedFeaturesAndAllowPlainRequests(t *testing.T) {
	base := Capabilities{Responses: true, ChatCompletions: true}
	for _, test := range []struct {
		name, path, body, param string
		caps                    Capabilities
	}{
		{"Responses", "/v1/responses", `{}`, "model", Capabilities{}},
		{"chat", "/v1/chat/completions", `{}`, "model", Capabilities{}},
		{"streaming", "/v1/chat/completions", `{"stream":true}`, "stream", base},
		{"tools", "/v1/chat/completions", `{"tools":[{"type":"function"}]}`, "tools", base},
		{"legacy functions", "/v1/chat/completions", `{"functions":[{"name":"f"}]}`, "tools", base},
		{"forced choice", "/v1/chat/completions", `{"tool_choice":"required"}`, "tools", base},
		{"tool history", "/v1/chat/completions", `{"messages":[{"role":"tool"}]}`, "messages", base},
		{"assistant tool history", "/v1/chat/completions", `{"messages":[{"role":"assistant","tool_calls":[{}]}]}`, "messages", base},
		{"Responses tool result", "/v1/responses", `{"input":[{"type":"function_call_output"}]}`, "input", base},
		{"schema", "/v1/chat/completions", `{"response_format":{"type":"json_schema"}}`, "response_format", base},
		{"JSON mode", "/v1/chat/completions", `{"response_format":{"type":"json_object"}}`, "response_format", base},
		{"Responses schema", "/v1/responses", `{"text":{"format":{"type":"json_schema"}}}`, "text.format", base},
		{"reasoning effort", "/v1/chat/completions", `{"reasoning_effort":"high"}`, "reasoning_effort", base},
		{"reasoning config", "/v1/responses", `{"reasoning":{"effort":"low"}}`, "reasoning", base},
		{"reasoning disabled", "/v1/responses", `{"reasoning":{"effort":"none"}}`, "", base},
		{"plain text", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hello"}]}`, "", base},
		{"empty tools", "/v1/chat/completions", `{"tools":[],"tool_choice":"auto"}`, "", base},
		{"no tools", "/v1/chat/completions", `{"tool_choice":"none"}`, "", base},
		{"Responses text", "/v1/responses", `{"input":"hello","text":{"format":{"type":"text"}}}`, "", base},
		{"qualified features", "/v1/chat/completions", `{"stream":true,"tools":[{}],"response_format":{"type":"json_schema"}}`, "", Capabilities{ChatCompletions: true, Streaming: true, FunctionCalling: true, StructuredOutput: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCapabilities(test.path, []byte(test.body), test.caps)
			if test.param == "" {
				if err != nil {
					t.Fatalf("qualified request refused: %v", err)
				}
			} else if err == nil || err.Param != test.param || err.Code != "unsupported_feature" {
				t.Fatalf("refusal=%v, want unsupported %s", err, test.param)
			}
		})
	}
}

type unreadBody struct{ read bool }

func (b *unreadBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }

func TestDecodedBodyReservationsBoundRequestsBeforeReadingAndAreReleased(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	server.memoryStatus = fixedMemory(64, 32)
	server.bodies = make(chan struct{}, 2)
	first, err := server.reserveBody(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveBody(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/v1/chat/completions", "/v1/messages"} {
		body := &unreadBody{}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090"+route, body)
		r.Host = "127.0.0.1:8090"
		r.Header.Set("Content-Type", "application/json")
		token := testInferenceToken
		if route == "/v1/messages" {
			token = testClaudeGatewayToken
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.DataHandler().ServeHTTP(w, r)
		if w.Code != http.StatusTooManyRequests || body.read {
			t.Fatalf("route %s: status=%d body read=%v", route, w.Code, body.read)
		}
	}
	first()
	second()
	bad := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", []byte(`{broken`))
	if bad.Code != http.StatusBadRequest || len(server.bodies) != 0 {
		t.Fatalf("malformed request retained reservation: status=%d retained=%d", bad.Code, len(server.bodies))
	}
	good := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`))
	if good.Code != http.StatusOK || len(server.bodies) != 0 {
		t.Fatalf("valid request did not recover: status=%d retained=%d", good.Code, len(server.bodies))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.reserveBody(ctx); !errors.Is(err, context.Canceled) || len(server.bodies) != 0 {
		t.Fatalf("canceled reservation: %v", err)
	}
}

func TestNamedToolChoiceRefusesBeforeUpstream(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unqualified tool constraint reached runtime") }))
	for _, test := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"local-coding","messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"echo"}}}`},
		{"/v1/chat/completions", `{"model":"local-coding","messages":[{"role":"user","content":"synthetic"}],"functions":[{"name":"echo","parameters":{"type":"object"}}],"function_call":{"name":"echo"}}`},
		{"/v1/responses", `{"model":"local-coding","input":"synthetic","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"echo"}}`},
		{"/v1/responses", `{"model":"local-coding","input":"synthetic","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","name":"echo","namespace":"functions"}}`},
	} {
		response := dataRequest(t, server.DataHandler(), http.MethodPost, test.path, []byte(test.body))
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "unsupported_feature" {
			t.Fatalf("named choice was accepted: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":10,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"name":"echo","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"echo"}}`))
	if response.Code != http.StatusBadRequest || anthropicErrorType(t, response.Body.String()) != "invalid_request_error" {
		t.Fatalf("Anthropic named choice was accepted: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAnthropicRequiredToolChoiceRefusesAnUnqualifiedModel(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("required tools reached an unqualified runtime") }))
	server.cfg.Models[0].Capabilities.FunctionCalling = false
	for _, choice := range []string{`{"type":"any"}`, `{"type":"tool","name":"read_file"}`} {
		response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"tool_choice":`+choice+`}`))
		if response.Code != http.StatusBadRequest || anthropicErrorType(t, response.Body.String()) != "invalid_request_error" {
			t.Fatalf("required choice status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestAnthropicToolChoiceTranslationAndInvalidPolicies(t *testing.T) {
	for _, test := range []struct {
		name, choice string
		expected     any
		parallel     any
		invalid      bool
	}{
		{"auto", `{"type":"auto"}`, "auto", nil, false},
		{"none", `{"type":"none"}`, "none", nil, false},
		{"any", `{"type":"any"}`, "required", nil, false},
		{"named unqualified", `{"type":"tool","name":"read_file"}`, nil, nil, true},
		{"serial", `{"type":"auto","disable_parallel_tool_use":true}`, "auto", false, false},
		{"parallel", `{"type":"auto","disable_parallel_tool_use":false}`, "auto", true, false},
		{"unknown type", `{"type":"surprise"}`, nil, nil, true},
		{"unknown tool", `{"type":"tool","name":"missing"}`, nil, nil, true},
		{"unknown option", `{"type":"auto","force":true}`, nil, nil, true},
		{"wrong parallel type", `{"type":"auto","disable_parallel_tool_use":"yes"}`, nil, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			converted, err := decodeAnthropicRequest([]byte(`{"model":"local-coding","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"tool_choice":` + test.choice + `}`))
			if test.invalid {
				if err == nil {
					t.Fatal("invalid policy accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var request map[string]any
			if err := json.Unmarshal(converted.body, &request); err != nil {
				t.Fatal(err)
			}
			actual, _ := json.Marshal(request["tool_choice"])
			expected, _ := json.Marshal(test.expected)
			if string(actual) != string(expected) || request["parallel_tool_calls"] != test.parallel {
				t.Fatalf("translated request=%s", converted.body)
			}
		})
	}
}
