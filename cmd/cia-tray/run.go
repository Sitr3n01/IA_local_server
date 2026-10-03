package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sitr3n/local-ai-provider/internal/panel"
	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

var version = "dev"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cia-tray", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", `C:\IA\local-ai-v2\config\panel.canary.json`, "path to the generated panel configuration")
	diagnose := flags.Bool("diagnose", false, "print one sanitized status snapshot and exit")
	openClaude := flags.Bool("claude-open", false, "open or foreground the signed-in Claude Desktop instance")
	openClaudeLocal := flags.Bool("claude-local", false, "open or foreground the Claude Desktop instance that uses this server, beside the signed-in one")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	actions := 0
	for _, selected := range []bool{*diagnose, *openClaude, *openClaudeLocal} {
		if selected {
			actions++
		}
	}
	if actions > 1 {
		return errors.New("diagnose, Claude open, and Claude local actions are mutually exclusive")
	}
	config, err := panel.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	controller, err := newAppController(config, version)
	if err != nil {
		return err
	}
	if *diagnose {
		snapshot, snapshotErr := controller.Snapshot(ctx)
		output := struct {
			Version  string          `json:"version"`
			Snapshot trayui.Snapshot `json:"snapshot"`
			Error    string          `json:"error,omitempty"`
		}{Version: version, Snapshot: snapshot}
		if snapshotErr != nil {
			output.Error = sanitizeDiagnosticError(snapshotErr)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
		// Diagnostics carry degraded state in the JSON payload. Returning the
		// snapshot error here would make the Windows GUI entry point display a
		// modal MessageBox and block unattended health collection.
		return nil
	}
	if *openClaude || *openClaudeLocal {
		output := struct {
			Action string `json:"action"`
			Result string `json:"result"`
			Error  string `json:"error,omitempty"`
		}{Action: "claude-open", Result: "opened"}
		open := controller.LaunchClaudeDesktop
		if *openClaudeLocal {
			output.Action, open = "claude-local", controller.OpenClaudeLocal
		}
		operationErr := open(ctx)
		if operationErr != nil {
			output.Result = "failed"
			output.Error = sanitizeDiagnosticError(operationErr)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
		if operationErr != nil {
			return fmt.Errorf("%s: %w", output.Action, operationErr)
		}
		return nil
	}
	return trayui.Run(ctx, controller, trayui.Options{
		Environment:     string(config.Environment),
		InstanceID:      string(config.Environment),
		RefreshInterval: config.RefreshInterval(),
	})
}

func sanitizeDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}

func headlessInvocation(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-diagnose", "--diagnose", "-claude-open", "--claude-open", "-claude-local", "--claude-local":
			return true
		}
	}
	return false
}
