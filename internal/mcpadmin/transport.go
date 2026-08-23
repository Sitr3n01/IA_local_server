package mcpadmin

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sitr3n/local-ai-provider/internal/adminpipe"
)

// defaultInstallRoot is the one approved v2 installation root. Every deployment
// script pins the same path, so the administrative client can derive the edge
// executable from it instead of asking a harness for one.
const defaultInstallRoot = `C:\IA\local-ai-v2`

// AdminTransportFromEnv resolves the DACL-protected administrative transport
// for a process that knows only its control URL.
//
// Precedence is explicit configuration, then the declared environment, then the
// control port — which is a fixed architectural constant per deployment, not a
// guess. Returning an empty name leaves the client on the deprecated HTTP
// plane, which is what an installation predating the pipe requires.
func AdminTransportFromEnv(controlURL string) (pipe string, server string) {
	server = strings.TrimSpace(os.Getenv("CIA_ADMIN_PIPE_SERVER"))
	if server == "" {
		server = filepath.Join(defaultInstallRoot, "bin", "cia-edge.exe")
	}

	switch configured := strings.TrimSpace(os.Getenv("CIA_ADMIN_PIPE")); configured {
	case "off":
		return "", server
	case "":
	default:
		return configured, server
	}

	environment := strings.TrimSpace(strings.ToLower(os.Getenv("CIA_ENVIRONMENT")))
	if environment == "" {
		environment = environmentFromControlURL(controlURL)
	}
	return adminpipe.DefaultName(environment), server
}

// environmentFromControlURL maps the two pinned control ports to their
// deployment. Any other port is an unrecognised deployment and yields no pipe
// rather than a guessed one.
func environmentFromControlURL(controlURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(controlURL))
	if err != nil {
		return ""
	}
	switch parsed.Port() {
	case "18091":
		return "canary"
	case "8091":
		return "final"
	default:
		return ""
	}
}

// AdminPipeForInstallation resolves the transport for a process that already
// knows its environment and installation root, such as the operator panel.
func AdminPipeForInstallation(environment, installRoot string) (pipe string, server string) {
	root := strings.TrimSpace(installRoot)
	if root == "" {
		root = defaultInstallRoot
	}
	return adminpipe.DefaultName(strings.ToLower(strings.TrimSpace(environment))), filepath.Join(root, "bin", "cia-edge.exe")
}
