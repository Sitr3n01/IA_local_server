package mcpinference

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvValidatesNonSecretConfigurationBeforeCredentialInjection(t *testing.T) {
	t.Setenv("CIA_MCP_INFERENCE_DATA_URL", "http://127.0.0.1:18090")
	t.Setenv("CIA_MCP_INFERENCE_MODEL", "local-coding")
	t.Setenv("CIA_MCP_INFERENCE_TIMEOUT", "2m")
	t.Setenv("CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS", "4096")
	t.Setenv("CIA_MCP_INFERENCE_TEMPERATURE", "0.3")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenProvider != nil {
		t.Fatal("ConfigFromEnv must not read or install a credential provider")
	}
	if cfg.DataURL != "http://127.0.0.1:18090" || cfg.Model != "local-coding" || cfg.Timeout != 2*time.Minute || cfg.MaxOutputTokens != 4096 || cfg.Temperature != 0.3 {
		t.Fatalf("unexpected environment config: %+v", cfg)
	}

	cfg.TokenProvider = fixedTokenProvider()
	if _, err := NewClient(cfg, "test"); err != nil {
		t.Fatalf("NewClient after credential injection: %v", err)
	}
}

func TestConfigFromEnvRejectsInvalidValuesWithoutEchoingThem(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "non loopback", env: "CIA_MCP_INFERENCE_DATA_URL", value: "http://example.invalid:8090"},
		{name: "invalid timeout", env: "CIA_MCP_INFERENCE_TIMEOUT", value: "not-a-duration"},
		{name: "excessive timeout", env: "CIA_MCP_INFERENCE_TIMEOUT", value: "41m"},
		{name: "invalid output", env: "CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS", value: "secret-invalid-output"},
		{name: "excessive output", env: "CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS", value: "65537"},
		{name: "invalid temperature", env: "CIA_MCP_INFERENCE_TEMPERATURE", value: "secret-invalid-temperature"},
		{name: "excessive temperature", env: "CIA_MCP_INFERENCE_TEMPERATURE", value: "2.1"},
		{name: "negative temperature", env: "CIA_MCP_INFERENCE_TEMPERATURE", value: "-0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CIA_MCP_INFERENCE_DATA_URL", "")
			t.Setenv("CIA_MCP_INFERENCE_MODEL", "")
			t.Setenv("CIA_MCP_INFERENCE_TIMEOUT", "")
			t.Setenv("CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS", "")
			t.Setenv("CIA_MCP_INFERENCE_TEMPERATURE", "")
			t.Setenv(test.env, test.value)
			if _, err := ConfigFromEnv(); err == nil {
				t.Fatal("ConfigFromEnv succeeded")
			} else if strings.Contains(err.Error(), test.value) {
				t.Fatalf("configuration error echoed environment value: %q", err)
			}
		})
	}
}

func TestNewClientRequiresLiteralLoopbackRootAndCredential(t *testing.T) {
	tests := []string{
		"https://127.0.0.1:8090",
		"http://localhost:8090",
		"http://0.0.0.0:8090",
		"http://192.168.1.20:8090",
		"http://user:pass@127.0.0.1:8090",
		"http://127.0.0.1:8090/v1",
		"http://127.0.0.1:8090?token=secret",
		"http://127.0.0.1",
	}
	for _, dataURL := range tests {
		t.Run(dataURL, func(t *testing.T) {
			_, err := NewClient(Config{
				DataURL:       dataURL,
				Model:         "local-coding",
				TokenProvider: fixedTokenProvider(),
			}, "test")
			if err == nil {
				t.Fatal("NewClient succeeded")
			}
		})
	}

	_, err := NewClient(Config{DataURL: "http://127.0.0.1:8090", Model: "local-coding"}, "test")
	if err == nil {
		t.Fatal("NewClient without TokenProvider succeeded")
	}
}

func TestNewClientEnforcesCompiledCeilings(t *testing.T) {
	base := Config{
		DataURL:       "http://127.0.0.1:8090",
		Model:         "local-coding",
		TokenProvider: fixedTokenProvider(),
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "model control", change: func(c *Config) { c.Model = "local\ncoding" }},
		{name: "timeout", change: func(c *Config) { c.Timeout = maximumTimeout + time.Second }},
		{name: "output", change: func(c *Config) { c.MaxOutputTokens = maximumOutputTokens + 1 }},
		{name: "temperature", change: func(c *Config) { c.Temperature = maximumTemperature + 1 }},
		{name: "prompt", change: func(c *Config) { c.MaxPromptBytes = defaultPromptBytes + 1 }},
		{name: "context", change: func(c *Config) { c.MaxContextBytes = defaultContextBytes + 1 }},
		{name: "combined", change: func(c *Config) { c.MaxCombinedBytes = defaultCombinedBytes + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.change(&cfg)
			if _, err := NewClient(cfg, "test"); err == nil {
				t.Fatal("NewClient succeeded")
			}
		})
	}
}

func fixedTokenProvider() TokenProvider {
	return TokenProviderFunc(func(context.Context) (string, error) {
		return "test-inference-token-0000000000000000", nil
	})
}
