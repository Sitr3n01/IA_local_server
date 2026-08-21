package edge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The classifier exists to name a degradation that raises no error and moves no
// llama.cpp counter, so it needs a gate of its own: a threshold that never fires
// certifies a paging configuration as healthy, and one that always fires trains
// an operator to ignore it. These are the four adapter states actually measured
// on the reference workstation, recorded in
// benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md.
//
// The load-bearing pair is the middle two. The 4-block split at a short context
// sits at 96.4% dedicated and was the fastest configuration measured; the same
// split at 32k sits at 97.6% and had collapsed to 195 t/s. Occupancy cannot
// separate them. A future change that makes this test pass by widening the
// occupancy band alone has deleted the only signal that tells them apart.
func TestClassifyGPUPressureMatchesMeasuredConfigurations(t *testing.T) {
	const budget = 16304.0

	cases := []struct {
		name      string
		dedicated float64
		shared    float64
		want      string
	}{
		{"idle desktop, no model loaded", 3122, 412, gpuPressureOK},
		{"4-block split at 512 ctx, 1034 t/s pp512", 15720, 514, gpuPressureElevated},
		{"4-block split at 32k ctx, 195 t/s pp32768", 15916, 1253, gpuPressurePressured},
		{"full residency at 512 ctx, 270 t/s pp512", 16071, 1350, gpuPressurePressured},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := gpuMemorySnapshot{Adapter: "test", DedicatedMiB: testCase.dedicated, SharedMiB: testCase.shared}
			got := classifyGPUPressure(snapshot, budget, nil)
			if got.State != testCase.want {
				t.Fatalf("state = %q, want %q (dedicated %.0f, shared %.0f)", got.State, testCase.want, testCase.dedicated, testCase.shared)
			}
			if got.DedicatedMiB == nil || *got.DedicatedMiB != testCase.dedicated {
				t.Fatalf("dedicated not reported back: %+v", got.DedicatedMiB)
			}
			if got.Message == "" {
				t.Fatal("a verdict with no message tells an operator nothing")
			}
		})
	}
}

func TestClassifyGPUPressureSeparatesSignals(t *testing.T) {
	const budget = 16304.0

	// Shared usage alone rises with ordinary desktop compositing. Reporting it
	// as pressure would fire on any machine running a browser.
	sharedOnly := classifyGPUPressure(gpuMemorySnapshot{DedicatedMiB: 6000, SharedMiB: 2048}, budget, nil)
	if sharedOnly.State != gpuPressureOK {
		t.Fatalf("shared usage alone classified as %q, want ok", sharedOnly.State)
	}

	// Occupancy alone is the intended state of a configuration sized to the card.
	occupancyOnly := classifyGPUPressure(gpuMemorySnapshot{DedicatedMiB: 16000, SharedMiB: 200}, budget, nil)
	if occupancyOnly.State != gpuPressureElevated {
		t.Fatalf("occupancy alone classified as %q, want elevated", occupancyOnly.State)
	}
}

// An unobservable host has not been shown to be healthy. Both of these must stay
// distinct from ok, or a deployment with no GPU counters reports a clean bill of
// health it never earned.
func TestClassifyGPUPressureUnknownWhenUnmeasurable(t *testing.T) {
	probeFailed := classifyGPUPressure(gpuMemorySnapshot{}, 16304, errGPUMemoryUnavailable)
	if probeFailed.State != gpuPressureUnknown {
		t.Fatalf("failed probe classified as %q, want unknown", probeFailed.State)
	}
	if probeFailed.DedicatedMiB != nil {
		t.Fatal("a failed probe must not report a dedicated figure")
	}

	noBudget := classifyGPUPressure(gpuMemorySnapshot{DedicatedMiB: 16000, SharedMiB: 1400}, 0, nil)
	if noBudget.State != gpuPressureUnknown {
		t.Fatalf("undeclared budget classified as %q, want unknown", noBudget.State)
	}
	// The raw reading is still worth reporting even when it cannot be classified.
	if noBudget.DedicatedMiB == nil || *noBudget.DedicatedMiB != 16000 {
		t.Fatal("an unclassifiable sample must still carry its reading")
	}
}

func TestGPUPressureLevelOrdersStates(t *testing.T) {
	// Unknown must sort below ok rather than equal to it: a dashboard averaging
	// the series should not read an unobservable host as healthy.
	if gpuPressureLevel(gpuPressureUnknown) >= gpuPressureLevel(gpuPressureOK) {
		t.Fatal("unknown must not rank at or above ok")
	}
	if gpuPressureLevel(gpuPressureOK) >= gpuPressureLevel(gpuPressureElevated) {
		t.Fatal("ok must rank below elevated")
	}
	if gpuPressureLevel(gpuPressureElevated) >= gpuPressureLevel(gpuPressurePressured) {
		t.Fatal("elevated must rank below pressured")
	}
}

// Nothing may sample the GPU on a timer. The probe runs when a reader asks and
// is reused inside the TTL, so an idle deployment performs no GPU work.
func TestGPUMemoryCacheProbesAtMostOncePerInterval(t *testing.T) {
	calls := 0
	probe := func() (gpuMemorySnapshot, error) {
		calls++
		return gpuMemorySnapshot{DedicatedMiB: float64(calls)}, nil
	}
	now := time.Unix(0, 0)
	cache := gpuMemoryCache{now: func() time.Time { return now }}

	for i := 0; i < 5; i++ {
		if _, err := cache.sample(probe); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times inside the TTL, want 1", calls)
	}

	now = now.Add(gpuMemoryCacheTTL)
	if _, err := cache.sample(probe); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probe ran %d times after the TTL expired, want 2", calls)
	}
}

// A host without the counter set fails every probe. Retrying per request would
// turn a missing capability into a recurring cost.
func TestGPUMemoryCacheCachesFailures(t *testing.T) {
	calls := 0
	probe := func() (gpuMemorySnapshot, error) {
		calls++
		return gpuMemorySnapshot{}, errors.New("no counters")
	}
	now := time.Unix(0, 0)
	cache := gpuMemoryCache{now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if _, err := cache.sample(probe); err == nil {
			t.Fatal("expected the probe error to be returned")
		}
	}
	if calls != 1 {
		t.Fatalf("failing probe ran %d times inside the TTL, want 1", calls)
	}
}

func newGPUStatusServer(t *testing.T, snapshot gpuMemorySnapshot, probeErr error) *Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	cfg := testConfig(backend.URL)
	vram := 15.921875 // 16304 MiB, the reference adapter
	cfg.Models[0].DeviceVRAMGiB = &vram
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.gpuMemory = func() (gpuMemorySnapshot, error) { return snapshot, probeErr }
	return server
}

// The verdict has to reach an operator, and it has to stay out of the admission
// decision. Both halves are asserted here: a pressured adapter appears in the
// status document, and readiness is unchanged by it.
func TestStatusReportsGPUPressureWithoutAffectingReadiness(t *testing.T) {
	server := newGPUStatusServer(t, gpuMemorySnapshot{Adapter: "luid_test", DedicatedMiB: 16071, SharedMiB: 1350}, nil)

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Ready     bool `json:"ready"`
		GPUMemory struct {
			State        string   `json:"state"`
			DedicatedMiB *float64 `json:"dedicated_mib"`
			SharedMiB    *float64 `json:"shared_mib"`
			Message      string   `json:"message"`
		} `json:"gpu_memory"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.GPUMemory.State != gpuPressurePressured {
		t.Fatalf("gpu_memory.state = %q, want pressured", payload.GPUMemory.State)
	}
	if payload.GPUMemory.DedicatedMiB == nil || *payload.GPUMemory.DedicatedMiB != 16071 {
		t.Fatal("status did not carry the dedicated reading")
	}
	if !strings.Contains(payload.GPUMemory.Message, "paging") {
		t.Fatalf("message does not name the failure: %q", payload.GPUMemory.Message)
	}
	// Readiness is a capacity and upstream question. GPU pressure is a
	// diagnosis, and turning it into a 503 would trade a silent slowdown for a
	// silent outage.
	if !payload.Ready {
		t.Fatal("GPU pressure must not make the edge report itself unready")
	}
}

// Inference must not consult the probe at all. A degraded adapter is slow, not
// broken, and refusing requests on it would be a policy decision taken from one
// instant of a noisy signal.
func TestGPUPressureDoesNotBlockInference(t *testing.T) {
	var inferenceCalls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		inferenceCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	vram := 15.921875
	cfg.Models[0].DeviceVRAMGiB = &vram
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	probed := false
	server.gpuMemory = func() (gpuMemorySnapshot, error) {
		probed = true
		return gpuMemorySnapshot{DedicatedMiB: 16071, SharedMiB: 1350}, nil
	}

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("a pressured adapter refused inference: %s", recorder.Body.String())
	}
	if probed {
		t.Fatal("the request path sampled the GPU; the probe is observability only")
	}
}

func TestMetricsOmitGaugesWhenProbeUnavailable(t *testing.T) {
	server := newGPUStatusServer(t, gpuMemorySnapshot{}, errGPUMemoryUnavailable)

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/metrics", nil)
	body := recorder.Body.String()

	// A scrape recording 0 MiB dedicated would look like an idle GPU rather than
	// an unobservable one, so the gauges are absent instead.
	if strings.Contains(body, "cia_edge_gpu_dedicated_mib") {
		t.Fatalf("dedicated gauge emitted without a measurement:\n%s", body)
	}
	if !strings.Contains(body, "cia_edge_gpu_memory_pressure -1") {
		t.Fatalf("pressure gauge does not report unknown as -1:\n%s", body)
	}
}

func TestMetricsEmitGaugesWhenMeasured(t *testing.T) {
	server := newGPUStatusServer(t, gpuMemorySnapshot{Adapter: "luid_test", DedicatedMiB: 15720, SharedMiB: 514}, nil)

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/metrics", nil)
	body := recorder.Body.String()

	for _, want := range []string{
		"cia_edge_gpu_dedicated_mib 15720",
		"cia_edge_gpu_shared_mib 514",
		"cia_edge_gpu_memory_pressure 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
