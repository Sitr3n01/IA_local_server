//go:build windows

package mcpadmin

import (
	"strings"
	"testing"
)

// On Windows the administrative transport exists, so each deployment must
// resolve to its own endpoint. Two installations answering the same pipe would
// let a canary command reach a final provider.
func TestAdminTransportResolvesOnePipePerDeployment(t *testing.T) {
	t.Setenv("CIA_ADMIN_PIPE", "")
	t.Setenv("CIA_ADMIN_PIPE_SERVER", "")
	t.Setenv("CIA_ENVIRONMENT", "")

	canary, _ := AdminTransportFromEnv("http://127.0.0.1:18091")
	final, _ := AdminTransportFromEnv("http://127.0.0.1:8091")
	if canary == "" || final == "" {
		t.Fatalf("a pinned control port did not resolve a pipe: canary=%q final=%q", canary, final)
	}
	if canary == final {
		t.Fatalf("canary and final resolved to the same pipe: %q", canary)
	}
	for _, pipe := range []string{canary, final} {
		if !strings.HasPrefix(pipe, `\\.\pipe\cia-local-ai-admin-`) {
			t.Fatalf("unexpected pipe name: %q", pipe)
		}
	}

	// A declared environment takes precedence over the port derivation.
	t.Setenv("CIA_ENVIRONMENT", "final")
	if declared, _ := AdminTransportFromEnv("http://127.0.0.1:18091"); declared != final {
		t.Fatalf("CIA_ENVIRONMENT was ignored: %q, want %q", declared, final)
	}
	t.Setenv("CIA_ENVIRONMENT", "staging")
	if unknown, _ := AdminTransportFromEnv("http://127.0.0.1:8091"); unknown != "" {
		t.Fatalf("an unknown environment resolved to pipe %q", unknown)
	}
}

func TestAdminPipeForInstallationBindsTheEnvironmentAndRoot(t *testing.T) {
	pipe, server := AdminPipeForInstallation("canary", `C:\IA\local-ai-v2`)
	if !strings.HasSuffix(pipe, "canary") {
		t.Fatalf("pipe %q does not name the canary deployment", pipe)
	}
	if server != `C:\IA\local-ai-v2\bin\cia-edge.exe` {
		t.Fatalf("server executable = %q", server)
	}
	if _, fallback := AdminPipeForInstallation("final", ""); fallback != `C:\IA\local-ai-v2\bin\cia-edge.exe` {
		t.Fatalf("empty install root did not fall back to the approved root: %q", fallback)
	}
	if unknown, _ := AdminPipeForInstallation("staging", `C:\IA\local-ai-v2`); unknown != "" {
		t.Fatalf("an unknown environment resolved to pipe %q", unknown)
	}
}
