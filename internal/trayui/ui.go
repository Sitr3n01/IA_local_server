package trayui

import (
	"context"
	"errors"
	"time"
)

// ErrAlreadyRunning lets launchers remain idempotent when the tray for the
// same environment is already present in the notification area.
var ErrAlreadyRunning = errors.New("tray is already running")

// Model is the operator-facing projection of one manifest entry deployed to
// this environment. Available means the edge publishes it right now.
type Model struct {
	ID          string
	DisplayName string
	Available   bool
}

// Snapshot is a side-effect-free view of provider and operator state.
type Snapshot struct {
	Environment string
	// EdgeReachable is true when the edge answered either its status or its
	// readiness route, ready or not; false means nothing listens.
	EdgeReachable bool
	ProviderReady bool
	// ReadyNote says why a reachable edge is not ready, when the reason is
	// that its default model does not fit.
	ReadyNote       string
	UpstreamReady   bool
	StatusAvailable bool
	SelectedModel   string
	// SelectionNote explains a selection the tray could not honour, such as a
	// saved model that has since been retired. It is empty when the saved
	// selection is the one in use.
	SelectionNote   string
	ActiveModel     string
	Active          int
	Queued          int
	MaxActive       int
	MaxQueue        int
	CapacityOK      bool
	CapacityNote    string
	ClaudeAvailable bool
	ClaudeDetail    string
	ClaudeGatewayOK bool
	Models          []Model
	UpdatedAt       time.Time
}

// Controller keeps all filesystem, credential, HTTP and process-launching
// decisions outside the Win32 message loop.
type Controller interface {
	Snapshot(context.Context) (Snapshot, error)
	SelectModel(context.Context, string) error
	LoadSelected(context.Context) error
	SwitchSelected(context.Context) error
	UnloadActive(context.Context) error
	// OpenClaudeLocal shows the Claude Desktop instance that uses this
	// server's gateway, beside the signed-in one. It stops no Desktop process.
	OpenClaudeLocal(context.Context) error
	// StartServer starts the router and edge. It is idempotent: a running
	// server is left alone.
	StartServer(context.Context) error
	// StopServer ends the router, the edge and the monitor this tray started.
	StopServer(context.Context) error
	// OpenPanel shows the browser monitor, starting it when needed.
	OpenPanel(context.Context) error
}

// Options controls presentation only. Security-sensitive endpoints and paths
// belong to the controller configuration.
type Options struct {
	Environment     string
	InstanceID      string
	RefreshInterval time.Duration
}
