//go:build !windows

package mcpadmin

import "testing"

// Off Windows the administrative transport is absent by design: its DACL is the
// authentication, and an emulation without one would be an unauthenticated
// mutation channel. Resolving no pipe is therefore the correct answer, and a
// client that gets one falls back to the deprecated HTTP plane.
func TestAdminTransportIsAbsentWithoutTheWindowsDacl(t *testing.T) {
	t.Setenv("CIA_ADMIN_PIPE", "")
	t.Setenv("CIA_ADMIN_PIPE_SERVER", "")
	t.Setenv("CIA_ENVIRONMENT", "")

	for _, controlURL := range []string{"http://127.0.0.1:18091", "http://127.0.0.1:8091"} {
		if pipe, _ := AdminTransportFromEnv(controlURL); pipe != "" {
			t.Fatalf("%s resolved a pipe off Windows: %q", controlURL, pipe)
		}
	}
	for _, environment := range []string{"canary", "final", "staging", ""} {
		if pipe, _ := AdminPipeForInstallation(environment, "/opt/local-ai-v2"); pipe != "" {
			t.Fatalf("environment %q resolved a pipe off Windows: %q", environment, pipe)
		}
	}
}
