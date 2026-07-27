package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeNamespacedToolsAndRestoreFunctionCall(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"input":[{"type":"function_call","namespace":"mcp__cia__","name":"local_ai_health","arguments":"{}","call_id":"call_1"}],
		"tools":[
			{"type":"function","name":"shell_command","description":"run","parameters":{"type":"object"}},
			{"type":"namespace","name":"mcp__cia__","description":"local AI","tools":[
				{"type":"function","name":"local_ai_health","description":"health","strict":false,"defer_loading":true,"parameters":{"type":"object"}}
			]}
		]
	}`)
	normalized, rewrite, err := normalizeNamespacedTools("/v1/responses", body)
	if err != nil {
		t.Fatal(err)
	}
	if rewrite == nil {
		t.Fatal("namespace rewrite was not created")
	}
	var payload map[string]any
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatal(err)
	}
	tools := payload["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
	}
	expanded := tools[1].(map[string]any)
	if expanded["type"] != "function" || expanded["name"] != "mcp__cia__local_ai_health" {
		t.Fatalf("unexpected expanded tool: %#v", expanded)
	}
	if _, present := expanded["defer_loading"]; present {
		t.Fatal("defer_loading leaked to llama.cpp")
	}
	input := payload["input"].([]any)[0].(map[string]any)
	if input["name"] != "mcp__cia__local_ai_health" {
		t.Fatalf("input name was not flattened: %#v", input)
	}
	if _, present := input["namespace"]; present {
		t.Fatalf("input namespace was not removed: %#v", input)
	}

	response := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"mcp__cia__local_ai_health","arguments":"{}","call_id":"call_1"}}`)
	restored, err := rewrite.rewriteResponseJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), `"namespace":"mcp__cia__"`) || !strings.Contains(string(restored), `"name":"local_ai_health"`) {
		t.Fatalf("response namespace was not restored: %s", restored)
	}
}

func TestCopyTranslatedSSERestoresNamespace(t *testing.T) {
	rewrite := &namespaceRewrite{byFlat: map[string]namespaceTarget{
		"mcp__cia__local_ai_health": {Namespace: "mcp__cia__", Name: "local_ai_health"},
	}}
	input := "event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"mcp__cia__local_ai_health","arguments":"{}","call_id":"call_1"}}` + "\n\n" +
		"data: [DONE]\n\n"
	recorder := httptest.NewRecorder()
	if err := copyTranslatedSSE(recorder, strings.NewReader(input), rewrite); err != nil {
		t.Fatal(err)
	}
	output := recorder.Body.String()
	if !strings.Contains(output, `"namespace":"mcp__cia__"`) || !strings.Contains(output, `"name":"local_ai_health"`) {
		t.Fatalf("namespace was not restored: %s", output)
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Fatalf("DONE marker was not preserved: %s", output)
	}
}

func TestCopyTranslatedSSERejectsInvalidJSONEvent(t *testing.T) {
	rewrite := &namespaceRewrite{byFlat: map[string]namespaceTarget{}}
	recorder := httptest.NewRecorder()
	err := copyTranslatedSSE(recorder, bytes.NewBufferString("data: {invalid}\n\n"), rewrite)
	if err == nil || !strings.Contains(err.Error(), "translate upstream SSE event") {
		t.Fatalf("error = %v, want translation failure", err)
	}
}

func TestTranslateBufferedResponseRejectsOversize(t *testing.T) {
	rewrite := &namespaceRewrite{byFlat: map[string]namespaceTarget{}}
	_, err := translateBufferedResponse(io.LimitReader(zeroReader{}, maxBufferedResponseBytes+1), rewrite)
	if err == nil || !strings.Contains(err.Error(), "buffered response limit") {
		t.Fatalf("error = %v, want buffered response limit", err)
	}
}

func TestCopyTranslatedSSERejectsOversizeLine(t *testing.T) {
	rewrite := &namespaceRewrite{byFlat: map[string]namespaceTarget{}}
	recorder := httptest.NewRecorder()
	input := io.MultiReader(io.LimitReader(zeroReader{}, maxSSELineBytes+1), strings.NewReader("\n"))
	err := copyTranslatedSSE(recorder, input, rewrite)
	if err == nil || !strings.Contains(err.Error(), "read upstream SSE line") {
		t.Fatalf("error = %v, want SSE line limit", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func TestNormalizeNamespacedToolsRejectsCustomTool(t *testing.T) {
	_, _, err := normalizeNamespacedTools("/v1/responses", []byte(`{"model":"local-coding","tools":[{"type":"custom","name":"apply_patch"}]}`))
	var validation *payloadError
	if !errors.As(err, &validation) || validation.Code != "unsupported_feature" {
		t.Fatalf("error = %#v, want unsupported_feature", err)
	}
}

func TestNormalizeChatCompletionsAcceptsWrappedFunctionTool(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object"}}}],
		"tool_choice":{"type":"function","function":{"name":"read_file"}}
	}`)
	normalized, rewrite, err := normalizeNamespacedTools("/v1/chat/completions", body)
	if err != nil {
		t.Fatal(err)
	}
	if rewrite != nil {
		t.Fatal("plain Chat Completions tools unexpectedly created a namespace rewrite")
	}
	if !bytes.Equal(normalized, body) {
		t.Fatalf("Chat Completions payload changed:\n%s", normalized)
	}
}

func TestFunctionToolShapeIsRouteSpecific(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      string
		wantParam string
	}{
		{
			name:      "responses rejects wrapped chat tool",
			path:      "/v1/responses",
			body:      `{"model":"local-coding","tools":[{"type":"function","function":{"name":"read_file"}}]}`,
			wantParam: "tools[0].name",
		},
		{
			name:      "chat rejects flat responses tool",
			path:      "/v1/chat/completions",
			body:      `{"model":"local-coding","tools":[{"type":"function","name":"read_file"}]}`,
			wantParam: "tools[0].function.name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := normalizeNamespacedTools(test.path, []byte(test.body))
			var validation *payloadError
			if !errors.As(err, &validation) || validation.Code != "invalid_tools" || validation.Param != test.wantParam {
				t.Fatalf("error = %#v, want invalid_tools at %s", err, test.wantParam)
			}
		})
	}
}
