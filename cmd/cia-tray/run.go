package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
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

func defaultStreams() (io.Writer, io.Writer) { return os.Stdout, os.Stderr }
