package mcpadmin

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

func TestServerExposesExactAdministrativeTools(t *testing.T) {
	t.Parallel()

	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		operation := r.URL.Path[strings.LastIndex(r.URL.Path, ":")+1:]
		_, _ = fmt.Fprintf(w, `{"operation":%q,"model":"local-coding","status":"completed","active_model":"local-coding"}`, operation)
	}))
	defer control.Close()

	controlClient, err := NewClient(Config{
		ControlURL: control.URL,
		Timeout:    time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "test-token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	server := New(controlClient, "test")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		gotNames = append(gotNames, tool.Name)
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %s has incorrect read-only/idempotent annotations: %+v", tool.Name, tool.Annotations)
			continue
		}
		wantDestructive := tool.Name != "local_ai_load_model"
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != wantDestructive {
			t.Errorf("tool %s destructiveHint = %v, want %v", tool.Name, tool.Annotations.DestructiveHint, wantDestructive)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %s has open-world annotation", tool.Name)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"required":["model_id"]`) {
			t.Errorf("tool %s does not require model_id: %s", tool.Name, schema)
		}
	}
	sort.Strings(gotNames)
	wantNames := []string{"local_ai_load_model", "local_ai_switch_model", "local_ai_unload_model"}
	if adminStringJSON(gotNames) != adminStringJSON(wantNames) {
		t.Fatalf("tool names = %v, want %v", gotNames, wantNames)
	}

	for _, toolName := range wantNames {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: map[string]any{"model_id": "local-coding"},
		})
		if err != nil {
			t.Fatalf("%s: %v", toolName, err)
		}
		if result.IsError {
			t.Fatalf("%s returned tool error: %+v", toolName, result.Content)
		}
		payload, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var output OperationOutput
		if err := json.Unmarshal(payload, &output); err != nil {
			t.Fatal(err)
		}
		if output.Model != "local-coding" || output.Status != "completed" {
			t.Fatalf("%s: unexpected output: %+v", toolName, output)
		}
	}

	missing, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_load_model",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.IsError {
		t.Fatal("missing required model_id did not produce a tool error")
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

func adminStringJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
