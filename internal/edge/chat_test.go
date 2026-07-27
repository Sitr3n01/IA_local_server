package edge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeChatAuthorityMessages(t *testing.T) {
	body := []byte(`{
		"model":"local-coding",
		"messages":[
			{"role":"system","content":"base"},
			{"role":"developer","content":[{"type":"text","text":"developer"}]},
			{"role":"user","content":"hello"}
		]
	}`)
	normalized, err := normalizeChatAuthorityMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" || payload.Messages[0].Content != "base\n\ndeveloper" || payload.Messages[1].Role != "user" {
		t.Fatalf("unexpected normalized messages: %#v", payload.Messages)
	}
}

func TestNormalizeChatAuthorityMessagesRejectsLateAuthority(t *testing.T) {
	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hello"},{"role":"system","content":"late"}]}`)
	_, err := normalizeChatAuthorityMessages(body)
	var validation *payloadError
	if !errors.As(err, &validation) || validation.Code != "unsupported_feature" || validation.Param != "messages[1].role" {
		t.Fatalf("error = %#v, want late-authority unsupported_feature", err)
	}
}

func TestNormalizeChatAuthorityMessagesRejectsNonTextAuthority(t *testing.T) {
	body := []byte(`{"model":"local-coding","messages":[{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"user","content":"hello"}]}`)
	_, err := normalizeChatAuthorityMessages(body)
	var validation *payloadError
	if !errors.As(err, &validation) || validation.Code != "unsupported_feature" || !strings.Contains(validation.Param, ".content[0].type") {
		t.Fatalf("error = %#v, want non-text unsupported_feature", err)
	}
}
