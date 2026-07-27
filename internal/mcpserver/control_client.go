package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultControlURL     = "http://127.0.0.1:8091"
	defaultControlTimeout = 5 * time.Second
	maxControlTimeout     = 30 * time.Second
	maxResponseBytes      = 1 << 20
)

// Config configures the read-only MCP server's control-plane client.
type Config struct {
	ControlURL string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// ConfigFromEnv builds a safe local-only, read-only configuration. It never
// reads an administrative token or starts a credential helper.
func ConfigFromEnv() (Config, error) {
	rawURL := strings.TrimSpace(os.Getenv("CIA_CONTROL_URL"))
	if rawURL == "" {
		rawURL = defaultControlURL
	}

	timeout := defaultControlTimeout
	if raw := strings.TrimSpace(os.Getenv("CIA_CONTROL_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid CIA_CONTROL_TIMEOUT: %w", err)
		}
		if parsed <= 0 || parsed > maxControlTimeout {
			return Config{}, fmt.Errorf("CIA_CONTROL_TIMEOUT must be greater than zero and at most %s", maxControlTimeout)
		}
		timeout = parsed
	}

	return Config{
		ControlURL: rawURL,
		Timeout:    timeout,
	}, nil
}

// ControlClient performs bounded, read-only requests against the loopback
// control API. Redirects are never followed.
type ControlClient struct {
	baseURL    *url.URL
	httpClient *http.Client
	userAgent  string
}

// NewControlClient validates the local-only boundary and creates a client.
func NewControlClient(cfg Config, version string) (*ControlClient, error) {
	baseURL, err := validateControlURL(cfg.ControlURL)
	if err != nil {
		return nil, err
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

	return &ControlClient{
		baseURL:    baseURL,
		httpClient: client,
		userAgent:  "cia-mcp/" + version,
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
	if u.Hostname() == "" {
		return nil, errors.New("control URL must contain a loopback host")
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("control URL host must be a literal loopback IP address")
	}

	u.Path = ""
	return u, nil
}

// Probe describes the public liveness/readiness response.
type Probe struct {
	Status            string `json:"status"`
	Service           string `json:"service"`
	UpstreamReachable *bool  `json:"upstream_reachable,omitempty"`
}

// Model is the stable model projection exposed by the control API.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// UpstreamStatus is deliberately limited to non-secret operational metadata.
type UpstreamStatus struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
}

// GateStatus reports bounded inference admission state.
type GateStatus struct {
	Active             int    `json:"active"`
	Queued             int    `json:"queued"`
	MaxActive          int    `json:"max_active"`
	MaxQueue           int    `json:"max_queue"`
	WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
	RejectedTotal      uint64 `json:"rejected_total"`
	TimedOutTotal      uint64 `json:"timed_out_total"`
}

// CapacityStatus reports whether the configured profile can be admitted.
type CapacityStatus struct {
	Admission         string   `json:"admission"`
	Model             string   `json:"model"`
	ModelRunning      bool     `json:"model_running"`
	CommitHeadroomGiB *float64 `json:"commit_headroom_gib"`
	RequiredCommitGiB *float64 `json:"required_commit_gib"`
	ReserveCommitGiB  float64  `json:"reserve_commit_gib"`
	Measured          bool     `json:"measured"`
	Available         bool     `json:"available"`
	Reason            string   `json:"reason"`
}

// RecentEvent contains only the metadata allowlisted by cia-edge.
type RecentEvent struct {
	Time       string `json:"time"`
	RequestID  string `json:"request_id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

// ModelStatus reports the admission and lifecycle state for one published model.
type ModelStatus struct {
	ID        string         `json:"id"`
	Available bool           `json:"available"`
	Active    bool           `json:"active"`
	Reason    string         `json:"reason"`
	Capacity  CapacityStatus `json:"capacity"`
}

// Status is the control API status contract consumed by all read-only tools.
type Status struct {
	Service       string         `json:"service"`
	Version       string         `json:"version"`
	Ready         bool           `json:"ready"`
	Upstream      UpstreamStatus `json:"upstream"`
	Models        []Model        `json:"models"`
	ActiveModel   string         `json:"active_model"`
	Gate          GateStatus     `json:"gate"`
	Capacity      CapacityStatus `json:"capacity"`
	ModelStatuses []ModelStatus  `json:"model_statuses"`
	RecentEvents  []RecentEvent  `json:"recent_events"`
}

// Liveness reads the unauthenticated, side-effect-free liveness endpoint.
func (c *ControlClient) Liveness(ctx context.Context) (Probe, error) {
	var probe Probe
	if err := c.getJSON(ctx, "/livez", map[int]bool{http.StatusOK: true}, &probe); err != nil {
		return Probe{}, err
	}
	return probe, nil
}

// Readiness reads readiness without causing a model load. A 503 response is a
// valid not-ready state and is decoded into Probe.
func (c *ControlClient) Readiness(ctx context.Context) (Probe, error) {
	var probe Probe
	allowed := map[int]bool{http.StatusOK: true, http.StatusServiceUnavailable: true}
	if err := c.getJSON(ctx, "/readyz", allowed, &probe); err != nil {
		return Probe{}, err
	}
	return probe, nil
}

// Status reads the public, side-effect-free status snapshot. Keeping this
// request unauthenticated prevents a process that wins the loopback port during
// provider downtime from harvesting an administrative credential.
func (c *ControlClient) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := c.getJSON(ctx, "/api/v1/status", map[int]bool{http.StatusOK: true}, &status); err != nil {
		return Status{}, err
	}
	if status.Models == nil {
		status.Models = []Model{}
	}
	if status.RecentEvents == nil {
		status.RecentEvents = []RecentEvent{}
	}
	if status.ModelStatuses == nil {
		status.ModelStatuses = []ModelStatus{}
	}
	return status, nil
}

func (c *ControlClient) getJSON(ctx context.Context, path string, allowed map[int]bool, dst any) error {
	target := *c.baseURL
	target.Path = path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("build control request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("control API unavailable: %w", err)
	}
	defer resp.Body.Close()

	if !allowed[resp.StatusCode] {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return fmt.Errorf("control API GET %s returned %s", path, resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read control API response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return errors.New("control API response exceeds 1 MiB")
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return errors.New("control API returned invalid JSON")
	}
	return nil
}
