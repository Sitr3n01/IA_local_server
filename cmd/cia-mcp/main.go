package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg, err := mcpserver.ConfigFromEnv()
	if err == nil {
		err = mcpserver.Run(ctx, cfg, version)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		// stdout is reserved exclusively for the MCP stdio transport.
		_, _ = fmt.Fprintf(os.Stderr, "cia-mcp: %v\n", err)
		os.Exit(1)
	}
}
