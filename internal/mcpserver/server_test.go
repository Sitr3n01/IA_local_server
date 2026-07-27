package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesExactReadOnlyToolsAndStructuredOutputs(t *testing.T) {
	t.Parallel()

	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/livez":
			if r.Header.Get("Authorization") != "" {
				t.Error("liveness request must not contain Authorization")
			}
			_, _ = fmt.Fprint(w, `{"status":"ok","service":"cia-edge"}`)
		case "/readyz":
			if r.Header.Get("Authorization") != "" {
				t.Error("readiness request must not contain Authorization")
			}
			_, _ = fmt.Fprint(w, `{"status":"ready","service":"cia-edge","upstream_reachable":true}`)
		case "/api/v1/status":
			if r.Header.Get("Authorization") != "" {
				t.Error("read-only status request must not contain Authorization")
			}
			_, _ = fmt.Fprint(w, `{
				"service":"cia-edge",
				"version":"dev",
				"ready":true,
				"upstream":{"url":"http://127.0.0.1:19292","reachable":true},
				"models":[{"id":"local-coding","object":"model","owned_by":"local"}],
				"active_model":"local-coding",
				"gate":{"active":1,"queued":2,"max_active":1,"max_queue":4,"wait_timeout_seconds":120},
				"capacity":{"admission":"configured-profile","available":true},
				"recent_events":[
					{"time":"2026-07-20T00:00:00Z","request_id":"one","method":"POST","path":"/v1/responses","status":200,"duration_ms":10},
					{"time":"2026-07-20T00:00:01Z","request_id":"two","method":"POST","path":"/v1/chat/completions","status":200,"duration_ms":20}
				]
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer control.Close()

	controlClient, err := NewControlClient(Config{
		ControlURL: control.URL,
		Timeout:    time.Second,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	server := New(controlClient, "test")
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
	gotNames := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		gotNames = append(gotNames, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %s lacks read-only/idempotent annotations: %+v", tool.Name, tool.Annotations)
			continue
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("tool %s has destructive annotation", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %s has open-world annotation", tool.Name)
		}
	}
	sort.Strings(gotNames)
	wantNames := []string{
		"local_ai_active_model",
		"local_ai_capacity",
		"local_ai_health",
		"local_ai_models",
		"local_ai_recent_events",
	}
	if stringJSON(gotNames) != stringJSON(wantNames) {
		t.Fatalf("tool names = %v, want %v", gotNames, wantNames)
	}

	modelsResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_models",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelsResult.IsError {
		t.Fatalf("local_ai_models returned tool error: %+v", modelsResult.Content)
	}
	modelsJSON, err := json.Marshal(modelsResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var models ModelsOutput
	if err := json.Unmarshal(modelsJSON, &models); err != nil {
		t.Fatal(err)
	}
	if models.Count != 1 || models.Models[0].ID != "local-coding" {
		t.Fatalf("unexpected models output: %+v", models)
	}

	healthResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_health",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var health HealthOutput
	decodeStructured(t, healthResult, &health)
	if !health.Healthy || health.Live.Status != "ok" || health.Ready.Status != "ready" {
		t.Fatalf("unexpected health output: %+v", health)
	}

	activeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_active_model",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var active ActiveModelOutput
	decodeStructured(t, activeResult, &active)
	if !active.Active || active.ModelID != "local-coding" {
		t.Fatalf("unexpected active model output: %+v", active)
	}

	capacityResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_capacity",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var capacity CapacityOutput
	decodeStructured(t, capacityResult, &capacity)
	if !capacity.Ready || !capacity.Available || capacity.Queued != 2 || capacity.MaxQueue != 4 {
		t.Fatalf("unexpected capacity output: %+v", capacity)
	}

	eventsResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "local_ai_recent_events",
		Arguments: map[string]any{"limit": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventsResult.IsError {
		t.Fatalf("local_ai_recent_events returned tool error: %+v", eventsResult.Content)
	}
	eventsJSON, err := json.Marshal(eventsResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var events RecentEventsOutput
	if err := json.Unmarshal(eventsJSON, &events); err != nil {
		t.Fatal(err)
	}
	if events.Count != 1 || events.Events[0].RequestID != "two" {
		t.Fatalf("unexpected events output: %+v", events)
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

func stringJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, dst any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %+v", result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatal(err)
	}
}
