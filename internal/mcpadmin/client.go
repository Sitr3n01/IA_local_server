package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	defaultControlTimeout = 5 * time.Second
	maxControlTimeout     = 5 * time.Minute
	maxResponseBytes      = 64 << 10
	maxModelIDBytes       = 256
)

// TokenProvider obtains the control-plane bearer token without exposing it to
// logs or tool results.
type TokenProvider interface {
	Token(context.Context) (string, error)
}

// TokenProviderFunc adapts a function to TokenProvider.
type TokenProviderFunc func(context.Context) (string, error)

func (f TokenProviderFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}

// Config configures the administrative control-plane client.
type Config struct {
	ControlURL    string
	Timeout       time.Duration
	HTTPClient    *http.Client
	TokenProvider TokenProvider
}

// Client performs only the three explicitly supported model lifecycle
// operations against a literal loopback control API.
type Client struct {
	baseURL       *url.URL
	httpClient    *http.Client
	tokenProvider TokenProvider
	userAgent     string
}

// OperationOutput is the structured success contract returned by cia-edge.
type OperationOutput struct {
	Operation   string `json:"operation"`
	Model       string `json:"model"`
	Status      string `json:"status"`
	ActiveModel string `json:"active_model"`
}

// APIError is the deliberately small error projection exposed to operator
// clients. Response messages and bodies are never retained because an
// upstream failure might contain sensitive runtime details.
type APIError struct {
	StatusCode int
	Status     string
	Code       string
	RequestID  string
}

func (e *APIError) Error() string {
	if e == nil {
		return "administrative control API error"
	}
	result := "administrative control API returned " + e.Status
	if e.Code != "" {
		result += " (" + e.Code + ")"
	}
	if e.RequestID != "" {
		result += " [request " + e.RequestID + "]"
	}
	return result
}

// NewClient validates the local-only boundary and disables proxying and
// redirects before any bearer token can be sent.
func NewClient(cfg Config, version string) (*Client, error) {
	baseURL, err := validateControlURL(cfg.ControlURL)
	if err != nil {
		return nil, err
	}
	if cfg.TokenProvider == nil {
		return nil, errors.New("administrative control credential is not configured")
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultControlTimeout
	}
	if timeout < 0 || timeout > maxControlTimeout {
		return nil, fmt.Errorf("control timeout must be greater than zero and at most %s", maxControlTimeout)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Timeout: timeout, Transport: transport}
	if cfg.HTTPClient != nil {
		copyClient := *cfg.HTTPClient
		client = &copyClient
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	if version == "" {
		version = "dev"
	}
	return &Client{
		baseURL:       baseURL,
		httpClient:    client,
		tokenProvider: cfg.TokenProvider,
		userAgent:     "cia-mcp-admin/" + version,
	}, nil
}

func validateControlURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid control URL: %w", err)
	}
	if u.Scheme != "http" {
		return nil, errors.New("control URL must use http over loopback")
	}
	if u.User != nil {
		return nil, errors.New("control URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("control URL must not contain a path, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("control URL host must be a literal loopback IP address")
	}
	u.Path = ""
	return u, nil
}

// Load asks the control plane to load modelID.
func (c *Client) Load(ctx context.Context, modelID string) (OperationOutput, error) {
	return c.operate(ctx, modelID, "load")
}

// Unload asks the control plane to unload modelID.
func (c *Client) Unload(ctx context.Context, modelID string) (OperationOutput, error) {
	return c.operate(ctx, modelID, "unload")
}

// Switch asks the control plane to switch explicitly to modelID.
func (c *Client) Switch(ctx context.Context, modelID string) (OperationOutput, error) {
	return c.operate(ctx, modelID, "switch")
}

func (c *Client) operate(ctx context.Context, modelID, operation string) (OperationOutput, error) {
	modelID, err := validateModelID(modelID)
	if err != nil {
		return OperationOutput{}, err
	}
	if operation != "load" && operation != "unload" && operation != "switch" {
		return OperationOutput{}, errors.New("unsupported administrative operation")
	}

	token, err := c.tokenProvider.Token(ctx)
	if err != nil {
		return OperationOutput{}, fmt.Errorf("obtain administrative control credential: %w", err)
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return OperationOutput{}, errors.New("administrative control credential is empty or invalid")
	}

	target := strings.TrimSuffix(c.baseURL.String(), "/") +
		"/api/v1/models/" + url.PathEscape(modelID) + ":" + operation
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return OperationOutput{}, errors.New("build administrative control request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return OperationOutput{}, fmt.Errorf("administrative control API unavailable: %w", err)
	}
	defer resp.Body.Close()

	payload, err := readBounded(resp.Body)
	if err != nil {
		return OperationOutput{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return OperationOutput{}, operationError(resp.StatusCode, resp.Status, resp.Header.Get("X-Request-Id"), payload)
	}

	var output OperationOutput
	if err := json.Unmarshal(payload, &output); err != nil {
		return OperationOutput{}, errors.New("administrative control API returned invalid JSON")
	}
	if output.Operation != operation || output.Model != modelID || output.Status != "completed" {
		return OperationOutput{}, errors.New("administrative control API returned an inconsistent result")
	}
	return output, nil
}

func validateModelID(modelID string) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", errors.New("model_id is required")
	}
	if len(modelID) > maxModelIDBytes {
		return "", fmt.Errorf("model_id exceeds %d bytes", maxModelIDBytes)
	}
	for _, r := range modelID {
		if unicode.IsControl(r) {
			return "", errors.New("model_id contains control characters")
		}
	}
	return modelID, nil
}

func readBounded(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read administrative control API response")
	}
	if len(payload) > maxResponseBytes {
		return nil, errors.New("administrative control API response exceeds 64 KiB")
	}
	return payload, nil
}

func operationError(statusCode int, status, requestID string, payload []byte) error {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code := ""
	if json.Unmarshal(payload, &envelope) == nil && safeErrorCode(envelope.Error.Code) {
		code = envelope.Error.Code
	}
	if !safeRequestID(requestID) {
		requestID = ""
	}
	return &APIError{StatusCode: statusCode, Status: status, Code: code, RequestID: requestID}
}

func safeErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func safeRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
