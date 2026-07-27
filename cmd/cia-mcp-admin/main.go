package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/sitr3n/local-ai-provider/internal/credential"
	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	shared, err := mcpserver.ConfigFromEnv()
	if err == nil {
		err = mcpadmin.Run(ctx, mcpadmin.Config{
			ControlURL: shared.ControlURL,
			Timeout:    shared.Timeout,
			HTTPClient: shared.HTTPClient,
			TokenProvider: mcpadmin.TokenProviderFunc(func(context.Context) (string, error) {
				return credential.Read("admin")
			}),
		}, version)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		// stdout is reserved exclusively for the MCP stdio transport.
		_, _ = fmt.Fprintf(os.Stderr, "cia-mcp-admin: %v\n", err)
		os.Exit(1)
	}
}
