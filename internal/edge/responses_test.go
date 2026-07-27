package edge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeResponsesAuthorityMessages(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"instructions":"base",
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer one"},{"type":"input_text","text":"developer two"}],"internal_chat_message_metadata_passthrough":{"opaque":true}},
			{"type":"message","role":"system","content":"system"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)
	normalized, err := normalizeResponsesAuthorityMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatal(err)
	}
	var instructions string
	if err := json.Unmarshal(payload["instructions"], &instructions); err != nil {
		t.Fatal(err)
	}
	if instructions != "base\n\ndeveloper one\n\ndeveloper two\n\nsystem" {
		t.Fatalf("instructions = %q", instructions)
	}
	var input []map[string]any
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 1 || input[0]["role"] != "user" {
		t.Fatalf("unexpected normalized input: %#v", input)
	}
}

func TestNormalizeResponsesAuthorityMessagesLeavesStringInputUntouched(t *testing.T) {
	body := []byte(`{"model":"local-coding","instructions":"base","input":"hello"}`)
	normalized, err := normalizeResponsesAuthorityMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != string(body) {
		t.Fatalf("string input changed: %s", normalized)
	}
}

func TestNormalizeResponsesAuthorityMessagesRejectsLateAuthority(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"input":[
			{"role":"user","content":"hello"},
			{"role":"developer","content":"late"}
		]
	}`)
	_, err := normalizeResponsesAuthorityMessages(body)
	var validation *payloadError
	if !errors.As(err, &validation) || validation.Code != "unsupported_feature" || validation.Param != "input[1].role" {
		t.Fatalf("error = %#v, want late-authority unsupported_feature", err)
	}
}

func TestNormalizeResponsesAuthorityMessagesRejectsNonTextAuthority(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"input":[
			{"role":"developer","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
			{"role":"user","content":"hello"}
		]
	}`)
	_, err := normalizeResponsesAuthorityMessages(body)
	var validation *payloadError
	if !errors.As(err, &validation) || validation.Code != "unsupported_feature" || !strings.Contains(validation.Param, ".content[0].type") {
		t.Fatalf("error = %#v, want non-text unsupported_feature", err)
	}
}
