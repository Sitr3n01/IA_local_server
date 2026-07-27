package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/credential"
)

type Component string

const (
	Router Component = "router"
	Edge   Component = "edge"
)

// Config is the complete, non-secret launch contract for one supervised
// serving process. Secrets are read from Windows Credential Manager only after
// all paths and loopback addresses have been validated.
type Config struct {
	Component    Component
	Environment  string
	InstallRoot  string
	RouterConfig string
	RouterAddr   string
	DataAddr     string
	ControlAddr  string
	UpstreamURL  string
	ModelsConfig string
	ProcessLog   string
}

type commandSpec struct {
	Path string
	Args []string
	Env  []string
}

func (c Config) Validate() error {
	if c.Component != Router && c.Component != Edge {
		return errors.New("component must be router or edge")
	}
	if !filepath.IsAbs(strings.TrimSpace(c.InstallRoot)) {
		return errors.New("install root must be an absolute path")
	}
	root, err := filepath.Abs(strings.TrimSpace(c.InstallRoot))
	if err != nil {
		return fmt.Errorf("resolve install root: %w", err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return fmt.Errorf("install root is unavailable: %s", root)
	}
	if err := requireWithin(filepath.Join(root, "logs"), c.ProcessLog, "process log"); err != nil {
		return err
	}
	if err := validateLoopbackAddr("router address", c.RouterAddr); err != nil {
		return err
	}

	switch c.Component {
	case Router:
		if err := requireWithin(filepath.Join(root, "config"), c.RouterConfig, "router config"); err != nil {
			return err
		}
		if err := requireFile(c.RouterConfig, "router config"); err != nil {
			return err
		}
		return requireFile(filepath.Join(root, "bin", "llama-swap.exe"), "llama-swap executable")
	case Edge:
		if c.Environment != "canary" && c.Environment != "final" {
			return errors.New("edge environment must be canary or final")
		}
		if err := validateLoopbackAddr("data address", c.DataAddr); err != nil {
			return err
		}
		if err := validateLoopbackAddr("control address", c.ControlAddr); err != nil {
			return err
		}
		if c.DataAddr == c.ControlAddr {
			return errors.New("data and control addresses must be different")
		}
		upstream, err := url.Parse(c.UpstreamURL)
		if err != nil || upstream.Scheme != "http" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || (upstream.Path != "" && upstream.Path != "/") {
			return errors.New("upstream must be a plain loopback http URL")
		}
		if err := validateLoopbackAddr("upstream", upstream.Host); err != nil {
			return err
		}
		if err := requireWithin(filepath.Join(root, "config"), c.ModelsConfig, "model manifest"); err != nil {
			return err
		}
		if err := requireFile(c.ModelsConfig, "model manifest"); err != nil {
			return err
		}
		if err := requireFile(filepath.Join(root, "config", "models.schema.json"), "model manifest schema"); err != nil {
			return err
		}
		return requireFile(filepath.Join(root, "bin", "cia-edge.exe"), "edge executable")
	}
	return nil
}

func (c Config) buildSpec() (commandSpec, error) {
	if err := c.Validate(); err != nil {
		return commandSpec{}, err
	}
	root, _ := filepath.Abs(c.InstallRoot)
	environment := sanitizedEnvironment(os.Environ())

	switch c.Component {
	case Router:
		routerToken, err := credential.Read("router")
		if err != nil {
			return commandSpec{}, fmt.Errorf("read router credential: %w", err)
		}
		if err := writeRouterAPIKey(root, routerToken); err != nil {
			return commandSpec{}, err
		}
		environment = setEnvironment(environment, "CIA_ROUTER_TOKEN", routerToken)
		return commandSpec{
			Path: filepath.Join(root, "bin", "llama-swap.exe"),
			Args: []string{"--config", c.RouterConfig, "--listen", c.RouterAddr},
			Env:  environment,
		}, nil
	case Edge:
		inferenceToken, err := credential.Read("inference")
		if err != nil {
			return commandSpec{}, fmt.Errorf("read inference credential: %w", err)
		}
		adminToken, err := credential.Read("admin")
		if err != nil {
			return commandSpec{}, fmt.Errorf("read administrative credential: %w", err)
		}
		routerToken, err := credential.Read("router")
		if err != nil {
			return commandSpec{}, fmt.Errorf("read router credential: %w", err)
		}
		environment = setEnvironment(environment, "CIA_INFERENCE_TOKEN", inferenceToken)
		environment = setEnvironment(environment, "CIA_ADMIN_TOKEN", adminToken)
		environment = setEnvironment(environment, "CIA_ROUTER_TOKEN", routerToken)
		environment = setEnvironment(environment, "CIA_EDGE_LOG_PATH", filepath.Join(root, "logs", "cia-edge.jsonl"))
		return commandSpec{
			Path: filepath.Join(root, "bin", "cia-edge.exe"),
			Args: []string{
				"--environment", c.Environment,
				"--data-addr", c.DataAddr,
				"--control-addr", c.ControlAddr,
				"--upstream", c.UpstreamURL,
				"--models-config", c.ModelsConfig,
				"--models-schema", filepath.Join(root, "config", "models.schema.json"),
			},
			Env: environment,
		}, nil
	}
	return commandSpec{}, errors.New("unsupported component")
}

const (
	initialRestartDelay = time.Minute
	maximumRestartDelay = 15 * time.Minute
	stableRunThreshold  = 10 * time.Minute
)

// Run keeps the selected component inside an operating-system containment
// boundary. On Windows that boundary is a kill-on-close Job Object, so stopping
// the scheduled task cannot leave serving descendants. Unexpected exits use a
// bounded exponential backoff beginning at one minute.
func Run(ctx context.Context, cfg Config, stdout, stderr io.Writer) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	restartDelay := initialRestartDelay
	for {
		started := time.Now()
		spec, specErr := cfg.buildSpec()
		var runErr error
		if specErr != nil {
			runErr = specErr
		} else {
			runErr = runContained(ctx, spec, stdout, stderr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		runDuration := time.Since(started)
		if runDuration >= stableRunThreshold {
			restartDelay = initialRestartDelay
		}
		errorText := "child process exited"
		if runErr != nil {
			errorText = runErr.Error()
		}
		_ = json.NewEncoder(stderr).Encode(map[string]any{
			"time":                  time.Now().UTC().Format(time.RFC3339Nano),
			"service":               "cia-supervisor",
			"component":             cfg.Component,
			"event":                 "child_exited",
			"error":                 errorText,
			"restart_delay_seconds": int(restartDelay / time.Second),
		})

		timer := time.NewTimer(restartDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		if runDuration < stableRunThreshold && restartDelay < maximumRestartDelay {
			restartDelay *= 2
			if restartDelay > maximumRestartDelay {
				restartDelay = maximumRestartDelay
			}
		}
	}
}

func validateLoopbackAddr(label, address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || port == "" {
		return fmt.Errorf("%s must include an explicit port", label)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use a literal loopback IP address", label)
	}
	return nil
}

func requireFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is unavailable: %s", label, path)
	}
	return nil
}

func requireWithin(root, path, label string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve %s root: %w", label, err)
	}
	pathAbs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s must be an absolute path", label)
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s must remain under %s", label, rootAbs)
	}
	return nil
}

func sanitizedEnvironment(environment []string) []string {
	allowed := map[string]bool{
		"ALLUSERSPROFILE":         true,
		"APPDATA":                 true,
		"COMMONPROGRAMFILES":      true,
		"COMMONPROGRAMFILES(X86)": true,
		"COMSPEC":                 true,
		"HOMEDRIVE":               true,
		"HOMEPATH":                true,
		"LOCALAPPDATA":            true,
		"NUMBER_OF_PROCESSORS":    true,
		"OS":                      true,
		"PATH":                    true,
		"PATHEXT":                 true,
		"PROCESSOR_ARCHITECTURE":  true,
		"PROGRAMDATA":             true,
		"PROGRAMFILES":            true,
		"PROGRAMFILES(X86)":       true,
		"SYSTEMDRIVE":             true,
		"SYSTEMROOT":              true,
		"TEMP":                    true,
		"TMP":                     true,
		"USERDOMAIN":              true,
		"USERNAME":                true,
		"USERPROFILE":             true,
		"WINDIR":                  true,
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || !allowed[strings.ToUpper(name)] {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func writeRouterAPIKey(root, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("router credential is empty")
	}
	path := filepath.Join(root, "state", "router-api-key.txt")
	if err := requireWithin(filepath.Join(root, "state"), path, "router API key file"); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create protected router API key file: %w", err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write protected router API key file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush protected router API key file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close protected router API key file: %w", err)
	}
	return nil
}

func setEnvironment(environment []string, name, value string) []string {
	return append(environment, name+"="+value)
}
