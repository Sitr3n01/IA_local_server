package edge

import (
	"strings"
	"testing"
)

func validConfigForTest() Config {
	cfg := DefaultConfig()
	cfg.InferenceToken = strings.Repeat("i", 32)
	cfg.AdminToken = strings.Repeat("a", 32)
	cfg.RouterToken = strings.Repeat("r", 32)
	return cfg
}

func TestConfigRejectsNonLoopbackNetworkTargets(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "data bind", change: func(cfg *Config) { cfg.DataAddr = "0.0.0.0:8090" }},
		{name: "control bind", change: func(cfg *Config) { cfg.ControlAddr = "192.168.1.2:8091" }},
		{name: "upstream", change: func(cfg *Config) { cfg.UpstreamURL = "https://api.openai.com:443" }},
		{name: "upstream credentials", change: func(cfg *Config) { cfg.UpstreamURL = "http://secret@127.0.0.1:9292" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfigForTest()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want loopback security error")
			}
		})
	}
}

func TestConfigRequiresDistinctStrongTokens(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "short inference", change: func(cfg *Config) { cfg.InferenceToken = "short" }},
		{name: "line break", change: func(cfg *Config) { cfg.AdminToken = strings.Repeat("a", 32) + "\n" }},
		{name: "same data and admin", change: func(cfg *Config) { cfg.AdminToken = cfg.InferenceToken }},
		{name: "same router and data", change: func(cfg *Config) { cfg.RouterToken = cfg.InferenceToken }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfigForTest()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want credential validation error")
			}
		})
	}
}
