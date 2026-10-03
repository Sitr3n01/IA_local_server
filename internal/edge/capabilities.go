package edge

import (
	"encoding/json"
	"net/http"
)

// validateCapabilities applies the manifest's qualifications before a request
// can reach the runtime. Empty tool lists and plain text require no extra cap.
func validateCapabilities(path string, body []byte, caps Capabilities) *payloadError {
	refuse := func(field, feature string) *payloadError {
		return &payloadError{Status: http.StatusBadRequest, Code: "unsupported_feature", Param: field,
			Message: "selected model does not support " + feature}
	}
	if path == "/v1/responses" && !caps.Responses {
		return refuse("model", "Responses")
	}
	if path == "/v1/chat/completions" && !caps.ChatCompletions {
		return refuse("model", "chat completions")
	}
	if requestStreams(body) && !caps.Streaming {
		return refuse("stream", "streaming")
	}
	var request struct {
		Tools          []json.RawMessage `json:"tools"`
		Functions      []json.RawMessage `json:"functions"`
		ToolChoice     json.RawMessage   `json:"tool_choice"`
		FunctionCall   json.RawMessage   `json:"function_call"`
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Text struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		} `json:"text"`
		Messages []struct {
			Role         string            `json:"role"`
			ToolCalls    []json.RawMessage `json:"tool_calls"`
			FunctionCall json.RawMessage   `json:"function_call"`
		} `json:"messages"`
		Input           json.RawMessage `json:"input"`
		Reasoning       json.RawMessage `json:"reasoning"`
		ReasoningEffort string          `json:"reasoning_effort"`
	}
	if json.Unmarshal(body, &request) != nil {
		return &payloadError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "invalid capability fields in request"}
	}
	if caps.FunctionCalling && rawHasText(request.ToolChoice) {
		var choice string
		if json.Unmarshal(request.ToolChoice, &choice) != nil {
			return &payloadError{Status: http.StatusBadRequest, Code: "unsupported_feature", Param: "tool_choice",
				Message: "named tool_choice is not qualified for the local runtime; use auto, none or required"}
		}
		if choice != "auto" && choice != "none" && choice != "required" {
			return &payloadError{Status: http.StatusBadRequest, Code: "invalid_request", Param: "tool_choice",
				Message: "tool_choice must be auto, none or required"}
		}
	}
	if caps.FunctionCalling && rawHasText(request.FunctionCall) {
		var choice string
		if json.Unmarshal(request.FunctionCall, &choice) != nil || (choice != "auto" && choice != "none") {
			return &payloadError{Status: http.StatusBadRequest, Code: "unsupported_feature", Param: "function_call",
				Message: "named function_call is not qualified for the local runtime; use auto or none"}
		}
	}
	if !caps.FunctionCalling {
		if len(request.Tools) > 0 || len(request.Functions) > 0 || requiresToolChoice(request.ToolChoice) || requiresToolChoice(request.FunctionCall) {
			return refuse("tools", "function calling")
		}
		for _, message := range request.Messages {
			if message.Role == "tool" || message.Role == "function" || len(message.ToolCalls) > 0 || rawHasText(message.FunctionCall) {
				return refuse("messages", "tool-use history")
			}
		}
		var input []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(request.Input, &input) == nil {
			for _, item := range input {
				if item.Type == "function_call" || item.Type == "function_call_output" {
					return refuse("input", "tool-use history")
				}
			}
		}
	}
	if !caps.StructuredOutput {
		if kind := request.ResponseFormat.Type; kind != "" && kind != "text" {
			return refuse("response_format", "structured output")
		}
		if kind := request.Text.Format.Type; kind != "" && kind != "text" {
			return refuse("text.format", "structured output")
		}
	}
	if !caps.Reasoning {
		if effort := request.ReasoningEffort; effort != "" && effort != "none" {
			return refuse("reasoning_effort", "reasoning")
		}
		if rawHasText(request.Reasoning) {
			var reasoning struct {
				Effort string `json:"effort"`
			}
			if json.Unmarshal(request.Reasoning, &reasoning) != nil || reasoning.Effort != "none" {
				return refuse("reasoning", "reasoning")
			}
		}
	}
	return nil
}

func requiresToolChoice(raw json.RawMessage) bool {
	if !rawHasText(raw) {
		return false
	}
	var choice string
	return json.Unmarshal(raw, &choice) != nil || (choice != "auto" && choice != "none")
}
