package panel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client is an operator-facing harness with an approved launcher.
type Client string

const (
	ClientCodex    Client = "codex"
	ClientOpenCode Client = "opencode"
)

// CommandSpec is an inspectable, shell-free launch contract. Args are passed
// directly to PowerShell and never concatenated into a command string.
type CommandSpec struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

// Launcher validates catalog compatibility before starting any child process.
type Launcher struct {
	config  Config
	catalog *Catalog
}

func NewLauncher(config Config, catalog *Catalog) (*Launcher, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if catalog == nil || len(catalog.models) == 0 {
		return nil, errors.New("launcher requires a non-empty catalog")
	}
	if catalog.Environment != config.Environment {
		return nil, fmt.Errorf("catalog environment %q does not match panel environment %q", catalog.Environment, config.Environment)
	}
	return &Launcher{config: config, catalog: catalog}, nil
}

// Spec builds a sanitized PowerShell invocation for an available, compatible
// catalog model. It is useful for UI validation and does not start a process.
func (l *Launcher) Spec(client Client, modelID string) (CommandSpec, error) {
	model, found := l.catalog.Model(modelID)
	if !found {
		return CommandSpec{}, fmt.Errorf("model %q is not in the catalog", modelID)
	}
	if !model.Available {
		return CommandSpec{}, fmt.Errorf("model %q is unavailable: %s", modelID, model.UnavailableReason)
	}

	var script string
	var compatible bool
	switch client {
	case ClientCodex:
		script = l.config.Launchers.Codex
		compatible = model.CanLaunchCodex()
	case ClientOpenCode:
		script = l.config.Launchers.OpenCode
		compatible = model.CanLaunchOpenCode()
	default:
		return CommandSpec{}, fmt.Errorf("unsupported panel client %q", client)
	}
	if !compatible {
		return CommandSpec{}, fmt.Errorf("model %q does not satisfy the %s capability contract", modelID, client)
	}
	powerShell, err := powerShellExecutable()
	if err != nil {
		return CommandSpec{}, fmt.Errorf("resolve system PowerShell: %w", err)
	}
	args := []string{
		"-NoLogo",
		"-NoProfile",
		"-ExecutionPolicy",
		"RemoteSigned",
	}
	if client == ClientCodex {
		// Codex runs in a real, visible console (applyLaunchAttributes); keep
		// it open after the script exits so an early throw, or the end of an
		// interactive session, stays readable instead of the window vanishing.
		args = append(args, "-NoExit")
	}
	args = append(args, "-File", script, "-Model", model.ID)
	return CommandSpec{
		Path: powerShell,
		Args: args,
		Env:  SanitizeEnvironment(os.Environ()),
		Dir:  filepath.Dir(script),
	}, nil
}

// Launch starts a detached launcher process using the platform-specific window
// policy. It returns after process creation; the harness owns its own lifetime.
func (l *Launcher) Launch(client Client, modelID string) error {
	spec, err := l.Spec(client, modelID)
	if err != nil {
		return err
	}
	info, err := os.Stat(spec.Args[len(spec.Args)-3])
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s launcher script is unavailable", client)
	}
	command := exec.Command(spec.Path, spec.Args...)
	command.Env = append([]string(nil), spec.Env...)
	command.Dir = spec.Dir
	applyLaunchAttributes(command, client)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start %s launcher: %w", client, err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release %s launcher process: %w", client, err)
	}
	return nil
}

// SanitizeEnvironment applies a case-insensitive allowlist and deliberately
// excludes credentials, proxy settings, provider overrides, and GPU selectors.
func SanitizeEnvironment(environment []string) []string {
	allowed := map[string]struct{}{
		"ALLUSERSPROFILE":         {},
		"APPDATA":                 {},
		"CODEX_HOME":              {},
		"COMMONPROGRAMFILES":      {},
		"COMMONPROGRAMFILES(X86)": {},
		"COMSPEC":                 {},
		"HOME":                    {},
		"HOMEDRIVE":               {},
		"HOMEPATH":                {},
		"LANG":                    {},
		"LC_ALL":                  {},
		"LOCALAPPDATA":            {},
		"NUMBER_OF_PROCESSORS":    {},
		"OS":                      {},
		"PATH":                    {},
		"PATHEXT":                 {},
		"PROCESSOR_ARCHITECTURE":  {},
		"PROGRAMDATA":             {},
		"PROGRAMFILES":            {},
		"PROGRAMFILES(X86)":       {},
		"SYSTEMDRIVE":             {},
		"SYSTEMROOT":              {},
		"TEMP":                    {},
		"TERM":                    {},
		"TMP":                     {},
		"USERDOMAIN":              {},
		"USERNAME":                {},
		"USERPROFILE":             {},
		"WINDIR":                  {},
		"XDG_CACHE_HOME":          {},
		"XDG_CONFIG_HOME":         {},
		"XDG_DATA_HOME":           {},
	}
	result := make([]string, 0, len(environment))
	positions := make(map[string]int, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		canonical := strings.ToUpper(name)
		if !found || name == "" {
			continue
		}
		if _, ok := allowed[canonical]; !ok {
			continue
		}
		if index, duplicate := positions[canonical]; duplicate {
			result[index] = entry
			continue
		}
		positions[canonical] = len(result)
		result = append(result, entry)
	}
	return result
}
