package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/sitr3n/local-ai-provider/internal/credential"
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
		for _, name := range []string{"inference", "admin", "router"} {
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

func usageError() error {
	return errors.New("usage: cia-credential <get NAME|set NAME|init|delete NAME|run-opencode [--] [COMMAND] [ARGS...]>; NAME is inference, admin, or router")
}
