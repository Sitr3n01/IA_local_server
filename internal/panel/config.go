package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	configSchemaVersion = 2
	maxConfigBytes      = 64 << 10
)

// Environment identifies the isolated deployment whose model catalog the
// panel controls. Canary and final are deliberately separate selections.
type Environment string

const (
	EnvironmentCanary Environment = "canary"
	EnvironmentFinal  Environment = "final"
)

// LauncherPaths contains the operator-approved PowerShell entry points. The
// panel never discovers or constructs script paths from user input.
type LauncherPaths struct {
	Codex    string `json:"codex"`
	OpenCode string `json:"opencode"`
}

// Config is the complete, non-secret panel configuration. Durations are
// represented as integer seconds in JSON so configuration stays portable and
// unambiguous.
type Config struct {
	SchemaVersion           int           `json:"schema_version"`
	Environment             Environment   `json:"environment"`
	DataURL                 string        `json:"data_url"`
	ControlURL              string        `json:"control_url"`
	ManifestPath            string        `json:"manifest_path"`
	SelectionPath           string        `json:"selection_path"`
	ModelRootsPath          string        `json:"model_roots_path"`
	ValidationPath          string        `json:"validation_path"`
	LogsPath                string        `json:"logs_path"`
	Launchers               LauncherPaths `json:"launchers"`
	RefreshSeconds          int           `json:"refresh_seconds"`
	OperationTimeoutSeconds int           `json:"operation_timeout_seconds"`
}

// LoadConfig reads one strict JSON object. Unknown fields and trailing JSON
// values are rejected to prevent misspelled safety settings from being ignored.
func LoadConfig(path string) (Config, error) {
	data, err := readLimitedFile(path, maxConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("open panel config: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode panel config: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Config{}, fmt.Errorf("decode panel config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks every boundary used by the panel without touching the
// filesystem. Installation code remains responsible for existence and ACLs.
func (c Config) Validate() error {
	if c.SchemaVersion != configSchemaVersion {
		return fmt.Errorf("panel config schema_version must be %d", configSchemaVersion)
	}
	if !c.Environment.Valid() {
		return errors.New("panel environment must be canary or final")
	}
	if err := validateDataURL(c.DataURL); err != nil {
		return err
	}
	if err := validateControlURL(c.ControlURL); err != nil {
		return err
	}
	if err := validateAbsolutePath("manifest_path", c.ManifestPath); err != nil {
		return err
	}
	if err := validateAbsolutePath("selection_path", c.SelectionPath); err != nil {
		return err
	}
	if err := validateAbsolutePath("model_roots_path", c.ModelRootsPath); err != nil {
		return err
	}
	if err := validateAbsolutePath("validation_path", c.ValidationPath); err != nil {
		return err
	}
	if err := validateAbsolutePath("logs_path", c.LogsPath); err != nil {
		return err
	}
	if err := validateAbsolutePath("launchers.codex", c.Launchers.Codex); err != nil {
		return err
	}
	if err := validateAbsolutePath("launchers.opencode", c.Launchers.OpenCode); err != nil {
		return err
	}
	if c.RefreshSeconds < 2 || c.RefreshSeconds > 300 {
		return errors.New("refresh_seconds must be between 2 and 300")
	}
	if c.OperationTimeoutSeconds < 5 || c.OperationTimeoutSeconds > 300 {
		return errors.New("operation_timeout_seconds must be between 5 and 300")
	}
	return nil
}

func (e Environment) Valid() bool {
	return e == EnvironmentCanary || e == EnvironmentFinal
}

func (c Config) RefreshInterval() time.Duration {
	return time.Duration(c.RefreshSeconds) * time.Second
}

func (c Config) OperationTimeout() time.Duration {
	return time.Duration(c.OperationTimeoutSeconds) * time.Second
}

func validateControlURL(raw string) error {
	return validateLoopbackURL("control_url", raw)
}

func validateDataURL(raw string) error {
	return validateLoopbackURL("data_url", raw)
}

func validateLoopbackURL(label, raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return fmt.Errorf("%s must be a plain loopback HTTP URL", label)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", label, err)
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a plain loopback HTTP URL without credentials, query, or fragment", label)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%s must not contain a path", label)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || port == "" {
		return fmt.Errorf("%s must include an explicit numeric port", label)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use a literal loopback IP address", label)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s port must be between 1 and 65535", label)
	}
	return nil
}

func validateAbsolutePath(label, path string) error {
	if path == "" || strings.TrimSpace(path) != path || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path", label)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func readLimitedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	// Windows PowerShell 5.1 emits a UTF-8 BOM for -Encoding UTF8. Treat that
	// marker as transport encoding, while keeping the JSON document itself
	// strict.
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), nil
}
