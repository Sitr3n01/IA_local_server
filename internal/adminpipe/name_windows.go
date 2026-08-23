//go:build windows

package adminpipe

// DefaultName is the administrative pipe path for one deployment. The
// environment is part of the name so a canary and a final installation can
// never answer each other's administrative commands.
func DefaultName(environment string) string {
	switch environment {
	case "canary", "final":
		return `\.\pipe\cia-local-ai-admin-` + environment
	default:
		return ""
	}
}
