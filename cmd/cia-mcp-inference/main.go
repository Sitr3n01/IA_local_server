package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/sitr3n/local-ai-provider/internal/credential"
	"github.com/sitr3n/local-ai-provider/internal/mcpinference"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg, err := mcpinference.ConfigFromEnv()
	if err == nil {
		cfg.TokenProvider = mcpinference.TokenProviderFunc(func(context.Context) (string, error) {
			return credential.Read("inference")
		})
		err = mcpinference.Run(ctx, cfg, version)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		// stdout is reserved exclusively for the MCP stdio transport. Startup
		// errors contain no prompt, response, header, or credential data.
		_, _ = fmt.Fprintf(os.Stderr, "cia-mcp-inference: %v\n", err)
		os.Exit(1)
	}
}
