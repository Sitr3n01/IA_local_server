package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// normalizeChatAuthorityMessages coalesces the initial system/developer block
// into the single system message supported by the qualified model template.
// Authority messages after conversation content are rejected because moving
// them to the beginning would silently change conversation semantics.
func normalizeChatAuthorityMessages(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, errors.New("invalid JSON object")
	}
	rawMessages, present := payload["messages"]
	if !present {
		return body, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return body, nil
	}

	merged := make([]string, 0, 2)
	authorityCount := 0
	for index, rawMessage := range messages {
		_, authority, err := responsesItemRole(rawMessage)
		if err != nil {
			return nil, invalidChatMessage(index, "messages must be objects with string roles")
		}
		if !authority {
			break
		}
		content, err := chatAuthorityText(rawMessage, index)
		if err != nil {
			return nil, err
		}
		if content != "" {
			merged = append(merged, content)
		}
		authorityCount++
	}

	for index := authorityCount; index < len(messages); index++ {
		_, authority, err := responsesItemRole(messages[index])
		if err != nil {
			continue
		}
		if authority {
			return nil, &payloadError{
				Status:  http.StatusBadRequest,
				Code:    "unsupported_feature",
				Message: "system and developer messages are supported only before conversation messages",
				Param:   fmt.Sprintf("messages[%d].role", index),
			}
		}
	}
	if authorityCount == 0 {
		return body, nil
	}

	coalesced, err := json.Marshal(map[string]any{
		"role":    "system",
		"content": strings.Join(merged, "\n\n"),
	})
	if err != nil {
		return nil, errors.New("authority messages could not be normalized")
	}
	normalizedMessages := make([]json.RawMessage, 0, len(messages)-authorityCount+1)
	normalizedMessages = append(normalizedMessages, coalesced)
	normalizedMessages = append(normalizedMessages, messages[authorityCount:]...)
	encodedMessages, err := json.Marshal(normalizedMessages)
	if err != nil {
		return nil, errors.New("messages could not be normalized")
	}
	payload["messages"] = encodedMessages
	normalized, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("request could not be normalized")
	}
	return normalized, nil
}

func chatAuthorityText(raw json.RawMessage, index int) (string, error) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return "", invalidChatMessage(index, "authority message must be an object")
	}
	rawContent, present := message["content"]
	if !present {
		return "", invalidChatMessage(index, "authority message must contain content")
	}
	var text string
	if json.Unmarshal(rawContent, &text) == nil {
		return text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(rawContent, &parts); err != nil {
		return "", invalidChatMessage(index, "authority content must be text")
	}
	textParts := make([]string, 0, len(parts))
	for partIndex, part := range parts {
		partType, err := requiredString(part, "type")
		if err != nil || (partType != "text" && partType != "input_text") {
			return "", &payloadError{
				Status:  http.StatusBadRequest,
				Code:    "unsupported_feature",
				Message: "authority messages support only text content",
				Param:   fmt.Sprintf("messages[%d].content[%d].type", index, partIndex),
			}
		}
		partText, err := requiredStringAllowEmpty(part, "text")
		if err != nil {
			return "", invalidChatMessage(index, "authority text content must contain text")
		}
		textParts = append(textParts, partText)
	}
	return strings.Join(textParts, "\n\n"), nil
}

func invalidChatMessage(index int, message string) *payloadError {
	return &payloadError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_request",
		Message: message,
		Param:   fmt.Sprintf("messages[%d]", index),
	}
}
