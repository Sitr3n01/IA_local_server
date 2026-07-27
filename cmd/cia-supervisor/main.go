package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/rotatelog"
	"github.com/sitr3n/local-ai-provider/internal/supervisor"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(os.Stderr, "cia-supervisor: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("cia-supervisor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	component := flags.String("component", "", "component to supervise: router or edge")
	environment := flags.String("environment", "canary", "deployment environment: canary or final")
	installRoot := flags.String("install-root", `C:\IA\local-ai-v2`, "v2 installation root")
	routerConfig := flags.String("router-config", "", "generated llama-swap configuration")
	routerAddr := flags.String("router-addr", "127.0.0.1:9292", "router loopback address")
	dataAddr := flags.String("data-addr", "127.0.0.1:8090", "edge data-plane loopback address")
	controlAddr := flags.String("control-addr", "127.0.0.1:8091", "edge control-plane loopback address")
	upstreamURL := flags.String("upstream", "http://127.0.0.1:9292", "router loopback URL")
	modelsConfig := flags.String("models-config", "", "versioned model manifest")
	processLog := flags.String("process-log", "", "rotated child process log under the install root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	cfg := supervisor.Config{
		Component:    supervisor.Component(*component),
		Environment:  *environment,
		InstallRoot:  *installRoot,
		RouterConfig: *routerConfig,
		RouterAddr:   *routerAddr,
		DataAddr:     *dataAddr,
		ControlAddr:  *controlAddr,
		UpstreamURL:  *upstreamURL,
		ModelsConfig: *modelsConfig,
		ProcessLog:   *processLog,
	}
	if cfg.ProcessLog == "" {
		cfg.ProcessLog = filepath.Join(cfg.InstallRoot, "logs", "cia-"+string(cfg.Component)+"-process.log")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logWriter, err := rotatelog.Open(cfg.ProcessLog, 10<<20, 7, 14*24*time.Hour)
	if err != nil {
		return fmt.Errorf("open process log: %w", err)
	}
	defer logWriter.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return supervisor.Run(ctx, cfg, logWriter, logWriter)
}
