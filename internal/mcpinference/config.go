package mcpinference

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultDataURL         = "http://127.0.0.1:8090"
	defaultModelID         = "local-coding"
	defaultTimeout         = 5 * time.Minute
	maximumTimeout         = 40 * time.Minute
	defaultOutputTokens    = 2048
	maximumOutputTokens    = 65536
	defaultTemperature     = 0.2
	maximumTemperature     = 2.0
	defaultPromptBytes     = 64 << 10
	defaultContextBytes    = 128 << 10
	defaultCombinedBytes   = 160 << 10
	maximumModelIDBytes    = 256
	maximumResponseBytes   = 4 << 20
	maximumOutputTextBytes = 1 << 20
)

// Config pins the only edge endpoint and model available to the inference MCP
// process. The input size fields are primarily exposed for tests and may only
// reduce, never raise, the compiled defensive ceilings.
type Config struct {
	DataURL          string
	Model            string
	Timeout          time.Duration
	MaxOutputTokens  int
	Temperature      float64
	MaxPromptBytes   int
	MaxContextBytes  int
	MaxCombinedBytes int
	HTTPClient       *http.Client
	TokenProvider    TokenProvider
}

// ConfigFromEnv reads only non-secret process configuration. The inference
// credential is deliberately obtained from Windows Credential Manager by the
// executable when a tool call is admitted.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		DataURL:          envOr("CIA_MCP_INFERENCE_DATA_URL", defaultDataURL),
		Model:            envOr("CIA_MCP_INFERENCE_MODEL", defaultModelID),
		Timeout:          defaultTimeout,
		MaxOutputTokens:  defaultOutputTokens,
		Temperature:      defaultTemperature,
		MaxPromptBytes:   defaultPromptBytes,
		MaxContextBytes:  defaultContextBytes,
		MaxCombinedBytes: defaultCombinedBytes,
	}

	if raw := strings.TrimSpace(os.Getenv("CIA_MCP_INFERENCE_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, errors.New("CIA_MCP_INFERENCE_TIMEOUT must be a valid duration")
		}
		cfg.Timeout = value
	}
	if raw := strings.TrimSpace(os.Getenv("CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, errors.New("CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS must be an integer")
		}
		cfg.MaxOutputTokens = value
	}
	if raw := strings.TrimSpace(os.Getenv("CIA_MCP_INFERENCE_TEMPERATURE")); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Config{}, errors.New("CIA_MCP_INFERENCE_TEMPERATURE must be a number")
		}
		cfg.Temperature = value
	}

	if _, err := validateConfig(cfg, false); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type validatedConfig struct {
	baseURL          *url.URL
	model            string
	timeout          time.Duration
	maxOutputTokens  int
	temperature      float64
	maxPromptBytes   int
	maxContextBytes  int
	maxCombinedBytes int
}

func validateConfig(cfg Config, requireTokenProvider bool) (validatedConfig, error) {
	baseURL, err := validateDataURL(cfg.DataURL)
	if err != nil {
		return validatedConfig{}, err
	}
	model, err := validateModelID(cfg.Model)
	if err != nil {
		return validatedConfig{}, err
	}
	if requireTokenProvider && cfg.TokenProvider == nil {
		return validatedConfig{}, errors.New("local AI inference credential is not configured")
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout <= 0 || timeout > maximumTimeout {
		return validatedConfig{}, fmt.Errorf("inference timeout must be greater than zero and at most %s", maximumTimeout)
	}
	maxOutput := cfg.MaxOutputTokens
	if maxOutput == 0 {
		maxOutput = defaultOutputTokens
	}
	if maxOutput < 1 || maxOutput > maximumOutputTokens {
		return validatedConfig{}, fmt.Errorf("maximum output tokens must be between 1 and %d", maximumOutputTokens)
	}
	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = defaultTemperature
	}
	if temperature < 0 || temperature > maximumTemperature {
		return validatedConfig{}, fmt.Errorf("temperature must be between 0 and %v", maximumTemperature)
	}

	maxPrompt := defaultIfZero(cfg.MaxPromptBytes, defaultPromptBytes)
	maxContext := defaultIfZero(cfg.MaxContextBytes, defaultContextBytes)
	maxCombined := defaultIfZero(cfg.MaxCombinedBytes, defaultCombinedBytes)
	if maxPrompt < 1 || maxPrompt > defaultPromptBytes {
		return validatedConfig{}, fmt.Errorf("maximum prompt bytes must be between 1 and %d", defaultPromptBytes)
	}
	if maxContext < 1 || maxContext > defaultContextBytes {
		return validatedConfig{}, fmt.Errorf("maximum context bytes must be between 1 and %d", defaultContextBytes)
	}
	if maxCombined < 1 || maxCombined > defaultCombinedBytes {
		return validatedConfig{}, fmt.Errorf("maximum combined input bytes must be between 1 and %d", defaultCombinedBytes)
	}

	return validatedConfig{
		baseURL:          baseURL,
		model:            model,
		timeout:          timeout,
		maxOutputTokens:  maxOutput,
		temperature:      temperature,
		maxPromptBytes:   maxPrompt,
		maxContextBytes:  maxContext,
		maxCombinedBytes: maxCombined,
	}, nil
}

func validateDataURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("invalid local AI data URL")
	}
	if u.Scheme != "http" {
		return nil, errors.New("local AI data URL must use http over loopback")
	}
	if u.User != nil {
		return nil, errors.New("local AI data URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("local AI data URL must not contain a path, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local AI data URL host must be a literal loopback IP address")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("local AI data URL must include an explicit numeric port")
	}
	u.Path = ""
	return u, nil
}

func validateModelID(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" {
		return "", errors.New("pinned local AI model is required")
	}
	if len(model) > maximumModelIDBytes {
		return "", fmt.Errorf("pinned local AI model exceeds %d bytes", maximumModelIDBytes)
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return "", errors.New("pinned local AI model contains control characters")
		}
	}
	return model, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func defaultIfZero(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
