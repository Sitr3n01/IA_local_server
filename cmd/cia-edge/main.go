package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/edge"
	"github.com/sitr3n/local-ai-provider/internal/manifestvalidator"
	"github.com/sitr3n/local-ai-provider/internal/rotatelog"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
			"service": "cia-edge",
			"event":   "fatal",
			"error":   err.Error(),
		})
		os.Exit(1)
	}
}

func run() error {
	cfg, err := edge.ConfigFromEnv()
	if err != nil {
		return err
	}
	if cfg.Version == "dev" {
		cfg.Version = version
	}
	if logPath := strings.TrimSpace(os.Getenv("CIA_EDGE_LOG_PATH")); logPath != "" {
		writer, err := rotatelog.Open(logPath, 10<<20, 7, 14*24*time.Hour)
		if err != nil {
			return err
		}
		cfg.LogOutput = writer
		defer writer.Close()
	}

	flags := flag.NewFlagSet("cia-edge", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&cfg.DataAddr, "data-addr", cfg.DataAddr, "loopback address for the OpenAI-compatible data plane")
	flags.StringVar(&cfg.ControlAddr, "control-addr", cfg.ControlAddr, "loopback address for health, status, and metrics")
	flags.StringVar(&cfg.UpstreamURL, "upstream", cfg.UpstreamURL, "fixed loopback llama-swap URL")
	modelsConfig := flags.String("models-config", "", "path to the generated models YAML manifest (required)")
	modelsSchema := flags.String("models-schema", "", "path to the versioned model-manifest JSON Schema (required)")
	environment := flags.String("environment", "canary", "deployment environment: canary or final")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *modelsConfig == "" || *modelsSchema == "" {
		return fmt.Errorf("--models-config and --models-schema are required")
	}
	if err := manifestvalidator.ValidateFiles(*modelsSchema, *modelsConfig); err != nil {
		return fmt.Errorf("validate models config: %w", err)
	}
	models, publicModel, err := edge.LoadModels(*modelsConfig, *environment)
	if err != nil {
		return err
	}
	cfg.Models = models
	cfg.PublicModelID = publicModel
	if err := cfg.Validate(); err != nil {
		return err
	}

	server, err := edge.New(cfg)
	if err != nil {
		return err
	}
	_ = json.NewEncoder(cfg.LogOutput).Encode(map[string]any{
		"time":         time.Now().UTC().Format(time.RFC3339Nano),
		"service":      "cia-edge",
		"event":        "starting",
		"data_addr":    cfg.DataAddr,
		"control_addr": cfg.ControlAddr,
		"upstream":     cfg.UpstreamURL,
		"model_count":  len(cfg.Models),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}
