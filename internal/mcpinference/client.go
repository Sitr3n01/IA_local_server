package mcpinference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const delegationSystemMessage = "You are a local language model handling one stateless delegated text task. Use only the text supplied in this request. You have no tools, files, memory, network access, or ability to perform actions. Keep any explanation brief and prioritize delivering a complete, working answer over lengthy commentary. Return the final answer as plain text."

// TokenProvider obtains the data-plane bearer credential only after a
// delegation call has passed local validation.
type TokenProvider interface {
	Token(context.Context) (string, error)
}

// TokenProviderFunc adapts a function to TokenProvider.
type TokenProviderFunc func(context.Context) (string, error)

func (f TokenProviderFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}

// DelegateInput is intentionally text-only. Model, endpoint, tools and system
// authority are not caller-controlled.
type DelegateInput struct {
	Prompt          string `json:"prompt" jsonschema:"required text task to delegate to the pinned local model"`
	Context         string `json:"context,omitempty" jsonschema:"optional reference text for this one stateless task"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty" jsonschema:"optional output token limit; zero uses the configured default ceiling"`
}

// Usage is the bounded token accounting returned by the local provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DelegateOutput is the complete structured contract exposed to the harness.
type DelegateOutput struct {
	Model        string `json:"model"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
	Usage        Usage  `json:"usage"`
}

// APIError deliberately retains only allowlisted metadata. Provider response
// bodies and messages are never surfaced because they may contain prompt data
// or runtime details.
type APIError struct {
	StatusCode int
	Status     string
	Code       string
	RequestID  string
}

func (e *APIError) Error() string {
	if e == nil {
		return "local AI provider error"
	}
	message := "local AI provider returned " + e.Status
	if e.Code != "" {
		message += " (" + e.Code + ")"
	}
	if e.RequestID != "" {
		message += " [request " + e.RequestID + "]"
	}
	return message
}

// Client performs exactly one non-streaming Chat Completions operation against
// the configured literal-loopback edge.
type Client struct {
	baseURL          string
	model            string
	timeout          time.Duration
	maxOutputTokens  int
	temperature      float64
	maxPromptBytes   int
	maxContextBytes  int
	maxCombinedBytes int
	httpClient       *http.Client
	tokenProvider    TokenProvider
	userAgent        string
}

// NewClient validates all boundaries before constructing a credential-bearing
// HTTP client. Redirects and environment proxying are disabled.
func NewClient(cfg Config, version string) (*Client, error) {
	validated, err := validateConfig(cfg, true)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	httpClient := &http.Client{Transport: transport}
	if cfg.HTTPClient != nil {
		copyClient := *cfg.HTTPClient
		httpClient = &copyClient
		httpClient.Timeout = 0
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	if version == "" {
		version = "dev"
	}
	return &Client{
		baseURL:          strings.TrimSuffix(validated.baseURL.String(), "/"),
		model:            validated.model,
		timeout:          validated.timeout,
		maxOutputTokens:  validated.maxOutputTokens,
		temperature:      validated.temperature,
		maxPromptBytes:   validated.maxPromptBytes,
		maxContextBytes:  validated.maxContextBytes,
		maxCombinedBytes: validated.maxCombinedBytes,
		httpClient:       httpClient,
		tokenProvider:    cfg.TokenProvider,
		userAgent:        "cia-mcp-inference/" + version,
	}, nil
}

// Delegate validates and executes one stateless request. It never retains the
// prompt, context, response, or credential after returning.
func (c *Client) Delegate(ctx context.Context, input DelegateInput) (DelegateOutput, error) {
	maxTokens, err := c.validateInput(input)
	if err != nil {
		return DelegateOutput{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	token, err := c.tokenProvider.Token(callCtx)
	if err != nil {
		if contextErr := delegationContextError(callCtx); contextErr != nil {
			return DelegateOutput{}, contextErr
		}
		return DelegateOutput{}, errors.New("local AI inference credential is unavailable")
	}
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return DelegateOutput{}, errors.New("local AI inference credential is invalid")
	}

	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: delegationSystemMessage},
			{Role: "user", Content: delegatedUserText(input.Context, input.Prompt)},
		},
		Stream:      false,
		MaxTokens:   maxTokens,
		Temperature: c.temperature,
	})
	if err != nil {
		return DelegateOutput{}, errors.New("could not encode local AI request")
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return DelegateOutput{}, errors.New("could not build local AI request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if contextErr := delegationContextError(callCtx); contextErr != nil {
			return DelegateOutput{}, contextErr
		}
		return DelegateOutput{}, errors.New("local AI provider is unavailable")
	}
	defer resp.Body.Close()

	payload, err := readResponse(resp.Body)
	if err != nil {
		if contextErr := delegationContextError(callCtx); contextErr != nil {
			return DelegateOutput{}, contextErr
		}
		return DelegateOutput{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return DelegateOutput{}, providerError(resp.StatusCode, resp.Header.Get("X-Request-Id"), payload)
	}

	var decoded chatResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return DelegateOutput{}, errors.New("local AI provider returned invalid JSON")
	}
	if len(decoded.Choices) != 1 {
		return DelegateOutput{}, errors.New("local AI provider returned an invalid choice count")
	}
	choice := decoded.Choices[0]
	if len(choice.Message.ToolCalls) != 0 {
		return DelegateOutput{}, errors.New("local AI provider returned an unsupported tool call")
	}
	if len(choice.Message.Content) > maximumOutputTextBytes {
		return DelegateOutput{}, errors.New("local AI provider output exceeds 1 MiB")
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return DelegateOutput{}, errors.New("local AI provider returned no text")
	}
	if !safeMetadata(choice.FinishReason, 64) {
		return DelegateOutput{}, errors.New("local AI provider returned an invalid finish reason")
	}
	if decoded.Usage.PromptTokens < 0 || decoded.Usage.CompletionTokens < 0 || decoded.Usage.TotalTokens < 0 {
		return DelegateOutput{}, errors.New("local AI provider returned invalid usage")
	}
	if decoded.Usage.TotalTokens != 0 && decoded.Usage.TotalTokens < decoded.Usage.PromptTokens+decoded.Usage.CompletionTokens {
		return DelegateOutput{}, errors.New("local AI provider returned inconsistent usage")
	}

	return DelegateOutput{
		Model:        c.model,
		Text:         choice.Message.Content,
		FinishReason: choice.FinishReason,
		Usage:        decoded.Usage,
	}, nil
}

func delegationContextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.Canceled:
		return errors.New("local AI delegation was canceled")
	case context.DeadlineExceeded:
		return errors.New("local AI delegation timed out")
	default:
		return nil
	}
}

func (c *Client) validateInput(input DelegateInput) (int, error) {
	if strings.TrimSpace(input.Prompt) == "" {
		return 0, errors.New("prompt is required")
	}
	if len(input.Prompt) > c.maxPromptBytes {
		return 0, fmt.Errorf("prompt exceeds %d bytes", c.maxPromptBytes)
	}
	if len(input.Context) > c.maxContextBytes {
		return 0, fmt.Errorf("context exceeds %d bytes", c.maxContextBytes)
	}
	if len(input.Prompt)+len(input.Context) > c.maxCombinedBytes {
		return 0, fmt.Errorf("combined prompt and context exceed %d bytes", c.maxCombinedBytes)
	}
	maxTokens := input.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = c.maxOutputTokens
	}
	if maxTokens < 1 || maxTokens > c.maxOutputTokens {
		return 0, fmt.Errorf("max_output_tokens must be between 1 and %d", c.maxOutputTokens)
	}
	return maxTokens, nil
}

func delegatedUserText(contextText, prompt string) string {
	if strings.TrimSpace(contextText) == "" {
		return prompt
	}
	return "Reference context (user supplied):\n" + contextText + "\n\nTask (user supplied):\n" + prompt
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func readResponse(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maximumResponseBytes+1))
	if err != nil {
		return nil, errors.New("could not read local AI provider response")
	}
	if len(payload) > maximumResponseBytes {
		return nil, errors.New("local AI provider response exceeds 4 MiB")
	}
	return payload, nil
}

func providerError(statusCode int, requestID string, payload []byte) error {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code := ""
	if json.Unmarshal(payload, &envelope) == nil && safeMetadata(envelope.Error.Code, 64) {
		code = envelope.Error.Code
	}
	if !safeMetadata(requestID, 128) {
		requestID = ""
	}
	status := fmt.Sprintf("HTTP %d", statusCode)
	if text := http.StatusText(statusCode); text != "" {
		status += " " + text
	}
	return &APIError{StatusCode: statusCode, Status: status, Code: code, RequestID: requestID}
}

func safeMetadata(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
