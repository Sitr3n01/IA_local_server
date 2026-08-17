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
	ID          string   `json:"id"`
	Object      string   `json:"object"`
	OwnedBy     string   `json:"owned_by"`
	State       string   `json:"-"`
	Deployments []string `json:"-"`

	// Admission-control inputs. All of them are optional in the manifest; a nil
	// value means "not measured", which is stricter than zero, not laxer.
	PeakCommitGiB *float64 `json:"-"`
	PeakVRAMGiB   *float64 `json:"-"`
	PeakRAMGiB    *float64 `json:"-"`
	DeviceVRAMGiB *float64 `json:"-"`
	CacheRAMMiB   *int     `json:"-"`
	// OffloadsTensors marks a model that deliberately keeps part of its weights
	// in system RAM. Such a model cannot be admitted on an unmeasured profile.
	OffloadsTensors bool `json:"-"`

	// Observability only. These are reported through /api/v1/status so an
	// operator can see which build is actually serving, and are excluded from
	// /v1/models so the public model list stays exactly what it was.
	Runtime       RuntimeSummary    `json:"-"`
	ContextTokens *int              `json:"-"`
	Checkpoints   CheckpointSummary `json:"-"`
}

// RuntimeSummary identifies a runtime by what it is rather than by where it was
// installed. Two builds of the same engine differ here by variant, commit, and
// artifact hash; a directory name distinguishes nothing.
type RuntimeSummary struct {
	ID                     string `json:"id"`
	State                  string `json:"state,omitempty"`
	Engine                 string `json:"engine,omitempty"`
	Variant                string `json:"variant,omitempty"`
	Backend                string `json:"backend,omitempty"`
	SourceRepository       string `json:"source_repository,omitempty"`
	Commit                 string `json:"commit,omitempty"`
	ArtifactSHA256Prefix   string `json:"artifact_sha256_prefix,omitempty"`
	CheckpointCapable      bool   `json:"checkpoint_capable"`
	CheckpointFixReference string `json:"checkpoint_fix_reference,omitempty"`
}

// CheckpointSummary is the checkpoint configuration a model asks the runtime
// for. Nil means the field is absent from the manifest and the runtime's own
// default applies, which is not the same as zero.
type CheckpointSummary struct {
	Count   *int `json:"ctx_checkpoints"`
	MinStep *int `json:"checkpoint_min_step"`
}

// Configured reports whether the manifest states a checkpoint policy at all.
// A configuration on a runtime that cannot restore hybrid checkpoints is inert,
// which is why the status reports both this and the runtime's capability.
func (c CheckpointSummary) Configured() bool {
	return c.Count != nil || c.MinStep != nil
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
	// PublicModelID is provider.public_model. Readiness and the headline
	// capacity figure are reported for this model specifically; the position of
	// an entry in Models must never carry meaning.
	PublicModelID string
	Version       string

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
		PublicModelID:   DefaultModelID,
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
	if strings.TrimSpace(c.PublicModelID) == "" {
		return errors.New("a public model ID is required")
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
		if model.PeakVRAMGiB != nil && *model.PeakVRAMGiB <= 0 {
			return fmt.Errorf("model %q peak VRAM must be positive when configured", model.ID)
		}
		if model.PeakRAMGiB != nil && *model.PeakRAMGiB <= 0 {
			return fmt.Errorf("model %q peak RAM must be positive when configured", model.ID)
		}
		if model.DeviceVRAMGiB != nil && *model.DeviceVRAMGiB <= 0 {
			return fmt.Errorf("model %q device VRAM budget must be positive when configured", model.ID)
		}
		if model.CacheRAMMiB != nil && *model.CacheRAMMiB < 0 {
			return fmt.Errorf("model %q prompt cache size cannot be negative", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	if _, ok := seen[c.PublicModelID]; !ok {
		return fmt.Errorf("public model %q is not in the allowlist", c.PublicModelID)
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
