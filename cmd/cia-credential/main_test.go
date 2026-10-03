package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestRandomToken(t *testing.T) {
	t.Parallel()
	a, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < 32 || a == b {
		t.Fatalf("unexpected generated tokens")
	}
}

func TestOpenCodeEnvironmentDropsCloudCredentials(t *testing.T) {
	got := openCodeEnvironment([]string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"OPENCODE_CONFIG=C:\\safe.json",
		"OPENAI_API_KEY=cloud",
		"AWS_SECRET_ACCESS_KEY=cloud",
		"CIA_LOCAL_API_KEY=stale",
	})
	want := []string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"OPENCODE_CONFIG=C:\\safe.json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}

func TestEdgeEnvironmentReplacesAllCIASecrets(t *testing.T) {
	got := edgeEnvironment([]string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"CIA_INFERENCE_TOKEN=stale",
		"CIA_ADMIN_TOKEN=stale",
		"CIA_ROUTER_TOKEN=stale",
		"CIA_CLAUDE_GATEWAY_TOKEN=stale",
		"CIA_UNRELATED=discard",
	}, map[string]string{
		"inference":      "test-inference",
		"admin":          "test-admin",
		"router":         "test-router",
		"claude-gateway": "test-claude-gateway",
	}, `D:\LocalAI\logs\cia-edge.jsonl`)
	want := []string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"CIA_INFERENCE_TOKEN=test-inference",
		"CIA_ADMIN_TOKEN=test-admin",
		"CIA_ROUTER_TOKEN=test-router",
		"CIA_CLAUDE_GATEWAY_TOKEN=test-claude-gateway",
		"CIA_EDGE_LOG_PATH=D:\\LocalAI\\logs\\cia-edge.jsonl",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}

func TestInstalledEdgePathsFollowCredentialHelperInstallation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Portable AI")
	credentialHelper := filepath.Join(root, "bin", "cia-credential.exe")
	edge, logPath, err := installedEdgePaths(credentialHelper)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "bin", "cia-edge.exe"); edge != want {
		t.Fatalf("edge=%q, want %q", edge, want)
	}
	if want := filepath.Join(root, "logs", "cia-edge.jsonl"); logPath != want {
		t.Fatalf("log=%q, want %q", logPath, want)
	}
}
