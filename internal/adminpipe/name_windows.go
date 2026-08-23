//go:build windows

package adminpipe

// DefaultName is the administrative pipe path for one deployment. The
// environment is part of the name so a canary and a final installation can
// never answer each other's administrative commands.
//
// The result must satisfy validatePipeName; TestDefaultNameSatisfiesTheListener
// asserts that, because a default the listener refuses would stop the edge from
// starting at all.
func DefaultName(environment string) string {
	switch environment {
	case "canary", "final":
		return pipePrefix + "cia-local-ai-admin-" + environment
	default:
		return ""
	}
}
