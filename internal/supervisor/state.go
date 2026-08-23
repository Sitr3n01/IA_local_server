package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stateSchemaVersion is the on-disk contract for the supervisor state file.
const stateSchemaVersion = 1

// State is the sanitized restart record for one supervised component. It exists
// so a restart loop is diagnosable without adding a listener to the supervisor:
// the file is metadata only — counters, timestamps, and the child's own exit
// text. No prompt, header, body, or credential can reach it.
type State struct {
	SchemaVersion  int    `json:"schema_version"`
	Service        string `json:"service"`
	Component      string `json:"component"`
	Environment    string `json:"environment,omitempty"`
	State          string `json:"state"`
	SupervisorPID  int    `json:"supervisor_pid"`
	StartedUTC     string `json:"started_utc"`
	UpdatedUTC     string `json:"updated_utc"`
	RestartCount   uint64 `json:"restart_count"`
	UnstableExits  uint64 `json:"consecutive_unstable_exits"`
	BackoffSeconds int    `json:"current_backoff_seconds"`
	LastExit       string `json:"last_exit,omitempty"`
	LastExitUTC    string `json:"last_exit_utc,omitempty"`
	LastRunSeconds int64  `json:"last_run_seconds"`
}

// maxLastExitBytes bounds the retained exit text so a pathological child cannot
// grow the state file without limit.
const maxLastExitBytes = 512

// stateWriter publishes the restart record atomically into the writable state
// directory. A failure to write it never stops supervision: the state file is
// diagnostics, and losing diagnostics must not take the provider down.
type stateWriter struct {
	path      string
	component string
	env       string
	started   time.Time
}

func newStateWriter(root string, cfg Config, started time.Time) *stateWriter {
	path := filepath.Join(root, "state", "supervisor-"+string(cfg.Component)+".json")
	if err := requireWithin(filepath.Join(root, "state"), path, "supervisor state file"); err != nil {
		return nil
	}
	return &stateWriter{
		path:      path,
		component: string(cfg.Component),
		env:       cfg.Environment,
		started:   started,
	}
}

func (w *stateWriter) write(state State) {
	if w == nil {
		return
	}
	state.SchemaVersion = stateSchemaVersion
	state.Service = "cia-supervisor"
	state.Component = w.component
	state.Environment = w.env
	state.SupervisorPID = os.Getpid()
	state.StartedUTC = w.started.UTC().Format(time.RFC3339)
	state.UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	if len(state.LastExit) > maxLastExitBytes {
		state.LastExit = state.LastExit[:maxLastExitBytes]
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	_ = atomicWrite(w.path, payload)
}

func atomicWrite(path string, payload []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)

	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish supervisor state: %w", err)
	}
	return nil
}
