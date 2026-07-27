package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizedEnvironmentRemovesSecretsAndUnslothDeviceSelection(t *testing.T) {
	got := sanitizedEnvironment([]string{
		"Path=C:\\Windows",
		"CIA_INFERENCE_TOKEN=old",
		"cia_admin_token=old",
		"CIA_ROUTER_TOKEN=old",
		"CIA_EDGE_LOG_PATH=old",
		"HIP_VISIBLE_DEVICES=1",
		"OPENAI_API_KEY=cloud",
		"KEEP=value",
	})
	want := []string{"Path=C:\\Windows"}
	if len(got) != len(want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("environment = %v, want %v", got, want)
		}
	}
}

func TestWriteRouterAPIKeyUsesStateDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRouterAPIKey(root, "router-test-value"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "state", "router-api-key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "router-test-value\n" {
		t.Fatal("router key file content mismatch")
	}
}

func TestRequireWithinRejectsSibling(t *testing.T) {
	root := t.TempDir()
	if err := requireWithin(root, filepath.Join(root, "logs", "edge.log"), "log"); err != nil {
		t.Fatal(err)
	}
	if err := requireWithin(root, filepath.Join(root, "..", "outside.log"), "log"); err == nil {
		t.Fatal("sibling path was accepted")
	}
}

func TestLoopbackAddressRequiresLiteralIPAndPort(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8090", "[::1]:8090"} {
		if err := validateLoopbackAddr("test", address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"localhost:8090", "0.0.0.0:8090", "127.0.0.1"} {
		if err := validateLoopbackAddr("test", address); err == nil {
			t.Fatalf("unsafe address %s was accepted", address)
		}
	}
}
