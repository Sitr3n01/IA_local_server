package edge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleAnthropicMessages is the native Anthropic compatibility surface used
// by gateway clients. It translates at the edge boundary and sends the
// canonical OpenAI-compatible request directly to llama-swap; it never posts
// back to this process' /v1/chat/completions endpoint.
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		if !isJSONContentType(contentType) {
			s.writeAnthropicError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "Content-Type must be application/json")
			return
		}
	}
	releaseBody, err := s.reserveBody(r.Context())
	if err != nil {
		s.writeAnthropicGateError(w, err)
		return
	}
	defer releaseBody()
	// Read and validate the bounded request before it waits for an inference
	// slot. Claude can cancel queued requests after another branch succeeds; if
	// the body is read only after the wait, that normal cancellation becomes a
	// misleading 400 "invalid request body" response.
	body, err := decodeRequestBody(r, s.cfg.MaxWireBytes, s.cfg.MaxDecodedBytes, s.cfg.MaxRatio)
	if err != nil {
		s.metrics.invalidRequests.Add(1)
		s.writeAnthropicBodyError(w, err)
		return
	}
	converted, err := decodeAnthropicRequest(body)
	if err != nil {
		s.metrics.invalidRequests.Add(1)
		var requestErr *anthropicRequestError
		if errors.As(err, &requestErr) {
			s.writeAnthropicError(w, requestErr.status, requestErr.kind, requestErr.message)
			return
		}
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object")
		return
	}
	// Model selection and capability checks need only the static manifest and
	// the decoded request, so they run before admission, as on the OpenAI
	// routes. With a single inference slot, a request this edge will refuse
	// must not wait behind a running inference or hold the slot to be refused.
	realModelID, ok := claudeRealModelID(s.cfg.Models, converted.model)
	if !ok {
		s.metrics.invalidRequests.Add(1)
		s.writeAnthropicError(w, http.StatusNotFound, "not_found_error", "requested model is not available")
		return
	}
	if err := converted.useInternalModel(realModelID); err != nil {
		s.metrics.invalidRequests.Add(1)
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request model could not be mapped")
		return
	}
	model, ok := s.modelByID(converted.model)
	if !ok {
		s.metrics.invalidRequests.Add(1)
		s.writeAnthropicError(w, http.StatusNotFound, "not_found_error", "requested model is not available")
		return
	}
	if !model.Capabilities.ChatCompletions {
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "selected model does not support chat completions")
		return
	}
	if converted.stream && !model.Capabilities.Streaming {
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "selected model does not support streaming")
		return
	}
	toolsOmitted := false
	if !model.Capabilities.FunctionCalling {
		if converted.requiresTools {
			s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "selected model does not support the required tool choice")
			return
		}
		if converted.hasToolHistory {
			s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "selected model cannot continue a tool-use conversation")
			return
		}
		if converted.hasTools {
			if err := converted.withoutTools(); err != nil {
				s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request tools could not be omitted")
				return
			}
			toolsOmitted = true
		}
	}
	release, err := s.gate.acquire(r.Context())
	if err != nil {
		s.writeAnthropicGateError(w, err)
		return
	}
	admitted := time.Now()
	defer func() {
		s.metrics.inferenceDuration.observe(time.Since(admitted))
		release()
	}()
	// The selection events describe admitted requests only. A request that
	// times out or is canceled in the queue never used the model, so it logs
	// neither event even though its checks ran before admission.
	if toolsOmitted {
		s.logEvent("claude.tools.omitted", map[string]any{"model": model.ID})
	}
	s.logEvent("claude.model.selected", map[string]any{
		"model":               model.ID,
		"external_alias_used": converted.clientModel != model.ID,
	})
	capacity, err := s.prepareInferenceCapacity(r.Context(), model)
	if err != nil {
		s.metrics.upstreamFailures.Add(1)
		s.writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "local model unload could not be verified")
		return
	}
	if !capacity.Available {
		// 400, not 503. Claude Desktop's agent retries a 5xx ten times and
		// shows only "Solicitação falhou"; memory does not free itself in
		// seconds, and an invalid_request_error shows the explanation at once.
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", s.refusalText(capacity))
		return
	}
	track := s.inference.begin(model, "/v1/messages", converted.stream, !capacity.ModelRunning)
	r = r.WithContext(withInferenceTrack(r.Context(), track))
	defer func() { track.end(responseStatus(w), r.Context().Err() != nil) }()
	if err := s.proxyAnthropic(w, r, converted); err != nil {
		s.metrics.upstreamFailures.Add(1)
		if !headersWritten(w) {
			if r.Context().Err() != nil {
				s.writeAnthropicError(w, 499, "api_error", "request was canceled")
				return
			}
			s.writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "local inference runtime is unavailable")
		}
	}
}

func isJSONContentType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return mediaType == "application/json"
}

type anthropicRequestError struct {
	status  int
	kind    string
	message string
}

func (e *anthropicRequestError) Error() string { return e.message }

func anthropicInvalid(message string) error {
	return &anthropicRequestError{status: http.StatusBadRequest, kind: "invalid_request_error", message: message}
}

type convertedAnthropicRequest struct {
	model          string
	clientModel    string
	stream         bool
	hasTools       bool
	hasToolHistory bool
	requiresTools  bool
	body           []byte
}

func (c *convertedAnthropicRequest) useInternalModel(model string) error {
	var payload map[string]any
	if err := json.Unmarshal(c.body, &payload); err != nil {
		return err
	}
	payload["model"] = model
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.model = model
	c.body = body
	return nil
}

func (c *convertedAnthropicRequest) withoutTools() error {
	var payload map[string]any
	if err := json.Unmarshal(c.body, &payload); err != nil {
		return err
	}
	delete(payload, "tools")
	delete(payload, "tool_choice")
	delete(payload, "parallel_tool_calls")
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.hasTools = false
	c.body = body
	return nil
}

type anthropicWireRequest struct {
	Model         string             `json:"model"`
	MaxTokens     *int               `json:"max_tokens"`
	System        json.RawMessage    `json:"system"`
	Messages      []anthropicMessage `json:"messages"`
	Tools         []anthropicTool    `json:"tools"`
	ToolChoice    json.RawMessage    `json:"tool_choice"`
	Stream        bool               `json:"stream"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func decodeAnthropicRequest(body []byte) (convertedAnthropicRequest, error) {
	var raw anthropicWireRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Claude Desktop's Cowork transport adds optional beta envelope fields
	// (for example metadata, thinking, and output_config). Decode
	// the stable subset we implement and rebuild the canonical upstream body
	// from that subset below, so extensions are tolerated but never forwarded.
	if err := decoder.Decode(&raw); err != nil {
		return convertedAnthropicRequest{}, anthropicInvalid("malformed Anthropic Messages request")
	}
	var surplus any
	if err := decoder.Decode(&surplus); err != io.EOF {
		return convertedAnthropicRequest{}, anthropicInvalid("request must contain one JSON object")
	}
	raw.Model = strings.TrimSpace(raw.Model)
	if raw.Model == "" {
		return convertedAnthropicRequest{}, anthropicInvalid("model is required")
	}
	if raw.MaxTokens == nil || *raw.MaxTokens <= 0 {
		return convertedAnthropicRequest{}, anthropicInvalid("max_tokens must be a positive integer")
	}
	if len(raw.Messages) == 0 {
		return convertedAnthropicRequest{}, anthropicInvalid("messages must contain at least one message")
	}
	if raw.Temperature != nil && (*raw.Temperature < 0 || *raw.Temperature > 1) {
		return convertedAnthropicRequest{}, anthropicInvalid("temperature must be between 0 and 1")
	}
	if raw.TopP != nil && (*raw.TopP < 0 || *raw.TopP > 1) {
		return convertedAnthropicRequest{}, anthropicInvalid("top_p must be between 0 and 1")
	}

	messages := make([]map[string]any, 0, len(raw.Messages)+1)
	hasToolHistory := false
	if len(raw.System) != 0 && string(raw.System) != "null" {
		system, err := anthropicText(raw.System, "system")
		if err != nil {
			return convertedAnthropicRequest{}, err
		}
		if system != "" {
			messages = append(messages, map[string]any{"role": "system", "content": system})
		}
	}
	for index, message := range raw.Messages {
		converted, err := convertAnthropicMessage(message, index)
		if err != nil {
			return convertedAnthropicRequest{}, err
		}
		messages = append(messages, converted...)
		for _, entry := range converted {
			if entry["role"] == "tool" || entry["tool_calls"] != nil {
				hasToolHistory = true
			}
		}
	}

	tools := make([]map[string]any, 0, len(raw.Tools))
	for index, tool := range raw.Tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(tool.InputSchema) == 0 {
			return convertedAnthropicRequest{}, anthropicInvalid(fmt.Sprintf("tools[%d] must include name and input_schema", index))
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema == nil {
			return convertedAnthropicRequest{}, anthropicInvalid(fmt.Sprintf("tools[%d].input_schema must be an object", index))
		}
		function := map[string]any{"name": name, "parameters": schema}
		if description := strings.TrimSpace(tool.Description); description != "" {
			function["description"] = description
		}
		tools = append(tools, map[string]any{"type": "function", "function": function})
	}
	payload := map[string]any{
		"model":      raw.Model,
		"max_tokens": *raw.MaxTokens,
		"messages":   messages,
		"stream":     raw.Stream,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	requiresTools, err := convertAnthropicToolChoice(raw.ToolChoice, raw.Tools, payload)
	if err != nil {
		return convertedAnthropicRequest{}, err
	}
	if raw.Temperature != nil {
		payload["temperature"] = *raw.Temperature
	}
	if raw.TopP != nil {
		payload["top_p"] = *raw.TopP
	}
	if len(raw.StopSequences) > 0 {
		payload["stop"] = raw.StopSequences
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return convertedAnthropicRequest{}, errors.New("encode canonical request")
	}
	return convertedAnthropicRequest{model: raw.Model, clientModel: raw.Model, stream: raw.Stream, hasTools: len(tools) > 0, hasToolHistory: hasToolHistory, requiresTools: requiresTools, body: encoded}, nil
}

func convertAnthropicToolChoice(raw json.RawMessage, tools []anthropicTool, payload map[string]any) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var choice struct {
		Type            string `json:"type"`
		Name            string `json:"name"`
		DisableParallel *bool  `json:"disable_parallel_tool_use"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&choice); err != nil {
		return false, anthropicInvalid("tool_choice must be a supported object")
	}
	required := false
	switch choice.Type {
	case "auto", "none":
		if choice.Name != "" {
			return false, anthropicInvalid("tool_choice.name requires type tool")
		}
		payload["tool_choice"] = choice.Type
	case "any":
		if len(tools) == 0 || choice.Name != "" {
			return false, anthropicInvalid("tool_choice any requires tools and no name")
		}
		payload["tool_choice"] = "required"
		required = true
	case "tool":
		return false, anthropicInvalid("named tool_choice is not qualified for the local runtime; use auto, none or any")
	default:
		return false, anthropicInvalid("unsupported tool_choice.type")
	}
	if choice.DisableParallel != nil {
		payload["parallel_tool_calls"] = !*choice.DisableParallel
	}
	return required, nil
}

func convertAnthropicMessage(message anthropicMessage, index int) ([]map[string]any, error) {
	role := strings.TrimSpace(message.Role)
	if role != "user" && role != "assistant" && role != "system" {
		return nil, anthropicInvalid(fmt.Sprintf("messages[%d].role must be user, assistant, or system", index))
	}
	if len(message.Content) == 0 {
		return nil, anthropicInvalid(fmt.Sprintf("messages[%d].content is required", index))
	}
	if role == "system" {
		if index == 0 {
			return nil, anthropicInvalid("messages[0].role cannot be system; use the top-level system field")
		}
		text, err := anthropicText(message.Content, fmt.Sprintf("messages[%d].content", index))
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"role": "system", "content": text}}, nil
	}
	if text, err := anthropicText(message.Content, fmt.Sprintf("messages[%d].content", index)); err == nil {
		return []map[string]any{{"role": role, "content": text}}, nil
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(message.Content, &blocks); err != nil || len(blocks) == 0 {
		return nil, anthropicInvalid(fmt.Sprintf("messages[%d].content must be text or content blocks", index))
	}
	textParts := make([]string, 0, len(blocks))
	toolCalls := make([]map[string]any, 0)
	toolResults := make([]map[string]any, 0)
	for blockIndex, block := range blocks {
		var kind string
		if err := json.Unmarshal(block["type"], &kind); err != nil {
			return nil, anthropicInvalid(fmt.Sprintf("messages[%d].content[%d].type is required", index, blockIndex))
		}
		switch kind {
		case "text":
			var text string
			if err := json.Unmarshal(block["text"], &text); err != nil {
				return nil, anthropicInvalid(fmt.Sprintf("messages[%d].content[%d].text is required", index, blockIndex))
			}
			textParts = append(textParts, text)
		case "tool_use":
			if role != "assistant" {
				return nil, anthropicInvalid("tool_use blocks are valid only in assistant messages")
			}
			var id, name string
			if json.Unmarshal(block["id"], &id) != nil || json.Unmarshal(block["name"], &name) != nil || strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
				return nil, anthropicInvalid("tool_use blocks require id and name")
			}
			input := block["input"]
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			var inputObject map[string]any
			if err := json.Unmarshal(input, &inputObject); err != nil || inputObject == nil {
				return nil, anthropicInvalid("tool_use input must be an object")
			}
			arguments, _ := json.Marshal(inputObject)
			toolCalls = append(toolCalls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(arguments)}})
		case "tool_result":
			if role != "user" {
				return nil, anthropicInvalid("tool_result blocks are valid only in user messages")
			}
			var id string
			if json.Unmarshal(block["tool_use_id"], &id) != nil || strings.TrimSpace(id) == "" {
				return nil, anthropicInvalid("tool_result blocks require tool_use_id")
			}
			content, err := anthropicText(block["content"], "tool_result.content")
			if err != nil {
				return nil, err
			}
			toolResults = append(toolResults, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
		case "thinking", "redacted_thinking":
			return nil, anthropicInvalid("thinking blocks are not supported by the selected local runtime")
		default:
			return nil, anthropicInvalid(fmt.Sprintf("unsupported content block type %q", kind))
		}
	}
	result := make([]map[string]any, 0, 1+len(toolResults))
	if role == "assistant" {
		entry := map[string]any{"role": "assistant"}
		if len(textParts) > 0 {
			entry["content"] = strings.Join(textParts, "")
		} else {
			entry["content"] = ""
		}
		if len(toolCalls) > 0 {
			entry["tool_calls"] = toolCalls
		}
		result = append(result, entry)
	} else if len(textParts) > 0 {
		result = append(result, map[string]any{"role": "user", "content": strings.Join(textParts, "")})
	}
	result = append(result, toolResults...)
	if len(result) == 0 {
		return nil, anthropicInvalid(fmt.Sprintf("messages[%d] contains no supported content", index))
	}
	return result, nil
}

func anthropicText(raw json.RawMessage, label string) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return "", anthropicInvalid(label + " must be text or text content blocks")
	}
	parts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		var kind, part string
		if json.Unmarshal(block["type"], &kind) != nil || kind != "text" || json.Unmarshal(block["text"], &part) != nil {
			return "", anthropicInvalid(fmt.Sprintf("%s[%d] must be a text block", label, index))
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ""), nil
}

func (s *Server) proxyAnthropic(w http.ResponseWriter, incoming *http.Request, converted convertedAnthropicRequest) error {
	target := *s.upstream
	target.Path = "/v1/chat/completions"
	request, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, target.String(), bytes.NewReader(converted.body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if converted.stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("X-Request-Id", w.Header().Get("X-Request-Id"))
	request.Header.Set("User-Agent", "cia-edge/"+s.cfg.Version)
	if s.cfg.RouterToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.cfg.RouterToken)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.writeAnthropicError(w, response.StatusCode, "api_error", "local inference runtime rejected the request")
		return nil
	}
	upstreamBody := observeInferenceBody(incoming.Context(), response)
	if converted.stream {
		return writeAnthropicStream(w, upstreamBody, converted.clientModel, w.Header().Get("X-Request-Id"))
	}
	return writeAnthropicResponse(w, upstreamBody, converted.clientModel, w.Header().Get("X-Request-Id"), s.cfg.MaxDecodedBytes)
}

func writeAnthropicResponse(w http.ResponseWriter, body io.Reader, model, requestID string, maximum int64) error {
	raw, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > maximum {
		return errors.New("upstream response exceeds configured limit")
	}
	message, usage, stopReason, err := anthropicResponseFromChat(raw, model, requestID)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"id":            message.id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       message.content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	})
}

type anthropicMessageResponse struct {
	id      string
	content []map[string]any
}

func anthropicResponseFromChat(raw []byte, model, requestID string) (anthropicMessageResponse, map[string]int, string, error) {
	var response struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage   inferenceUsage   `json:"usage"`
		Timings inferenceTimings `json:"timings"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || len(response.Choices) == 0 {
		return anthropicMessageResponse{}, nil, "", errors.New("upstream response is not a valid chat completion")
	}
	choice := response.Choices[0]
	content := make([]map[string]any, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": choice.Message.Content})
	}
	for _, call := range choice.Message.ToolCalls {
		var input any = map[string]any{}
		if call.Function.Arguments != "" && json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
			return anthropicMessageResponse{}, nil, "", errors.New("upstream tool arguments are not JSON")
		}
		content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	id := response.ID
	if id == "" {
		id = "msg_" + requestID
	}
	return anthropicMessageResponse{id: id, content: content}, anthropicTokenUsage(response.Usage, response.Timings), anthropicStopReason(choice.FinishReason), nil
}

func anthropicTokenUsage(usage inferenceUsage, timings inferenceTimings) map[string]int {
	counts := map[string]int{"input_tokens": 0, "output_tokens": 0}
	input := firstCount(usage.PromptTokens, usage.InputTokens, sumCounts(timings.PromptN, timings.CacheN), timings.PromptN)
	output := firstCount(usage.CompletionTokens, usage.OutputTokens, timings.PredictedN)
	if input != nil {
		counts["input_tokens"] = *input
	}
	if output != nil {
		counts["output_tokens"] = *output
	}
	return counts
}

func anthropicStopReason(value string) string {
	switch value {
	case "tool_calls", "function_call":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func writeAnthropicStream(w http.ResponseWriter, body io.Reader, model, requestID string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	var state anthropicStreamState
	if err := state.write(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_" + requestID, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 32<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return state.finish(w)
		}
		if err := state.consume(w, []byte(data)); err != nil {
			return state.fail(w, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return state.fail(w, err)
	}
	return state.finish(w)
}

type anthropicStreamState struct {
	textStarted bool
	textIndex   int
	toolBlocks  map[int]int
	openBlocks  []int
	stopReason  string
	usage       inferenceUsage
	timings     inferenceTimings
}

func (s *anthropicStreamState) consume(w http.ResponseWriter, data []byte) error {
	var event struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage   *inferenceUsage   `json:"usage"`
		Timings *inferenceTimings `json:"timings"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return errors.New("upstream stream emitted invalid JSON")
	}
	if rawHasText(event.Error) {
		return errors.New("upstream stream reported an error")
	}
	if event.Usage != nil {
		if event.Usage.PromptTokens != nil {
			s.usage.PromptTokens = event.Usage.PromptTokens
		}
		if event.Usage.InputTokens != nil {
			s.usage.InputTokens = event.Usage.InputTokens
		}
		if event.Usage.CompletionTokens != nil {
			s.usage.CompletionTokens = event.Usage.CompletionTokens
		}
		if event.Usage.OutputTokens != nil {
			s.usage.OutputTokens = event.Usage.OutputTokens
		}
	}
	if event.Timings != nil {
		if event.Timings.PromptN != nil {
			s.timings.PromptN = event.Timings.PromptN
		}
		if event.Timings.CacheN != nil {
			s.timings.CacheN = event.Timings.CacheN
		}
		if event.Timings.PredictedN != nil {
			s.timings.PredictedN = event.Timings.PredictedN
		}
	}
	if len(event.Choices) == 0 {
		return nil
	}
	choice := event.Choices[0]
	if choice.FinishReason != nil {
		s.stopReason = anthropicStopReason(*choice.FinishReason)
	}
	if content := choice.Delta.Content; content != "" {
		if !s.textStarted {
			s.textIndex = len(s.openBlocks)
			if err := s.write(w, "content_block_start", map[string]any{"type": "content_block_start", "index": s.textIndex, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
				return err
			}
			s.textStarted = true
			s.openBlocks = append(s.openBlocks, s.textIndex)
		}
		if err := s.write(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.textIndex, "delta": map[string]any{"type": "text_delta", "text": content}}); err != nil {
			return err
		}
	}
	for _, call := range choice.Delta.ToolCalls {
		if s.toolBlocks == nil {
			s.toolBlocks = make(map[int]int)
		}
		block, found := s.toolBlocks[call.Index]
		if !found {
			block = len(s.openBlocks)
			s.toolBlocks[call.Index] = block
			s.openBlocks = append(s.openBlocks, block)
			if err := s.write(w, "content_block_start", map[string]any{"type": "content_block_start", "index": block, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": map[string]any{}}}); err != nil {
				return err
			}
		}
		if call.Function.Arguments != "" {
			if err := s.write(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": block, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *anthropicStreamState) finish(w http.ResponseWriter) error {
	if s.stopReason == "" {
		return s.fail(w, errors.New("upstream stream ended before completion"))
	}
	for _, index := range s.openBlocks {
		if err := s.write(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index}); err != nil {
			return err
		}
	}
	if err := s.write(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil}, "usage": anthropicTokenUsage(s.usage, s.timings)}); err != nil {
		return err
	}
	return s.write(w, "message_stop", map[string]any{"type": "message_stop"})
}

func (s *anthropicStreamState) fail(w http.ResponseWriter, cause error) error {
	if err := s.write(w, "error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "local inference stream did not complete"}}); err != nil {
		return err
	}
	return cause
}

func (s *anthropicStreamState) write(w http.ResponseWriter, name string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (s *Server) writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
}

func (s *Server) writeAnthropicGateError(w http.ResponseWriter, err error) {
	status := http.StatusTooManyRequests
	message := "local inference queue is full"
	switch {
	case errors.Is(err, errQueueTimeout):
		message = "local inference queue wait timed out"
	case errors.Is(err, errControlBusy), errors.Is(err, errDraining):
		status = http.StatusServiceUnavailable
		message = "local inference is temporarily unavailable"
	}
	s.writeAnthropicError(w, status, "overloaded_error", message)
}

func (s *Server) writeAnthropicBodyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWireTooLarge), errors.Is(err, errDecodedTooLarge), errors.Is(err, errCompressionRatio):
		s.writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds configured limits")
	case errors.Is(err, errUnsupportedEncoding):
		s.writeAnthropicError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported content encoding")
	default:
		s.writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid request body")
	}
}
