package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// normalizeResponsesAuthorityMessages adapts the authority-message shape used
// by Codex to the narrower Responses-to-Chat conversion implemented by
// llama.cpp. The model's template accepts one system message at the beginning;
// llama.cpp otherwise turns top-level instructions plus an initial
// system/developer input item into multiple system messages.
//
// Only the initial contiguous authority block is merged. Moving a later
// authority message ahead of conversation history would change its semantics,
// so that shape is rejected deterministically instead.
func normalizeResponsesAuthorityMessages(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, errors.New("invalid JSON object")
	}

	rawInput, present := payload["input"]
	if !present {
		return body, nil
	}
	var input []json.RawMessage
	if err := json.Unmarshal(rawInput, &input); err != nil {
		// String input and invalid input are left to llama.cpp's normal protocol
		// validation. Only an input-item list needs this compatibility adapter.
		return body, nil
	}

	instructions, err := responsesInstructions(payload["instructions"])
	if err != nil {
		return nil, err
	}
	merged := make([]string, 0, 3)
	if instructions != "" {
		merged = append(merged, instructions)
	}

	authorityCount := 0
	for index, rawItem := range input {
		_, authority, err := responsesItemRole(rawItem)
		if err != nil {
			return nil, invalidResponsesInput(index, "input items must be objects with string roles")
		}
		if !authority {
			break
		}
		content, err := responsesAuthorityText(rawItem, index)
		if err != nil {
			return nil, err
		}
		if content != "" {
			merged = append(merged, content)
		}
		authorityCount++
	}
	for index := authorityCount; index < len(input); index++ {
		_, authority, err := responsesItemRole(input[index])
		if err != nil {
			continue
		}
		if authority {
			return nil, &payloadError{
				Status:  http.StatusBadRequest,
				Code:    "unsupported_feature",
				Message: "system and developer messages are supported only before conversation input",
				Param:   fmt.Sprintf("input[%d].role", index),
			}
		}
	}
	if authorityCount == 0 {
		return body, nil
	}

	encodedInstructions, err := json.Marshal(strings.Join(merged, "\n\n"))
	if err != nil {
		return nil, errors.New("instructions could not be normalized")
	}
	encodedInput, err := json.Marshal(input[authorityCount:])
	if err != nil {
		return nil, errors.New("input could not be normalized")
	}
	payload["instructions"] = encodedInstructions
	payload["input"] = encodedInput
	normalized, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("request could not be normalized")
	}
	return normalized, nil
}

func responsesInstructions(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var instructions string
	if err := json.Unmarshal(raw, &instructions); err != nil {
		return "", &payloadError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request",
			Message: "instructions must be a string",
			Param:   "instructions",
		}
	}
	return instructions, nil
}

func responsesItemRole(raw json.RawMessage) (string, bool, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return "", false, errors.New("invalid input item")
	}
	rawRole, present := item["role"]
	if !present {
		return "", false, nil
	}
	var role string
	if err := json.Unmarshal(rawRole, &role); err != nil {
		return "", false, errors.New("invalid input role")
	}
	return role, role == "system" || role == "developer", nil
}

func responsesAuthorityText(raw json.RawMessage, index int) (string, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return "", invalidResponsesInput(index, "authority input must be an object")
	}
	rawContent, present := item["content"]
	if !present {
		return "", invalidResponsesInput(index, "authority input must contain content")
	}
	var text string
	if json.Unmarshal(rawContent, &text) == nil {
		return text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(rawContent, &parts); err != nil {
		return "", invalidResponsesInput(index, "authority content must be text")
	}
	textParts := make([]string, 0, len(parts))
	for partIndex, part := range parts {
		partType, err := requiredString(part, "type")
		if err != nil || partType != "input_text" {
			return "", &payloadError{
				Status:  http.StatusBadRequest,
				Code:    "unsupported_feature",
				Message: "authority messages support only input_text content",
				Param:   fmt.Sprintf("input[%d].content[%d].type", index, partIndex),
			}
		}
		partText, err := requiredStringAllowEmpty(part, "text")
		if err != nil {
			return "", invalidResponsesInput(index, "authority input_text content must contain text")
		}
		textParts = append(textParts, partText)
	}
	return strings.Join(textParts, "\n\n"), nil
}

func requiredStringAllowEmpty(object map[string]json.RawMessage, field string) (string, error) {
	var value string
	if err := json.Unmarshal(object[field], &value); err != nil {
		return "", errors.New("missing string")
	}
	return value, nil
}

func invalidResponsesInput(index int, message string) *payloadError {
	return &payloadError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_request",
		Message: message,
		Param:   fmt.Sprintf("input[%d]", index),
	}
}
