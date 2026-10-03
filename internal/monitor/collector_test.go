package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSampler struct {
	mu        sync.Mutex
	reading   hardwareSample
	preferred []string
	closed    bool
}

func (f *fakeSampler) sample(preferred string) hardwareSample {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preferred = append(f.preferred, preferred)
	return f.reading
}

func (f *fakeSampler) info() machineInfo {
	return machineInfo{CPUName: "Test CPU", Cores: 8, Threads: 16}
}

func (f *fakeSampler) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// statusJSON carries fields the page has no use for - request IDs, the router
// URL, the admin-facing event log - to prove the projection drops them.
const statusJSON = `{
  "service": "cia-edge", "version": "1.2.3", "ready": true, "uptime_seconds": 3600,
  "upstream": {"url": "http://127.0.0.1:19292", "reachable": true},
  "active_model": "fast",
  "gate": {"active": 1, "queued": 0, "max_active": 1, "max_queue": 4, "wait_timeout_seconds": 30},
  "capacity": {"model": "fast", "model_running": true, "available": true, "reason": "model_already_running",
               "commit_headroom_gib": 12.5, "required_commit_gib": 9.25, "measured": true},
  "gpu_memory": {"state": "ok", "dedicated_mib": 9000, "shared_mib": 400, "budget_mib": 16304,
                 "adapter": "luid_0x00000000_0x00018459_phys_0", "message": "healthy"},
  "maintenance": {"state": "running", "draining": false, "drained": false},
  "models": [{"id": "fast", "object": "model", "owned_by": "local", "display_name": "Fast Model",
              "capabilities": {"chat_completions": true, "streaming": true}}],
  "model_statuses": [{"id": "fast", "available": true, "active": true, "process_state": "ready",
                      "reason": "model_already_running", "context_tokens": 131072,
                      "profile": {"n_predict": 8192, "compact_threshold_tokens": 100000},
                      "runtime": {"id": "b10549", "engine": "llama.cpp", "source_repository": "https://example.invalid/private"},
                      "checkpoints": {"configured": true, "ctx_checkpoints": 32, "runtime_capable": true}}],
  "runtimes": [{"id": "b10549", "engine": "llama.cpp", "backend": "ROCm", "checkpoint_capable": true}],
  "recent_events": [{"request_id": "req-secret-7", "path": "/v1/chat/completions", "status": 200}],
  "deployment": {"environment": "canary", "release": "r42", "commit": "abcdef1234567890", "status": "active"}
}`

const inferenceJSON = `{
  "generated_at": "2026-09-28T12:00:00Z",
  "gate": {"active": 1, "queued": 2, "max_active": 1, "max_queue": 4},
  "live": [{"model": "fast", "route": "/v1/chat/completions", "stream": true, "phase": "generating",
            "started_at": "2026-09-28T11:59:50Z", "elapsed_ms": 10000, "ttft_ms": 800,
            "output_tokens": 420, "tokens_per_second": 47.5, "context_tokens": 131072, "max_output_tokens": 8192}],
  "recent": [{"started_at": "2026-09-28T11:58:00Z", "model": "fast", "route": "/v1/chat/completions",
              "stream": true, "status": 200, "finish": "stop", "prompt_tokens": 23000, "cached_tokens": 20000,
              "output_tokens": 512, "decode_tokens_per_second": 51.2, "duration_ms": 14000, "context_tokens": 131072}],
  "totals": {"since": "2026-09-28T11:00:00Z", "requests": 9, "failed": 1, "prompt_tokens": 90000}
}`

func fakeEdge(t *testing.T, inference http.HandlerFunc) *url.URL {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case edgeStatusPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, statusJSON)
		case edgeInferencePath:
			inference(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func serveInference(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func newTestCollector(t *testing.T, control *url.URL, sampler *fakeSampler) (*Collector, *fakeClock) {
	t.Helper()
	data, _ := url.Parse("http://127.0.0.1:18090")
	clock := &fakeClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	return newCollector(Options{Version: "test", Environment: "canary", ControlURL: control, DataURL: data}, sampler, clock.Now), clock
}

func decodeSnapshot(t *testing.T, collector *Collector) (snapshot, string) {
	t.Helper()
	raw := collector.Snapshot()
	if raw == nil {
		t.Fatal("no snapshot was encoded")
	}
	var decoded snapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return decoded, string(raw)
}

func TestCollectorPairsTheEdgeWithTheMachine(t *testing.T) {
	sampler := &fakeSampler{reading: hardwareSample{GPUUtil: ptr(88), VRAMDedicatedMiB: ptr(9000), CPUUtil: ptr(12)}}
	collector, _ := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), sampler)
	ctx := context.Background()
	collector.refreshStatus(ctx)
	collector.tick(ctx)

	got, raw := decodeSnapshot(t, collector)
	if !got.Edge.Reachable || got.Edge.Telemetry != "ok" {
		t.Fatalf("edge view = %+v", got.Edge)
	}
	if got.Coverage.Edge != "ok" || got.Coverage.ExternalActivityOnly != 0 {
		t.Fatalf("coverage = %+v", got.Coverage)
	}
	if got.Activity.Phase != phaseGenerating || got.Activity.Model != "fast" || got.Activity.Queued != 2 {
		t.Fatalf("activity = %+v, want generating fast with two queued", got.Activity)
	}
	if got.Hardware.GPUUtil == nil || *got.Hardware.GPUUtil != 88 || got.Machine.Threads != 16 {
		t.Fatalf("hardware = %+v machine = %+v", got.Hardware, got.Machine)
	}
	if len(got.History.TokensPerSecond) != 1 || got.History.TokensPerSecond[0] == nil || *got.History.TokensPerSecond[0] != 47.5 {
		t.Fatalf("speed history = %v, want the live rate", got.History.TokensPerSecond)
	}
	if got.Monitor.DataURL != "http://127.0.0.1:18090" || got.Monitor.IntervalMS != 1000 {
		t.Fatalf("monitor = %+v", got.Monitor)
	}
	// The sampler is steered to the adapter the edge judged.
	if last := sampler.preferred[len(sampler.preferred)-1]; last != "luid_0x00000000_0x00018459_phys_0" {
		t.Fatalf("sampler was steered to %q", last)
	}
	for _, private := range []string{"req-secret-7", "recent_events", "19292", "example.invalid", "request_id"} {
		if strings.Contains(raw, private) {
			t.Errorf("snapshot forwarded %q; the projection must drop what the page does not use", private)
		}
	}
}

func TestCollectorKeepsWorkingWithAnEdgeWithoutTelemetry(t *testing.T) {
	collector, _ := newTestCollector(t, fakeEdge(t, http.NotFound), &fakeSampler{})
	ctx := context.Background()
	collector.refreshStatus(ctx)
	collector.tick(ctx)

	got, _ := decodeSnapshot(t, collector)
	if !got.Edge.Reachable || got.Edge.Telemetry != "unavailable" || got.Edge.Inference != nil {
		t.Fatalf("edge view = %+v, want reachable without telemetry", got.Edge)
	}
	if got.Coverage.Edge != "unavailable" {
		t.Fatalf("coverage = %+v, want explicit unavailable edge telemetry", got.Coverage)
	}
	// The status still says a request holds the slot; without telemetry the
	// page can only say something is being processed.
	if got.Activity.Phase != phaseWorking {
		t.Fatalf("phase = %q, want %q", got.Activity.Phase, phaseWorking)
	}
}

// hangUp accepts the connection and drops it, which fails a request at once;
// a closed port would do the same only after Windows' connect retries.
func hangUp(t *testing.T) *url.URL {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	return parsed
}

func TestCollectorReportsAnEdgeThatIsDown(t *testing.T) {
	collector, clock := newTestCollector(t, hangUp(t), &fakeSampler{reading: hardwareSample{CPUUtil: ptr(5)}})
	ctx := context.Background()
	collector.refreshStatus(ctx)
	collector.tick(ctx)
	clock.Advance(time.Second)
	collector.tick(ctx)

	got, _ := decodeSnapshot(t, collector)
	if got.Edge.Reachable || got.Activity.Phase != phaseOffline || got.Edge.Status != nil {
		t.Fatalf("snapshot = %+v %+v, want offline", got.Edge, got.Activity)
	}
	if len(got.History.TokensPerSecond) != 2 || got.History.TokensPerSecond[1] != nil {
		t.Fatalf("speed history = %v: an unasked edge is a gap, not a zero", got.History.TokensPerSecond)
	}
	if len(got.History.CPUUtil) != 2 || got.History.CPUUtil[1] == nil {
		t.Fatalf("CPU history = %v: the machine is still measured while the edge is down", got.History.CPUUtil)
	}
}

func TestCollectorReportsTheAgeOfTheLastStatus(t *testing.T) {
	collector, clock := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), &fakeSampler{})
	ctx := context.Background()
	collector.refreshStatus(ctx)
	clock.Advance(7 * time.Second)
	collector.tick(ctx)
	got, _ := decodeSnapshot(t, collector)
	if got.Edge.StatusAgeMS == nil || *got.Edge.StatusAgeMS != 7000 {
		t.Fatalf("status age = %v, want 7000 ms", got.Edge.StatusAgeMS)
	}
}

func TestSeriesKeepsTheNewestSamplesInOrder(t *testing.T) {
	var s series
	for value := 0; value < historyLength+5; value++ {
		s.push(ptr(float64(value)))
	}
	ordered := s.ordered()
	if len(ordered) != historyLength {
		t.Fatalf("len = %d, want %d", len(ordered), historyLength)
	}
	if *ordered[0] != 5 || *ordered[historyLength-1] != historyLength+4 {
		t.Fatalf("ordered runs %v..%v, want 5..%d", *ordered[0], *ordered[historyLength-1], historyLength+4)
	}
	var young series
	young.push(ptr(1))
	young.push(nil)
	if got := young.ordered(); len(got) != 2 || *got[0] != 1 || got[1] != nil {
		t.Fatalf("young series = %v", got)
	}
}

func TestDeriveActivityNamesWhatTheServerIsDoing(t *testing.T) {
	loading := &edgeStatus{ModelStatuses: []edgeModelStatus{{ID: "big", ProcessState: "starting"}}}
	loaded := &edgeStatus{ActiveModel: "fast", ModelStatuses: []edgeModelStatus{{ID: "fast", ProcessState: "ready"}}}
	draining := &edgeStatus{Maintenance: edgeMaintenance{Draining: true}}
	streamPrompt := &edgeInference{Live: []edgeLive{{Model: "fast", Stream: true, Phase: "prompt"}}}
	coldPrompt := &edgeInference{Live: []edgeLive{{Model: "big", Stream: true, Phase: "prompt", ColdStart: true}}}
	buffered := &edgeInference{Live: []edgeLive{{Model: "fast", Phase: "prompt"}}}
	generating := &edgeInference{Live: []edgeLive{{Model: "fast", Stream: true, Phase: "generating"}}, Gate: edgeGate{Queued: 3}}
	queued := &edgeInference{Gate: edgeGate{Queued: 2}}

	for _, tc := range []struct {
		name      string
		reachable bool
		status    *edgeStatus
		inference *edgeInference
		phase     string
		model     string
		queued    int64
	}{
		{"edge down", false, loaded, generating, phaseOffline, "", 0},
		{"draining outranks a live request", true, draining, generating, phaseDraining, "", 3},
		{"generating reports the queue beside it", true, loaded, generating, phaseGenerating, "fast", 3},
		{"a streamed prompt", true, loaded, streamPrompt, phasePrompt, "fast", 0},
		{"a cold start while the router loads", true, loading, coldPrompt, phaseLoading, "big", 0},
		{"a request the edge cannot see into", true, loaded, buffered, phaseWorking, "fast", 0},
		{"a load with nothing in flight", true, loading, &edgeInference{}, phaseLoading, "big", 0},
		{"waiting for the slot", true, loaded, queued, phaseQueued, "", 2},
		{"a loaded model at rest", true, loaded, &edgeInference{}, phaseReady, "fast", 0},
		{"nothing loaded", true, &edgeStatus{}, &edgeInference{}, phaseIdle, "", 0},
		{"no status yet", true, nil, &edgeInference{}, phaseIdle, "", 0},
	} {
		got := deriveActivity(tc.reachable, tc.status, tc.inference, nil)
		if got.Phase != tc.phase || got.Model != tc.model || got.Queued != tc.queued {
			t.Errorf("%s: got %+v, want phase %q model %q queued %d", tc.name, got, tc.phase, tc.model, tc.queued)
		}
	}
}

func TestGenerationRateIsZeroAtRestAndAbsentOffline(t *testing.T) {
	if got := generationRate(activity{Phase: phaseReady}); got == nil || *got != 0 {
		t.Fatalf("at rest = %v, want 0", got)
	}
	if got := generationRate(activity{Phase: phaseOffline}); got != nil {
		t.Fatalf("offline = %v, want nil", *got)
	}
	if got := generationRate(activity{Phase: phaseGenerating, Live: &edgeLive{TokensPerSecond: ptr(33)}}); got == nil || *got != 33 {
		t.Fatalf("generating = %v, want 33", got)
	}
}

func TestEdgeClientRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, statusJSON)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()
	base, _ := url.Parse(redirector.URL)
	if _, err := newEdgeClient(base).status(context.Background()); err == nil {
		t.Fatal("a redirect was followed; the control URL is the only destination the monitor may reach")
	}
}
