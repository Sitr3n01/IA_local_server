package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/claudedesktop"
	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
	"github.com/sitr3n/local-ai-provider/internal/panel"
)

func testManifestModel(id, state, deployments string) string {
	return fmt.Sprintf(`{
  "id": %q, "display_name": %q, "state": %q, "runtime": "runtime",
  "artifact": {"path": "C:\\models\\model.gguf", "bytes": 1024, "sha256": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
  "deployments": %s, "context_tokens": 65536, "max_output_tokens": 8192,
  "cache_type_k": "q4_0", "cache_type_v": "q4_0", "gpu_layers": 99,
  "capabilities": {"responses": true, "chat_completions": true, "streaming": true, "function_calling": true, "structured_output": false}
}`, id, "Model "+id+" | Q4 | 64k context", state, deployments)
}

// testCatalog serves "public" and "second" to canary and keeps "retired" in
// the manifest the way a retired roster entry stays there.
func testCatalog(t *testing.T) *panel.Catalog {
	t.Helper()
	manifest := fmt.Sprintf(`{"schema_version": 1, "provider": {"public_model": "public"}, "models": [%s, %s, %s]}`,
		testManifestModel("public", "candidate", `["canary"]`),
		testManifestModel("second", "candidate", `["canary"]`),
		testManifestModel("retired", "retired", `[]`))
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := panel.LoadCatalog(path, panel.EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestLoadSelectionFallsBackFromARetiredModelWithoutRewritingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.json")
	saved := []byte("{\n  \"schema_version\": 1,\n  \"model\": \"retired\"\n}\n")
	if err := os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := panel.NewSelectionStore(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	model, note, err := loadSelection(store)
	if err != nil || model != "public" || note == "" {
		t.Fatalf("model=%q note=%q err=%v", model, note, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(saved) {
		t.Fatalf("the saved selection was rewritten: %s", after)
	}
}

func TestLoadSelectionKeepsAValidChoice(t *testing.T) {
	store, err := panel.NewSelectionStore(filepath.Join(t.TempDir(), "selection.json"), testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("second"); err != nil {
		t.Fatal(err)
	}
	if model, note, err := loadSelection(store); err != nil || model != "second" || note != "" {
		t.Fatalf("model=%q note=%q err=%v", model, note, err)
	}
}

func TestClaudeGatewayProbeIsSpacedOut(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	probes := 0
	controller := &appController{
		config:         panel.Config{DataURL: "http://127.0.0.1:18090"},
		readCredential: func(string) (string, error) { return "claude-gateway-test-token-000000000000", nil },
		probeGateway: func(context.Context, claudedesktop.Gateway) error {
			probes++
			return errors.New("gateway down")
		},
		now: func() time.Time { return now },
	}
	for range 3 {
		if ok, note := controller.claudeGateway(context.Background()); ok || !strings.Contains(note, "gateway down") {
			t.Fatalf("ok=%v note=%q", ok, note)
		}
	}
	if probes != 1 {
		t.Fatalf("probed %d times within one interval", probes)
	}
	now = now.Add(gatewayProbeInterval)
	controller.claudeGateway(context.Background())
	controller.forgetGateway()
	controller.claudeGateway(context.Background())
	if probes != 3 {
		t.Fatalf("probes=%d, want a probe after the interval and after a mode change", probes)
	}
}

func statusController(t *testing.T, controlURL string) *appController {
	t.Helper()
	client, err := mcpserver.NewControlClient(mcpserver.Config{ControlURL: controlURL, Timeout: 2 * time.Second}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &appController{
		config:       panel.Config{Environment: panel.EnvironmentCanary},
		catalog:      testCatalog(t),
		statusClient: client,
		selected:     "public",
		now:          time.Now,
	}
}

func TestSnapshotListsTheDeploymentsModelsWhenTheEdgeIsDown(t *testing.T) {
	listener := httptest.NewServer(http.NotFoundHandler())
	url := listener.URL
	listener.Close() // nothing listens on this port any more
	snapshot, err := statusController(t, url).Snapshot(context.Background())
	if err == nil || snapshot.EdgeReachable {
		t.Fatalf("err=%v reachable=%v", err, snapshot.EdgeReachable)
	}
	if len(snapshot.Models) != 2 || snapshot.Models[0].ID != "public" || snapshot.Models[1].ID != "second" {
		t.Fatalf("models=%+v", snapshot.Models)
	}
}

func TestSnapshotExplainsAnEdgeThatIsNotReadyForLackOfMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"service": "cia-edge", "ready": false,
			"upstream": {"url": "http://127.0.0.1:19292", "reachable": true},
			"models": [{"id": "public"}],
			"active_model": "",
			"gate": {"active": 0, "queued": 0, "max_active": 1, "max_queue": 16},
			"capacity": {"available": false, "reason": "insufficient_physical_memory"},
			"model_statuses": [{"id": "public", "available": false, "reason": "insufficient_physical_memory"}]
		}`))
	}))
	defer server.Close()
	snapshot, err := statusController(t, server.URL).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.EdgeReachable || snapshot.ProviderReady || snapshot.ReadyNote != "memória física insuficiente" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	// The edge publishes only "public"; "second" must not be offered.
	if !snapshot.Models[0].Available || snapshot.Models[1].Available || snapshot.Models[1].Codex {
		t.Fatalf("models=%+v", snapshot.Models)
	}
}
