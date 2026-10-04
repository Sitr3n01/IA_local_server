package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/Sitr3n01/local-ai-provider/internal/credential"
	"github.com/Sitr3n01/local-ai-provider/internal/mcpadmin"
	"github.com/Sitr3n01/local-ai-provider/internal/mcpserver"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	shared, err := mcpserver.ConfigFromEnv()
	if err == nil {
		// Mutations prefer the DACL-protected pipe. The bearer token is still
		// obtained lazily, and only if the pipe is absent entirely.
		adminPipe, adminPipeServer := mcpadmin.AdminTransportFromEnv(shared.ControlURL)
		err = mcpadmin.Run(ctx, mcpadmin.Config{
			ControlURL:      shared.ControlURL,
			Timeout:         shared.Timeout,
			HTTPClient:      shared.HTTPClient,
			AdminPipe:       adminPipe,
			AdminPipeServer: adminPipeServer,
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
