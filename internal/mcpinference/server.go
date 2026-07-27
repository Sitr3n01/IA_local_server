package mcpinference

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolDescription = "Delegate one stateless text task to the pinned local model. Use this tool only when the user explicitly asks you to use, consult, or delegate to the local model or local AI server; never invoke it merely because local AI is available. The pinned model is a small (9B) executor, best suited to one well-specified, self-contained subtask at a time (a single function, a focused explanation, a review of a short snippet). It has no file, tool, or execution access and cannot verify what it writes. When asked to satisfy several interacting requirements in one call (for example: concurrency plus multiple features plus tests), it reliably produces plausible-looking code with real correctness bugs (race conditions, inverted comparisons, unhandled cases, unimplemented features). Prefer several narrow calls over one broad one, and verify or execute the result yourself before relying on it."

// New creates a text-only inference MCP server. It exposes no prompts,
// resources, files, history, administrative operations, model selection, or
// tools for the delegated model.
func New(client *Client, version string) *mcp.Server {
	if client == nil {
		panic("mcpinference: nil client")
	}
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "cia-local-ai-inference", Version: version},
		&mcp.ServerOptions{
			Instructions: "This server delegates a single stateless text task to a pinned local model. Invoke local_ai_delegate only when the user explicitly requests use of the local model or local AI server. It has no files, tools, memory, administrative access, or model-selection capability.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)

	falseValue := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "local_ai_delegate",
		Description: toolDescription,
		Title:       "Delegate to local AI",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Delegate to local AI",
			ReadOnlyHint:    true,
			IdempotentHint:  false,
			DestructiveHint: &falseValue,
			OpenWorldHint:   &falseValue,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DelegateInput) (*mcp.CallToolResult, DelegateOutput, error) {
		output, err := client.Delegate(ctx, input)
		return nil, output, err
	})

	return server
}

// Run serves one MCP stdio session until the harness disconnects or ctx is
// canceled.
func Run(ctx context.Context, cfg Config, version string) error {
	client, err := NewClient(cfg, version)
	if err != nil {
		return err
	}
	return New(client, version).Run(ctx, &mcp.StdioTransport{})
}
