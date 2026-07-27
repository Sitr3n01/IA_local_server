package edge

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDataAddr        = "127.0.0.1:8090"
	DefaultControlAddr     = "127.0.0.1:8091"
	DefaultUpstreamURL     = "http://127.0.0.1:9292"
	DefaultModelID         = "local-coding"
	DefaultMaxWireBytes    = int64(16 << 20)
	DefaultMaxDecodedBytes = int64(64 << 20)
	DefaultMaxRatio        = int64(100)
	DefaultMaxActive       = 1
	DefaultMaxQueue        = 4
	DefaultQueueWait       = 120 * time.Second
	DefaultHeaderTimeout   = 1800 * time.Second
	DefaultShutdownTimeout = 15 * time.Second
)

// Model is the stable, public model identity exposed by the edge. It is kept
// intentionally small: provenance and runtime details belong to the generated
// model manifest, not to the OpenAI-compatible wire response.
type Model struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	OwnedBy       string   `json:"owned_by"`
	State         string   `json:"-"`
	Deployments   []string `json:"-"`
	PeakCommitGiB *float64 `json:"-"`
}

// Config contains every network and admission-control decision made by the
// edge. Addresses and the upstream are validated as loopback-only by Validate.
type Config struct {
	DataAddr       string
	ControlAddr    string
	UpstreamURL    string
	InferenceToken string
	AdminToken     string
	RouterToken    string
	Models         []Model
	Version        string

	MaxWireBytes    int64
	MaxDecodedBytes int64
	MaxRatio        int64
	MaxActive       int
	MaxQueue        int
	QueueWait       time.Duration
	HeaderTimeout   time.Duration
	ShutdownTimeout time.Duration
	LogOutput       io.Writer
}

func DefaultConfig() Config {
	return Config{
		DataAddr:    DefaultDataAddr,
		ControlAddr: DefaultControlAddr,
		UpstreamURL: DefaultUpstreamURL,
		Models: []Model{{
			ID:          DefaultModelID,
			Object:      "model",
			OwnedBy:     "local",
			State:       "candidate",
			Deployments: []string{"canary"},
		}},
		Version:         "dev",
		MaxWireBytes:    DefaultMaxWireBytes,
		MaxDecodedBytes: DefaultMaxDecodedBytes,
		MaxRatio:        DefaultMaxRatio,
		MaxActive:       DefaultMaxActive,
		MaxQueue:        DefaultMaxQueue,
		QueueWait:       DefaultQueueWait,
		HeaderTimeout:   DefaultHeaderTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
		LogOutput:       os.Stdout,
	}
}

// ConfigFromEnv reads process-local configuration. Secrets are deliberately
// accepted only through environment variables and never through command-line
// arguments, which keeps them out of process listings and generated configs.
func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	cfg.DataAddr = envOr("CIA_EDGE_DATA_ADDR", cfg.DataAddr)
	cfg.ControlAddr = envOr("CIA_EDGE_CONTROL_ADDR", cfg.ControlAddr)
	cfg.UpstreamURL = envOr("CIA_EDGE_UPSTREAM_URL", cfg.UpstreamURL)
	cfg.InferenceToken = os.Getenv("CIA_INFERENCE_TOKEN")
	cfg.AdminToken = os.Getenv("CIA_ADMIN_TOKEN")
	cfg.RouterToken = os.Getenv("CIA_ROUTER_TOKEN")
	cfg.Version = envOr("CIA_EDGE_VERSION", cfg.Version)

	var err error
	if cfg.MaxActive, err = envInt("CIA_EDGE_MAX_ACTIVE", cfg.MaxActive); err != nil {
		return Config{}, err
	}
	if cfg.MaxQueue, err = envInt("CIA_EDGE_MAX_QUEUE", cfg.MaxQueue); err != nil {
		return Config{}, err
	}
	if cfg.QueueWait, err = envDuration("CIA_EDGE_QUEUE_WAIT", cfg.QueueWait); err != nil {
		return Config{}, err
	}
	if cfg.HeaderTimeout, err = envDuration("CIA_EDGE_HEADER_TIMEOUT", cfg.HeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = envDuration("CIA_EDGE_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if err := validateLoopbackAddr("data address", c.DataAddr); err != nil {
		return err
	}
	if err := validateLoopbackAddr("control address", c.ControlAddr); err != nil {
		return err
	}
	if c.DataAddr == c.ControlAddr {
		return errors.New("data and control addresses must be different")
	}

	u, err := url.Parse(c.UpstreamURL)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("upstream URL must be a plain loopback http URL without credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("upstream URL must not contain a path")
	}
	if err := validateLoopbackHost("upstream", u.Hostname()); err != nil {
		return err
	}
	if u.Port() == "" {
		return errors.New("upstream URL must include an explicit port")
	}
	if err := validatePort("upstream", u.Port()); err != nil {
		return err
	}

	if err := validateTokenSecret("CIA_INFERENCE_TOKEN", c.InferenceToken, false); err != nil {
		return err
	}
	if err := validateTokenSecret("CIA_ADMIN_TOKEN", c.AdminToken, false); err != nil {
		return err
	}
	if err := validateTokenSecret("CIA_ROUTER_TOKEN", c.RouterToken, true); err != nil {
		return err
	}
	if c.InferenceToken == c.AdminToken {
		return errors.New("inference and admin tokens must be different")
	}
	if c.RouterToken != "" && (c.RouterToken == c.InferenceToken || c.RouterToken == c.AdminToken) {
		return errors.New("router token must be different from inference and admin tokens")
	}
	if len(c.Models) == 0 {
		return errors.New("at least one allowed model is required")
	}
	seen := make(map[string]struct{}, len(c.Models))
	for _, model := range c.Models {
		if strings.TrimSpace(model.ID) == "" {
			return errors.New("model IDs cannot be empty")
		}
		if _, duplicate := seen[model.ID]; duplicate {
			return fmt.Errorf("duplicate model ID %q", model.ID)
		}
		if model.PeakCommitGiB != nil && *model.PeakCommitGiB <= 0 {
			return fmt.Errorf("model %q peak commit must be positive when configured", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	if c.MaxWireBytes <= 0 || c.MaxDecodedBytes <= 0 || c.MaxDecodedBytes < c.MaxWireBytes {
		return errors.New("body limits must be positive and decoded limit must be at least the wire limit")
	}
	if c.MaxRatio <= 0 {
		return errors.New("maximum compression ratio must be positive")
	}
	if c.MaxActive <= 0 || c.MaxQueue < 0 || c.QueueWait <= 0 {
		return errors.New("invalid admission-control configuration")
	}
	if c.HeaderTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return errors.New("timeouts must be positive")
	}
	return nil
}

func validateTokenSecret(name, token string, optional bool) error {
	if token == "" && optional {
		return nil
	}
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("%s must contain 32 to 4096 characters without line breaks", name)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return value, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	return value, nil
}

func validateLoopbackAddr(label, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, addr, err)
	}
	if port == "" {
		return fmt.Errorf("%s must include a port", label)
	}
	if err := validatePort(label, port); err != nil {
		return err
	}
	if err := validateLoopbackHost(label, host); err != nil {
		return err
	}
	return nil
}

func validatePort(label, raw string) error {
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%s must use a numeric port between 1 and 65535", label)
	}
	return nil
}

func validateLoopbackHost(label, host string) error {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use a literal loopback address", label)
	}
	return nil
}
