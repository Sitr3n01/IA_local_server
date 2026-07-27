package panel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigStrictAndValidated(t *testing.T) {
	cfg := testConfig(t)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestFile(t, "panel.json", string(data))
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Environment != EnvironmentCanary || loaded.RefreshInterval() != 5*time.Second || loaded.OperationTimeout() != 30*time.Second {
		t.Fatalf("unexpected config: %+v", loaded)
	}

	unknown := strings.TrimSuffix(string(data), "}") + ",\"typo\":true}"
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown config field was accepted")
	}
}

func TestLoadConfigAcceptsWindowsPowerShellUTF8BOM(t *testing.T) {
	cfg := testConfig(t)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte{0xEF, 0xBB, 0xBF}, data...)
	path := writeTestFile(t, "panel.json", string(data))
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("PowerShell UTF-8 config was rejected: %v", err)
	}
}

func TestConfigRejectsUnsafeBoundaries(t *testing.T) {
	tests := map[string]func(*Config){
		"schema":             func(c *Config) { c.SchemaVersion = 3 },
		"environment":        func(c *Config) { c.Environment = "production" },
		"hostname":           func(c *Config) { c.ControlURL = "http://localhost:18091" },
		"non-loopback":       func(c *Config) { c.ControlURL = "http://192.168.1.2:18091" },
		"https":              func(c *Config) { c.ControlURL = "https://127.0.0.1:18091" },
		"credentials":        func(c *Config) { c.ControlURL = "http://user@127.0.0.1:18091" },
		"path":               func(c *Config) { c.ControlURL = "http://127.0.0.1:18091/api" },
		"relative manifest":  func(c *Config) { c.ManifestPath = "models.yaml" },
		"relative selection": func(c *Config) { c.SelectionPath = "selection.json" },
		"relative launcher":  func(c *Config) { c.Launchers.Codex = "Start-Codex.ps1" },
		"short refresh":      func(c *Config) { c.RefreshSeconds = 1 },
		"long refresh":       func(c *Config) { c.RefreshSeconds = 301 },
		"short timeout":      func(c *Config) { c.OperationTimeoutSeconds = 4 },
		"long timeout":       func(c *Config) { c.OperationTimeoutSeconds = 301 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe config was accepted")
			}
		})
	}
}

func TestControlURLAcceptsLiteralIPv4AndIPv6Loopback(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:18091", "http://[::1]:18091/"} {
		cfg := testConfig(t)
		cfg.ControlURL = raw
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}
