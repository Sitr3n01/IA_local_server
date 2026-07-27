package mcpinference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesOnlyExplicitLocalDelegationTool(t *testing.T) {
	t.Parallel()

	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"choices":[{"message":{"content":"delegated result"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
		}`)
	}))
	defer edge.Close()

	server := New(newTestClient(t, edge.URL, Config{}), "test")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "local_ai_delegate" {
		t.Fatalf("tools = %+v", listed.Tools)
	}
	tool := listed.Tools[0]
	if !strings.Contains(tool.Description, "only when the user explicitly asks") {
		t.Fatalf("tool description does not require explicit user intent: %q", tool.Description)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint {
		t.Fatalf("unexpected annotations: %+v", tool.Annotations)
	}
	if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
		t.Fatalf("unsafe annotations: %+v", tool.Annotations)
	}

	schemaJSON, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	properties := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		properties = append(properties, name)
	}
	sort.Strings(properties)
	if got := strings.Join(properties, ","); got != "context,max_output_tokens,prompt" {
		t.Fatalf("input properties = %s", got)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "local_ai_delegate",
		Arguments: map[string]any{
			"prompt":            "use the local model",
			"max_output_tokens": 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %+v", result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output DelegateOutput
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatal(err)
	}
	if output.Model != "local-coding" || output.Text != "delegated result" || output.FinishReason != "stop" || output.Usage.TotalTokens != 6 {
		t.Fatalf("unexpected output: %+v", output)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server ended with error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop after client disconnect")
	}
}
