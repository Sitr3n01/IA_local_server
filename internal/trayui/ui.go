package trayui

import (
	"context"
	"errors"
	"time"
)

// ErrAlreadyRunning lets launchers remain idempotent when the panel for the
// same environment is already present in the notification area.
var ErrAlreadyRunning = errors.New("tray is already running")

// Client identifies one explicitly local harness that can be launched from
// the notification-area controller.
type Client string

const (
	ClientCodex    Client = "codex"
	ClientOpenCode Client = "opencode"
)

// Model is the operator-facing projection of one manifest entry. Available
// means the model is allowed in this deployment; capability flags decide which
// launch actions the menu may offer.
type Model struct {
	ID             string
	DisplayName    string
	State          string
	Available      bool
	Reason         string
	Codex          bool
	OpenCode       bool
	ArtifactPath   string
	ArtifactBytes  int64
	ArtifactSHA256 string
	Runtime        string
	ContextTokens  int
	GPULayers      int
	Quantization   string
	Capabilities   string
	Discovered     bool
	Validation     string
}

type Event struct {
	Time       string
	Method     string
	Path       string
	Status     int
	DurationMS int64
}

// Snapshot is a side-effect-free view of provider and operator state.
type Snapshot struct {
	Environment     string
	ProviderReady   bool
	UpstreamReady   bool
	StatusAvailable bool
	SelectedModel   string
	ActiveModel     string
	Active          int
	Queued          int
	MaxActive       int
	MaxQueue        int
	CapacityOK      bool
	CapacityNote    string
	Models          []Model
	ModelRoots      []string
	RecentEvents    []Event
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
	Launch(context.Context, Client, string) error
	AddModelRoot(context.Context, string) error
	RemoveModelRoot(context.Context, string) error
	ValidateModel(context.Context, string) error
}

// Options controls presentation only. Security-sensitive endpoints and paths
// belong to the controller configuration.
type Options struct {
	Title           string
	InstanceID      string
	RefreshInterval time.Duration
}
