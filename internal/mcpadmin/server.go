package mcpadmin

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ModelInput selects the exact stable model ID for an administrative action.
type ModelInput struct {
	ModelID string `json:"model_id" jsonschema:"stable local model ID; required"`
}

// New creates a separate administrative MCP server. Harness configurations
// must not register this server implicitly.
func New(client *Client, version string) *mcp.Server {
	if client == nil {
		panic("mcpadmin: nil control client")
	}
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "cia-local-ai-admin", Version: version},
		&mcp.ServerOptions{
			Instructions: "Explicit administrative model lifecycle operations for the local CIA AI provider. Each call changes provider state and requires operator approval.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)

	mcp.AddTool(server, administrativeTool(
		"local_ai_load_model",
		"Load an explicitly selected local model. This changes provider state but is non-destructive and idempotent.",
		"Load local AI model",
		false,
	), func(ctx context.Context, _ *mcp.CallToolRequest, input ModelInput) (*mcp.CallToolResult, OperationOutput, error) {
		if strings.TrimSpace(input.ModelID) == "" {
			return nil, OperationOutput{}, errors.New("model_id is required")
		}
		output, err := client.Load(ctx, input.ModelID)
		return nil, output, err
	})

	mcp.AddTool(server, administrativeTool(
		"local_ai_unload_model",
		"Unload an explicitly selected local model. This destructive state change is idempotent.",
		"Unload local AI model",
		true,
	), func(ctx context.Context, _ *mcp.CallToolRequest, input ModelInput) (*mcp.CallToolResult, OperationOutput, error) {
		if strings.TrimSpace(input.ModelID) == "" {
			return nil, OperationOutput{}, errors.New("model_id is required")
		}
		output, err := client.Unload(ctx, input.ModelID)
		return nil, output, err
	})

	mcp.AddTool(server, administrativeTool(
		"local_ai_switch_model",
		"Switch explicitly to a selected local model. This may unload another model and is idempotent.",
		"Switch local AI model",
		true,
	), func(ctx context.Context, _ *mcp.CallToolRequest, input ModelInput) (*mcp.CallToolResult, OperationOutput, error) {
		if strings.TrimSpace(input.ModelID) == "" {
			return nil, OperationOutput{}, errors.New("model_id is required")
		}
		output, err := client.Switch(ctx, input.ModelID)
		return nil, output, err
	})

	return server
}

func administrativeTool(name, description, title string, destructive bool) *mcp.Tool {
	falseValue := false
	destructiveValue := destructive
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Title:       title,
		Annotations: &mcp.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			DestructiveHint: &destructiveValue,
			OpenWorldHint:   &falseValue,
		},
	}
}

// Run serves one administrative MCP stdio session.
func Run(ctx context.Context, cfg Config, version string) error {
	client, err := NewClient(cfg, version)
	if err != nil {
		return err
	}
	return New(client, version).Run(ctx, &mcp.StdioTransport{})
}
