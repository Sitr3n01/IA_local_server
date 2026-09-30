package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/sitr3n/local-ai-provider/internal/panel"
	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

var version = "dev"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cia-tray", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", `C:\IA\local-ai-v2\config\panel.canary.json`, "path to the generated panel configuration")
	diagnose := flags.Bool("diagnose", false, "print one sanitized status snapshot and exit")
	validateModel := flags.String("validate-model", "", "validate one registered model, persist the sanitized result, and exit")
	claudeMode := flags.String("claude-mode", "", "Claude Desktop mode to preview or apply: anthropic or local")
	openClaude := flags.Bool("claude-open", false, "open or foreground the installed Claude Desktop in its current mode")
	applyClaude := flags.Bool("apply", false, "apply the requested Claude Desktop mode; otherwise preview only")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	actions := 0
	for _, selected := range []bool{*diagnose, strings.TrimSpace(*validateModel) != "", strings.TrimSpace(*claudeMode) != "", *openClaude} {
		if selected {
			actions++
		}
	}
	if actions > 1 {
		return errors.New("diagnose, model validation, Claude mode, and Claude open actions are mutually exclusive")
	}
	if *applyClaude && strings.TrimSpace(*claudeMode) == "" {
		return errors.New("-apply requires -claude-mode")
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
	if *openClaude {
		output := struct {
			Action string `json:"action"`
			Result string `json:"result"`
			Error  string `json:"error,omitempty"`
		}{Action: "claude-open", Result: "opened"}
		operationErr := controller.LaunchClaudeDesktop(ctx)
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
			return fmt.Errorf("open Claude Desktop: %w", operationErr)
		}
		return nil
	}
	if rawMode := strings.ToLower(strings.TrimSpace(*claudeMode)); rawMode != "" {
		mode := trayui.ClaudeMode(rawMode)
		if mode != trayui.ClaudeModeAnthropic && mode != trayui.ClaudeModeLocal {
			return fmt.Errorf("unsupported Claude Desktop mode %q", rawMode)
		}
		snapshot, snapshotErr := controller.Snapshot(ctx)
		output := struct {
			Mode      trayui.ClaudeMode `json:"mode"`
			Apply     bool              `json:"apply"`
			Available bool              `json:"available"`
			GatewayOK bool              `json:"gateway_ok"`
			Detail    string            `json:"detail,omitempty"`
			Result    string            `json:"result"`
			Error     string            `json:"error,omitempty"`
		}{Mode: mode, Apply: *applyClaude, Available: snapshot.ClaudeAvailable, GatewayOK: snapshot.ClaudeGatewayOK, Detail: snapshot.ClaudeDetail, Result: "preview"}
		if snapshotErr != nil {
			output.Error = sanitizeDiagnosticError(snapshotErr)
		}
		var operationErr error
		if *applyClaude {
			if err := controller.SetClaudeMode(ctx, mode); err != nil {
				output.Result = "failed"
				output.Error = sanitizeDiagnosticError(err)
				operationErr = err
			} else {
				output.Result = "applied"
			}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
		if operationErr != nil {
			return fmt.Errorf("apply Claude Desktop mode %s: %w", mode, operationErr)
		}
		return nil
	}
	if modelID := strings.TrimSpace(*validateModel); modelID != "" {
		result := struct {
			Model  string `json:"model"`
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		}{Model: modelID, Status: "validated"}
		if err := controller.ValidateModel(ctx, modelID); err != nil {
			result.Status = "failed"
			result.Error = sanitizeDiagnosticError(err)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	return trayui.Run(ctx, controller, trayui.Options{
		Title:           "CIA Local AI — " + strings.ToUpper(string(config.Environment)),
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
		case "-diagnose", "--diagnose", "-validate-model", "--validate-model", "-claude-mode", "--claude-mode", "-claude-open", "--claude-open":
			return true
		}
	}
	return false
}
