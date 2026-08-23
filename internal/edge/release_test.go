package edge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validReleaseManifest = `{
  "schema_version": 1,
  "environment": "final",
  "release_id": "final-20260822T190000Z-1a2b3c4d",
  "version": "v2-final-20260822.1",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "source_dirty": false,
  "previous_release_id": "final-20260801T120000Z-99887766",
  "status": "installed",
  "created_utc": "2026-08-22T19:00:00Z",
  "components": [{"name": "cia-edge.exe", "path": "C:\\IA\\local-ai-v2\\bin\\cia-edge.exe", "sha256": "AA"}],
  "router_api_key_path": "C:\\IA\\local-ai-v2\\state\\router-api-key.txt"
}`

func writeRelease(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.final.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReleaseParsesDeterministicallyAndDropsPaths(t *testing.T) {
	release, err := LoadRelease(writeRelease(t, validReleaseManifest), "final")
	if err != nil {
		t.Fatalf("LoadRelease: %v", err)
	}
	if release.Release != "final-20260822T190000Z-1a2b3c4d" || release.Environment != "final" {
		t.Fatalf("unexpected release identity: %+v", release)
	}
	if release.PreviousRelease != "final-20260801T120000Z-99887766" || release.Status != "installed" {
		t.Fatalf("unexpected release lineage: %+v", release)
	}

	// The on-disk manifest carries absolute paths and component hashes. None of
	// them may survive into the in-memory identity the status handler reports.
	encoded, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"C:\\\\IA", "router-api-key", "cia-edge.exe", "components", "sha256"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("release identity leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestLoadReleaseFailsClosedOnMalformedMetadata(t *testing.T) {
	cases := map[string]string{
		"unsupported schema":     strings.Replace(validReleaseManifest, `"schema_version": 1`, `"schema_version": 2`, 1),
		"environment mismatch":   strings.Replace(validReleaseManifest, `"environment": "final"`, `"environment": "canary"`, 1),
		"unknown environment":    strings.Replace(validReleaseManifest, `"environment": "final"`, `"environment": "staging"`, 1),
		"missing release id":     strings.Replace(validReleaseManifest, `"release_id": "final-20260822T190000Z-1a2b3c4d"`, `"release_id": ""`, 1),
		"release id with spaces": strings.Replace(validReleaseManifest, `"release_id": "final-20260822T190000Z-1a2b3c4d"`, `"release_id": "final 2026 08 22"`, 1),
		"abbreviated commit":     strings.Replace(validReleaseManifest, `"commit": "0123456789abcdef0123456789abcdef01234567"`, `"commit": "0123456"`, 1),
		"unknown status":         strings.Replace(validReleaseManifest, `"status": "installed"`, `"status": "probably-fine"`, 1),
		"not json":               "{",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRelease(writeRelease(t, contents), "final"); err == nil {
				t.Fatal("malformed release manifest was accepted")
			}
		})
	}

	if _, err := LoadRelease(filepath.Join(t.TempDir(), "absent.json"), "final"); err == nil {
		t.Fatal("a missing release manifest was reported as valid")
	}
}

func TestStatusReportsSanitizedDeploymentIdentity(t *testing.T) {
	release, err := LoadRelease(writeRelease(t, validReleaseManifest), "final")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)
	server.cfg.Release = release
	server.cfg.Environment = "final"

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d", recorder.Code)
	}
	var payload struct {
		Deployment struct {
			Environment     string `json:"environment"`
			Release         string `json:"release"`
			Commit          string `json:"commit"`
			PreviousRelease string `json:"previous_release"`
			Status          string `json:"status"`
			Healthy         bool   `json:"healthy"`
		} `json:"deployment"`
		Maintenance maintenanceSnapshot `json:"maintenance"`
		Uptime      int64               `json:"uptime_seconds"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if payload.Deployment.Environment != "final" || payload.Deployment.Release != release.Release {
		t.Fatalf("unexpected deployment block: %+v", payload.Deployment)
	}
	if payload.Deployment.Status != "installed" || payload.Deployment.PreviousRelease == "" {
		t.Fatalf("deployment block lost its lineage: %+v", payload.Deployment)
	}
	if payload.Maintenance.State != maintenanceRunning {
		t.Fatalf("status maintenance state = %q", payload.Maintenance.State)
	}
	if payload.Uptime < 0 {
		t.Fatalf("status uptime = %d", payload.Uptime)
	}
	for _, forbidden := range []string{"router-api-key", testAdminToken, testInferenceToken, testRouterToken} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("status response leaked %q", forbidden)
		}
	}
}

func TestMetricsExposeStableNamesAndBoundedReleaseCardinality(t *testing.T) {
	release, err := LoadRelease(writeRelease(t, validReleaseManifest), "final")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)
	server.cfg.Release = release

	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	if recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", body); recorder.Code != http.StatusOK {
		t.Fatalf("inference status = %d", recorder.Code)
	}
	controlRequest(t, server.ControlHandler(), http.MethodPost, "/api/v1/maintenance:drain", nil)

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/metrics", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", recorder.Code)
	}
	exposition := recorder.Body.String()

	for _, name := range []string{
		"cia_edge_requests_total",
		"cia_edge_auth_failures_total",
		"cia_edge_invalid_requests_total",
		"cia_edge_upstream_failures_total",
		"cia_edge_active_requests",
		"cia_edge_queued_requests",
		"cia_edge_queue_rejections_total",
		"cia_edge_queue_timeouts_total",
		"cia_edge_uptime_seconds",
		"cia_edge_maintenance_state",
		"cia_edge_maintenance_rejections_total",
		"cia_edge_model_loads_total",
		"cia_edge_model_load_failures_total",
		"cia_edge_admin_http_mutations_total",
		"cia_edge_admin_pipe_mutations_total",
		"cia_edge_queue_wait_seconds",
		"cia_edge_inference_duration_seconds",
		"cia_edge_model_load_duration_seconds",
		"cia_edge_release_info",
	} {
		if !strings.Contains(exposition, "# HELP "+name+" ") {
			t.Errorf("metric %s has no HELP line", name)
		}
		if !strings.Contains(exposition, "# TYPE "+name+" ") {
			t.Errorf("metric %s has no TYPE line", name)
		}
	}

	if !strings.Contains(exposition, "cia_edge_maintenance_state 2") {
		t.Errorf("drained provider did not report maintenance state 2:\n%s", exposition)
	}
	if !strings.Contains(exposition, `cia_edge_inference_duration_seconds_count 1`) {
		t.Errorf("inference duration was not observed:\n%s", exposition)
	}

	// Exactly one release series, and nothing in its labels but the identity.
	releaseSeries := 0
	for _, line := range strings.Split(exposition, "\n") {
		if strings.HasPrefix(line, "cia_edge_release_info{") {
			releaseSeries++
			if !strings.Contains(line, `release="final-20260822T190000Z-1a2b3c4d"`) {
				t.Errorf("release series lost its identity: %s", line)
			}
		}
	}
	if releaseSeries != 1 {
		t.Errorf("release info series count = %d, want 1", releaseSeries)
	}

	for _, forbidden := range []string{testAdminToken, testRouterToken, "router-api-key", "local-coding\"}"} {
		if strings.Contains(exposition, forbidden) {
			t.Errorf("metrics exposition leaked %q", forbidden)
		}
	}
}

func TestMetricLabelReducesUnsafeValues(t *testing.T) {
	if got := metricLabel(`a"b\nc d`); strings.ContainsAny(got, `"\ `) {
		t.Fatalf("metricLabel left an unsafe character: %q", got)
	}
	long := metricLabel(strings.Repeat("x", 400))
	if len(long) != 128 {
		t.Fatalf("metricLabel length = %d, want 128", len(long))
	}
}
