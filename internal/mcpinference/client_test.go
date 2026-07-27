package mcpinference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegateUsesPinnedModelCredentialAndStatelessTextContract(t *testing.T) {
	t.Parallel()

	const testInferenceToken = "test-inference-token-0000000000000000"

	var tokenCalls atomic.Int32
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testInferenceToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "cia-mcp-inference/test-version" {
			t.Errorf("User-Agent = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "local-coding" || body["stream"] != false || body["max_tokens"] != float64(321) || body["temperature"] != defaultTemperature {
			t.Errorf("unexpected request controls: %#v", body)
		}
		if _, exists := body["tools"]; exists {
			t.Error("delegated request must not expose tools")
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages = %#v", body["messages"])
		}
		user := messages[1].(map[string]any)
		content := user["content"].(string)
		if !strings.Contains(content, "reference text") || !strings.Contains(content, "solve this") {
			t.Errorf("user content did not contain supplied text")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"model":"private-upstream-name",
			"choices":[{"message":{"role":"assistant","content":"local answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`)
	}))
	defer edge.Close()

	client := newTestClient(t, edge.URL, Config{
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			tokenCalls.Add(1)
			return testInferenceToken, nil
		}),
	})
	output, err := client.Delegate(context.Background(), DelegateInput{
		Prompt:          "solve this",
		Context:         "reference text",
		MaxOutputTokens: 321,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token provider calls = %d", tokenCalls.Load())
	}
	if output.Model != "local-coding" || output.Text != "local answer" || output.FinishReason != "stop" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if output.Usage.PromptTokens != 10 || output.Usage.CompletionTokens != 3 || output.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", output.Usage)
	}
}

func TestDelegateRejectsInvalidInputBeforeCredentialOrNetwork(t *testing.T) {
	t.Parallel()

	var tokenCalls atomic.Int32
	client := newTestClient(t, "http://127.0.0.1:1", Config{
		MaxPromptBytes:   8,
		MaxContextBytes:  10,
		MaxCombinedBytes: 12,
		MaxOutputTokens:  20,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			tokenCalls.Add(1)
			return "test-inference-token-0000000000000000", nil
		}),
	})
	tests := []DelegateInput{
		{},
		{Prompt: "         "},
		{Prompt: "123456789"},
		{Prompt: "task", Context: "12345678901"},
		{Prompt: "12345678", Context: "12345"},
		{Prompt: "task", MaxOutputTokens: -1},
		{Prompt: "task", MaxOutputTokens: 21},
	}
	for _, input := range tests {
		if _, err := client.Delegate(context.Background(), input); err == nil {
			t.Fatalf("Delegate(%+v) succeeded", input)
		}
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("token provider called %d times for rejected input", tokenCalls.Load())
	}
}

func TestDelegateSanitizesProviderAndCredentialErrors(t *testing.T) {
	t.Parallel()

	const sensitive = "SENSITIVE_PROMPT_OR_TOKEN"
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "safe-request-1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"capacity_full","message":%q}}`, sensitive)
	}))
	defer edge.Close()

	client := newTestClient(t, edge.URL, Config{})
	_, err := client.Delegate(context.Background(), DelegateInput{Prompt: sensitive})
	if err == nil {
		t.Fatal("Delegate succeeded")
	}
	if strings.Contains(err.Error(), sensitive) || !strings.Contains(err.Error(), "capacity_full") || !strings.Contains(err.Error(), "safe-request-1") {
		t.Fatalf("unexpected sanitized error: %q", err)
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %#v", err)
	}

	credentialClient := newTestClient(t, edge.URL, Config{
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "", errors.New(sensitive)
		}),
	})
	_, err = credentialClient.Delegate(context.Background(), DelegateInput{Prompt: "safe"})
	if err == nil || strings.Contains(err.Error(), sensitive) {
		t.Fatalf("credential error was not sanitized: %v", err)
	}
}

func TestDelegatePropagatesCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	edge := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer edge.Close()
	defer close(release)

	client := newTestClient(t, edge.URL, Config{Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Delegate(ctx, DelegateInput{Prompt: "cancel me"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "local AI delegation was canceled" {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Delegate did not propagate cancellation")
	}
}

func TestDelegatePropagatesCancellationWhileReadingResponse(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer edge.Close()
	defer close(release)

	client := newTestClient(t, edge.URL, Config{Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Delegate(ctx, DelegateInput{Prompt: "cancel while reading"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "local AI delegation was canceled" {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Delegate did not propagate response-body cancellation")
	}
}

func TestDelegateAppliesBoundedTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	edge := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer edge.Close()
	defer close(release)

	client := newTestClient(t, edge.URL, Config{Timeout: 25 * time.Millisecond})
	_, err := client.Delegate(context.Background(), DelegateInput{Prompt: "time out"})
	if err == nil || err.Error() != "local AI delegation timed out" {
		t.Fatalf("error = %v", err)
	}
}

func TestDelegateRejectsToolCallsAndInconsistentUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "tool call", body: `{"choices":[{"message":{"content":"x","tool_calls":[{}]},"finish_reason":"tool_calls"}],"usage":{}}`},
		{name: "usage", body: `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":5}}`},
		{name: "unsafe finish reason", body: `{"choices":[{"message":{"content":"x"},"finish_reason":"stop\nsecret"}],"usage":{}}`},
		{name: "empty text", body: `{"choices":[{"message":{"content":"   "},"finish_reason":"stop"}],"usage":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer edge.Close()
			client := newTestClient(t, edge.URL, Config{})
			if _, err := client.Delegate(context.Background(), DelegateInput{Prompt: "task"}); err == nil {
				t.Fatal("Delegate succeeded")
			}
		})
	}
}

func newTestClient(t *testing.T, dataURL string, override Config) *Client {
	t.Helper()
	cfg := Config{
		DataURL:       dataURL,
		Model:         "local-coding",
		Timeout:       time.Second,
		TokenProvider: fixedTokenProvider(),
	}
	if override.Timeout != 0 {
		cfg.Timeout = override.Timeout
	}
	if override.MaxOutputTokens != 0 {
		cfg.MaxOutputTokens = override.MaxOutputTokens
	}
	if override.Temperature != 0 {
		cfg.Temperature = override.Temperature
	}
	if override.MaxPromptBytes != 0 {
		cfg.MaxPromptBytes = override.MaxPromptBytes
	}
	if override.MaxContextBytes != 0 {
		cfg.MaxContextBytes = override.MaxContextBytes
	}
	if override.MaxCombinedBytes != 0 {
		cfg.MaxCombinedBytes = override.MaxCombinedBytes
	}
	if override.HTTPClient != nil {
		cfg.HTTPClient = override.HTTPClient
	}
	if override.TokenProvider != nil {
		cfg.TokenProvider = override.TokenProvider
	}
	client, err := NewClient(cfg, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	return client
}
