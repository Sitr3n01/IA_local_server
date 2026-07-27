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
)

const maxBufferedResponseBytes = 64 << 20
const maxSSELineBytes = 16 << 20

type namespaceTarget struct {
	Namespace string
	Name      string
}

type namespaceRewrite struct {
	byFlat map[string]namespaceTarget
	byPair map[string]string
}

func normalizeNamespacedTools(path string, body []byte) ([]byte, *namespaceRewrite, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, errors.New("invalid JSON object")
	}
	rawTools, present := payload["tools"]
	if !present || string(rawTools) == "null" {
		return body, nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return nil, nil, invalidTools("tools", "tools must be an array of objects")
	}

	rewrite := &namespaceRewrite{byFlat: map[string]namespaceTarget{}, byPair: map[string]string{}}
	seenNames := make(map[string]struct{})
	normalized := make([]json.RawMessage, 0, len(tools))
	for index, rawTool := range tools {
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return nil, nil, invalidTools(fmt.Sprintf("tools[%d]", index), "every tool must be an object")
		}
		toolType, err := requiredString(tool, "type")
		if err != nil {
			return nil, nil, invalidTools(fmt.Sprintf("tools[%d].type", index), "every tool must declare a type")
		}
		switch toolType {
		case "function":
			name, nameParam, err := functionToolName(path, tool, index)
			if err != nil {
				return nil, nil, invalidTools(nameParam, "every function tool must declare a name")
			}
			if _, duplicate := seenNames[name]; duplicate {
				return nil, nil, invalidTools(nameParam, "tool names must be unique after namespace expansion")
			}
			seenNames[name] = struct{}{}
			normalized = append(normalized, rawTool)
		case "namespace":
			if path != "/v1/responses" {
				return nil, nil, unsupportedTool(fmt.Sprintf("tools[%d].type", index), toolType)
			}
			namespace, err := requiredString(tool, "name")
			if err != nil {
				return nil, nil, invalidTools(fmt.Sprintf("tools[%d].name", index), "every namespace must declare a name")
			}
			var nested []json.RawMessage
			if err := json.Unmarshal(tool["tools"], &nested); err != nil {
				return nil, nil, invalidTools(fmt.Sprintf("tools[%d].tools", index), "namespace tools must be an array")
			}
			for nestedIndex, rawNested := range nested {
				var nestedTool map[string]json.RawMessage
				if err := json.Unmarshal(rawNested, &nestedTool); err != nil {
					return nil, nil, invalidTools(fmt.Sprintf("tools[%d].tools[%d]", index, nestedIndex), "namespace entries must be objects")
				}
				nestedType, err := requiredString(nestedTool, "type")
				if err != nil || nestedType != "function" {
					return nil, nil, unsupportedTool(fmt.Sprintf("tools[%d].tools[%d].type", index, nestedIndex), nestedType)
				}
				name, err := requiredString(nestedTool, "name")
				if err != nil {
					return nil, nil, invalidTools(fmt.Sprintf("tools[%d].tools[%d].name", index, nestedIndex), "namespace function must declare a name")
				}
				flatName := flattenNamespaceName(namespace, name)
				if len(flatName) > 128 {
					return nil, nil, invalidTools(fmt.Sprintf("tools[%d].tools[%d].name", index, nestedIndex), "expanded tool name exceeds 128 characters")
				}
				if _, duplicate := seenNames[flatName]; duplicate {
					return nil, nil, invalidTools(fmt.Sprintf("tools[%d].tools[%d].name", index, nestedIndex), "tool names must be unique after namespace expansion")
				}
				seenNames[flatName] = struct{}{}
				nestedTool["name"], _ = json.Marshal(flatName)
				delete(nestedTool, "defer_loading")
				encoded, err := json.Marshal(nestedTool)
				if err != nil {
					return nil, nil, invalidTools("tools", "namespace tool could not be normalized")
				}
				normalized = append(normalized, encoded)
				target := namespaceTarget{Namespace: namespace, Name: name}
				rewrite.byFlat[flatName] = target
				rewrite.byPair[pairKey(namespace, name)] = flatName
			}
		default:
			return nil, nil, unsupportedTool(fmt.Sprintf("tools[%d].type", index), toolType)
		}
	}

	if len(rewrite.byFlat) == 0 {
		return body, nil, nil
	}
	encodedTools, err := json.Marshal(normalized)
	if err != nil {
		return nil, nil, invalidTools("tools", "tools could not be normalized")
	}
	payload["tools"] = encodedTools
	for _, field := range []string{"input", "tool_choice"} {
		if raw, ok := payload[field]; ok {
			rewritten, err := rewrite.rewriteRequestJSON(raw)
			if err != nil {
				return nil, nil, &payloadError{Status: http.StatusBadRequest, Code: "invalid_request", Message: field + " could not be normalized", Param: field}
			}
			payload[field] = rewritten
		}
	}
	normalizedBody, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, errors.New("invalid JSON object")
	}
	return normalizedBody, rewrite, nil
}

func functionToolName(path string, tool map[string]json.RawMessage, index int) (string, string, error) {
	if path == "/v1/chat/completions" {
		param := fmt.Sprintf("tools[%d].function.name", index)
		var function map[string]json.RawMessage
		if err := json.Unmarshal(tool["function"], &function); err != nil || function == nil {
			return "", param, errors.New("missing function object")
		}
		name, err := requiredString(function, "name")
		return name, param, err
	}

	param := fmt.Sprintf("tools[%d].name", index)
	name, err := requiredString(tool, "name")
	return name, param, err
}

func requiredString(object map[string]json.RawMessage, field string) (string, error) {
	var value string
	if err := json.Unmarshal(object[field], &value); err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("missing string")
	}
	return value, nil
}

func invalidTools(param, message string) *payloadError {
	return &payloadError{Status: http.StatusBadRequest, Code: "invalid_tools", Message: message, Param: param}
}

func unsupportedTool(param, toolType string) *payloadError {
	if strings.TrimSpace(toolType) == "" {
		toolType = "unknown"
	}
	return &payloadError{
		Status:  http.StatusBadRequest,
		Code:    "unsupported_feature",
		Message: fmt.Sprintf("tool type %q is not supported by the local runtime", toolType),
		Param:   param,
	}
}

func flattenNamespaceName(namespace, name string) string {
	if strings.HasSuffix(namespace, "__") {
		return namespace + name
	}
	return namespace + "__" + name
}

func pairKey(namespace, name string) string { return namespace + "\x00" + name }

func (rewrite *namespaceRewrite) rewriteRequestJSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	rewriteRequestValue(value, rewrite.byPair)
	return json.Marshal(value)
}

func rewriteRequestValue(value any, byPair map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		namespace, hasNamespace := typed["namespace"].(string)
		name, hasName := typed["name"].(string)
		if hasNamespace && hasName {
			if flatName, ok := byPair[pairKey(namespace, name)]; ok {
				typed["name"] = flatName
				delete(typed, "namespace")
			}
		}
		for _, nested := range typed {
			rewriteRequestValue(nested, byPair)
		}
	case []any:
		for _, nested := range typed {
			rewriteRequestValue(nested, byPair)
		}
	}
}

func (rewrite *namespaceRewrite) rewriteResponseJSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	rewriteResponseValue(value, rewrite.byFlat)
	return json.Marshal(value)
}

func rewriteResponseValue(value any, byFlat map[string]namespaceTarget) {
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "function_call" {
			if flatName, ok := typed["name"].(string); ok {
				if target, found := byFlat[flatName]; found {
					typed["name"] = target.Name
					typed["namespace"] = target.Namespace
				}
			}
		}
		for _, nested := range typed {
			rewriteResponseValue(nested, byFlat)
		}
	case []any:
		for _, nested := range typed {
			rewriteResponseValue(nested, byFlat)
		}
	}
}

func translateBufferedResponse(reader io.Reader, rewrite *namespaceRewrite) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBufferedResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBufferedResponseBytes {
		return nil, errors.New("upstream response exceeds the buffered response limit")
	}
	translated, err := rewrite.rewriteResponseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("translate upstream response: %w", err)
	}
	return translated, nil
}

func copyTranslatedSSE(w http.ResponseWriter, reader io.Reader, rewrite *namespaceRewrite) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32<<10), maxSSELineBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		translated := line
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) > 0 && !bytes.Equal(data, []byte("[DONE]")) {
				rewritten, rewriteErr := rewrite.rewriteResponseJSON(data)
				if rewriteErr != nil {
					return fmt.Errorf("translate upstream SSE event: %w", rewriteErr)
				}
				translated = append([]byte("data: "), rewritten...)
			}
		}
		translated = append(translated, '\n')
		if _, writeErr := w.Write(translated); writeErr != nil {
			return writeErr
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream SSE line (limit %d bytes): %w", maxSSELineBytes, err)
	}
	return nil
}
