package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Sitr3n01/local-ai-provider/internal/claudedesktop"
	"github.com/Sitr3n01/local-ai-provider/internal/credential"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cia-credential:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "get":
		if len(args) != 2 {
			return usageError()
		}
		value, err := credential.Read(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, value)
		return err
	case "set":
		if len(args) != 2 {
			return usageError()
		}
		value, err := bufio.NewReader(io.LimitReader(stdin, 4097)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		value = strings.TrimSpace(value)
		if len(value) < 32 || len(value) > 4096 {
			return errors.New("credential must contain 32 to 4096 characters")
		}
		return credential.Write(args[1], value)
	case "init":
		if len(args) != 1 {
			return usageError()
		}
		for _, name := range []string{"inference", "admin", "router", "claude-gateway"} {
			if _, err := credential.Read(name); err == nil {
				continue
			} else if !errors.Is(err, credential.ErrNotFound) {
				return err
			}
			value, err := randomToken()
			if err != nil {
				return err
			}
			if err := credential.Write(name, value); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(stdout, "Credentials initialized in Windows Credential Manager.")
		return err
	case "delete":
		if len(args) != 2 {
			return usageError()
		}
		return credential.Delete(args[1])
	case "run-opencode":
		commandArgs := args[1:]
		if len(commandArgs) > 0 && commandArgs[0] == "--" {
			commandArgs = commandArgs[1:]
		}
		return runOpenCode(commandArgs, stdin, stdout, stderr)
	case "run-edge":
		commandArgs := args[1:]
		if len(commandArgs) > 0 && commandArgs[0] == "--" {
			commandArgs = commandArgs[1:]
		}
		return runEdge(commandArgs, stdin, stdout, stderr)
	case "probe-claude":
		if len(args) < 2 || len(args) > 3 {
			return usageError()
		}
		baseURL := "http://127.0.0.1:18090"
		if len(args) == 3 {
			baseURL = args[2]
		}
		value, err := credential.Read("claude-gateway")
		if err != nil {
			return err
		}
		result, err := claudedesktop.ProbeModel(context.Background(), claudedesktop.Gateway{BaseURL: baseURL, APIKey: value}, args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	default:
		return usageError()
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cia_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func runOpenCode(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	value, err := credential.Read("inference")
	if err != nil {
		return err
	}
	program := "opencode"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		program = args[0]
		args = args[1:]
	}
	cmd := exec.Command(program, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(openCodeEnvironment(os.Environ()), "CIA_LOCAL_API_KEY="+value)
	return cmd.Run()
}

// runEdge is the sole secret handoff for the installed data-plane executable.
// It keeps every credential out of PowerShell variables, command-line
// arguments, generated launcher files, and process logs.
func runEdge(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("run-edge requires cia-edge arguments after --")
	}
	credentials := make(map[string]string, 4)
	for _, name := range []string{"inference", "admin", "router", "claude-gateway"} {
		value, err := credential.Read(name)
		if err != nil {
			return fmt.Errorf("read %s credential: %w", name, err)
		}
		credentials[name] = value
	}
	credentialExecutable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate installed credential helper: %w", err)
	}
	edgeExecutable, edgeLogPath, err := installedEdgePaths(credentialExecutable)
	if err != nil {
		return err
	}
	command := exec.Command(edgeExecutable, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = edgeEnvironment(os.Environ(), credentials, edgeLogPath)
	return command.Run()
}

func installedEdgePaths(credentialExecutable string) (string, string, error) {
	if strings.TrimSpace(credentialExecutable) != credentialExecutable || !filepath.IsAbs(credentialExecutable) {
		return "", "", errors.New("installed credential helper path must be absolute")
	}
	binDir := filepath.Dir(filepath.Clean(credentialExecutable))
	installRoot := filepath.Dir(binDir)
	if !strings.EqualFold(filepath.Base(binDir), "bin") || installRoot == binDir {
		return "", "", errors.New("installed credential helper must run from the installation bin directory")
	}
	return filepath.Join(binDir, "cia-edge.exe"), filepath.Join(installRoot, "logs", "cia-edge.jsonl"), nil
}

func openCodeEnvironment(environment []string) []string {
	allowed := map[string]bool{
		"ALLUSERSPROFILE":         true,
		"APPDATA":                 true,
		"COLORTERM":               true,
		"COMMONPROGRAMFILES":      true,
		"COMMONPROGRAMFILES(X86)": true,
		"COMSPEC":                 true,
		"FORCE_COLOR":             true,
		"HOMEDRIVE":               true,
		"HOMEPATH":                true,
		"LOCALAPPDATA":            true,
		"NO_COLOR":                true,
		"NUMBER_OF_PROCESSORS":    true,
		"OPENCODE_CONFIG":         true,
		"OPENCODE_CONFIG_CONTENT": true,
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
		"TERM":                    true,
		"TMP":                     true,
		"USERDOMAIN":              true,
		"USERNAME":                true,
		"USERPROFILE":             true,
		"WINDIR":                  true,
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && allowed[strings.ToUpper(name)] {
			result = append(result, entry)
		}
	}
	return result
}

func edgeEnvironment(environment []string, credentials map[string]string, edgeLogPath string) []string {
	result := make([]string, 0, len(environment)+5)
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && !strings.HasPrefix(strings.ToUpper(name), "CIA_") {
			result = append(result, entry)
		}
	}
	return append(result,
		"CIA_INFERENCE_TOKEN="+credentials["inference"],
		"CIA_ADMIN_TOKEN="+credentials["admin"],
		"CIA_ROUTER_TOKEN="+credentials["router"],
		"CIA_CLAUDE_GATEWAY_TOKEN="+credentials["claude-gateway"],
		"CIA_EDGE_LOG_PATH="+edgeLogPath,
	)
}

func usageError() error {
	return errors.New("usage: cia-credential <get NAME|set NAME|init|delete NAME|probe-claude MODEL [BASE_URL]|run-opencode [--] [COMMAND] [ARGS...]|run-edge -- [CIA-EDGE-ARGS...]>; NAME is inference, admin, router, or claude-gateway")
}
