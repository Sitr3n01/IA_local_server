package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultRecentEventsLimit = 20
	maxRecentEventsLimit     = 100
)

type emptyInput struct{}

// HealthOutput is the structured result of local_ai_health.
type HealthOutput struct {
	Healthy bool  `json:"healthy"`
	Live    Probe `json:"live"`
	Ready   Probe `json:"ready"`
}

// ModelsOutput is the structured result of local_ai_models.
type ModelsOutput struct {
	Count  int     `json:"count"`
	Models []Model `json:"models"`
}

// ActiveModelOutput is the structured result of local_ai_active_model.
type ActiveModelOutput struct {
	Active  bool   `json:"active"`
	ModelID string `json:"model_id"`
}

// CapacityOutput is the structured result of local_ai_capacity.
type CapacityOutput struct {
	Ready              bool   `json:"ready"`
	Admission          string `json:"admission"`
	Available          bool   `json:"available"`
	Active             int    `json:"active"`
	Queued             int    `json:"queued"`
	MaxActive          int    `json:"max_active"`
	MaxQueue           int    `json:"max_queue"`
	WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
}

// RecentEventsInput limits the number of newest events returned.
type RecentEventsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of newest sanitized events to return (default 20, maximum 100)"`
}

// RecentEventsOutput is the structured result of local_ai_recent_events.
type RecentEventsOutput struct {
	Count  int           `json:"count"`
	Events []RecentEvent `json:"events"`
}

// New creates a strictly read-only MCP server. It exposes no prompts,
// resources, chat/session state, or administrative tools.
func New(client *ControlClient, version string) *mcp.Server {
	if client == nil {
		panic("mcpserver: nil control client")
	}
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "cia-local-ai", Version: version},
		&mcp.ServerOptions{
			Instructions: "Read-only operational visibility for the local CIA AI provider. Tools never load models or mutate provider state.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)

	mcp.AddTool(server, readOnlyTool(
		"local_ai_health",
		"Read side-effect-free liveness and readiness for the local AI provider.",
		"Local AI health",
	), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, HealthOutput, error) {
		live, err := client.Liveness(ctx)
		if err != nil {
			return nil, HealthOutput{}, err
		}
		ready, err := client.Readiness(ctx)
		if err != nil {
			return nil, HealthOutput{}, err
		}
		healthy := live.Status == "ok" && ready.Status == "ready"
		if ready.UpstreamReachable != nil {
			healthy = healthy && *ready.UpstreamReachable
		}
		return nil, HealthOutput{
			Healthy: healthy,
			Live:    live,
			Ready:   ready,
		}, nil
	})

	mcp.AddTool(server, readOnlyTool(
		"local_ai_models",
		"List the stable local model IDs published by the provider without loading a model.",
		"Local AI models",
	), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, ModelsOutput, error) {
		status, err := client.Status(ctx)
		if err != nil {
			return nil, ModelsOutput{}, err
		}
		return nil, ModelsOutput{Count: len(status.Models), Models: status.Models}, nil
	})

	mcp.AddTool(server, readOnlyTool(
		"local_ai_active_model",
		"Report the active local model, if the router exposes one, without changing model state.",
		"Active local AI model",
	), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, ActiveModelOutput, error) {
		status, err := client.Status(ctx)
		if err != nil {
			return nil, ActiveModelOutput{}, err
		}
		modelID := strings.TrimSpace(status.ActiveModel)
		return nil, ActiveModelOutput{Active: modelID != "", ModelID: modelID}, nil
	})

	mcp.AddTool(server, readOnlyTool(
		"local_ai_capacity",
		"Read admission capacity and queue occupancy for the configured local model profile.",
		"Local AI capacity",
	), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, CapacityOutput, error) {
		status, err := client.Status(ctx)
		if err != nil {
			return nil, CapacityOutput{}, err
		}
		return nil, CapacityOutput{
			Ready:              status.Ready,
			Admission:          status.Capacity.Admission,
			Available:          status.Capacity.Available,
			Active:             status.Gate.Active,
			Queued:             status.Gate.Queued,
			MaxActive:          status.Gate.MaxActive,
			MaxQueue:           status.Gate.MaxQueue,
			WaitTimeoutSeconds: status.Gate.WaitTimeoutSeconds,
		}, nil
	})

	mcp.AddTool(server, readOnlyTool(
		"local_ai_recent_events",
		"Read newest sanitized request metadata. Prompts, responses, headers, and credentials are never returned.",
		"Recent local AI events",
	), func(ctx context.Context, _ *mcp.CallToolRequest, input RecentEventsInput) (*mcp.CallToolResult, RecentEventsOutput, error) {
		limit := input.Limit
		if limit == 0 {
			limit = defaultRecentEventsLimit
		}
		if limit < 1 || limit > maxRecentEventsLimit {
			return nil, RecentEventsOutput{}, errors.New("limit must be between 1 and 100")
		}

		status, err := client.Status(ctx)
		if err != nil {
			return nil, RecentEventsOutput{}, err
		}
		events := status.RecentEvents
		if len(events) > limit {
			events = events[len(events)-limit:]
		}
		if events == nil {
			events = []RecentEvent{}
		}
		return nil, RecentEventsOutput{Count: len(events), Events: events}, nil
	})

	return server
}

func readOnlyTool(name, description, title string) *mcp.Tool {
	falseValue := false
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Title:       title,
		Annotations: &mcp.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &falseValue,
			OpenWorldHint:   &falseValue,
		},
	}
}

// Run serves one MCP stdio session until the harness disconnects or ctx is
// cancelled.
func Run(ctx context.Context, cfg Config, version string) error {
	client, err := NewControlClient(cfg, version)
	if err != nil {
		return err
	}
	return New(client, version).Run(ctx, &mcp.StdioTransport{})
}
