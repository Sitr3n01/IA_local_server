package monitor

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEnergyIsIntegratedFromPowerReadingsAndNotInventedAcrossGaps(t *testing.T) {
	sampler := &fakeSampler{reading: hardwareSample{PowerW: ptr(120)}}
	collector, clock := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), sampler)
	ctx := context.Background()
	collector.refreshStatus(ctx)

	// The first reading has no interval before it.
	collector.tick(ctx)
	if got, _ := decodeSnapshot(t, collector); got.Energy.SessionWh != nil {
		t.Fatalf("energy after one reading = %v, want none", *got.Energy.SessionWh)
	}
	for range 3 {
		clock.Advance(time.Second)
		collector.tick(ctx)
	}
	got, _ := decodeSnapshot(t, collector)
	want := 120.0 * 3 / 3600 // three seconds at 120 W, in watt-hours
	if got.Energy.SessionWh == nil || math.Abs(*got.Energy.SessionWh-want) > 1e-9 || got.Energy.ObservedSeconds != 3 {
		t.Fatalf("energy = %+v, want %.6f Wh over 3 s", got.Energy, want)
	}
	if len(got.History.PowerW) != 4 || got.History.PowerW[3] == nil || *got.History.PowerW[3] != 120 {
		t.Fatalf("power history = %v", got.History.PowerW)
	}

	// A pause longer than the gap is a sleep, not a load.
	clock.Advance(time.Minute)
	collector.tick(ctx)
	if after, _ := decodeSnapshot(t, collector); *after.Energy.SessionWh != *got.Energy.SessionWh {
		t.Fatalf("a %v pause added energy: %v -> %v", time.Minute, *got.Energy.SessionWh, *after.Energy.SessionWh)
	}

	// A reading the driver did not give adds nothing and does not reset the total.
	sampler.mu.Lock()
	sampler.reading = hardwareSample{}
	sampler.mu.Unlock()
	clock.Advance(time.Second)
	collector.tick(ctx)
	if after, _ := decodeSnapshot(t, collector); *after.Energy.SessionWh != *got.Energy.SessionWh {
		t.Fatalf("a missing reading changed the total")
	}
}

func TestSnapshotCarriesSourcesAndTheGPUProcessesTheyAreFoundFrom(t *testing.T) {
	sampler := &fakeSampler{reading: hardwareSample{GPUProcesses: []gpuProcess{{PID: 10, Name: "Bionic.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 6500, GPUUtil: ptr(70)}}}}
	collector, _ := newTestCollector(t, fakeEdge(t, serveInference(`{"gate":{},"live":[],"recent":[],"totals":{}}`)), sampler)
	collector.sources = &discoverer{latest: []inferenceSource{{
		ID: "lm-studio-10", Kind: kindLMStudio, Label: "LM Studio / Bionic", PID: 10, Status: sourceOK, Activity: activityGenerating,
		ActivityBasis: "gpu", Models: []sourceModel{{ID: "qwen/qwen3-4b", Quantization: "Q4_K_M"}},
	}}}
	ctx := context.Background()
	collector.refreshStatus(ctx)
	collector.tick(ctx)

	got, raw := decodeSnapshot(t, collector)
	if len(got.Sources) != 1 || got.Sources[0].Models[0].ID != "qwen/qwen3-4b" {
		t.Fatalf("sources = %+v", got.Sources)
	}
	if got.Activity.Phase != phaseExternal || got.Activity.Model != "qwen/qwen3-4b" || got.Activity.External == nil || got.Activity.External.Label != "LM Studio / Bionic" {
		t.Fatalf("activity = %+v", got.Activity)
	}
	if collector.gpuProcesses()[0].PID != 10 {
		t.Fatal("the sampler's GPU processes were not handed to discovery")
	}
	// The image path is for grouping and never leaves the process.
	if strings.Contains(raw, `"path"`) {
		t.Fatalf("snapshot exposes an image path: %s", raw)
	}
	// Sources and processes are lists, never null, so the page needs no guard.
	var generic map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &generic)
	empty, _ := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), &fakeSampler{})
	empty.refreshStatus(ctx)
	empty.tick(ctx)
	_, emptyRaw := decodeSnapshot(t, empty)
	if !strings.Contains(emptyRaw, `"sources":[]`) || !strings.Contains(emptyRaw, `"gpu_processes":[]`) {
		t.Fatalf("empty lists were encoded as null: %s", emptyRaw)
	}
}

func TestDeriveActivityLetsTheEdgeLeadAndOtherToolsFillTheGaps(t *testing.T) {
	loaded := &edgeStatus{ActiveModel: "fast", ModelStatuses: []edgeModelStatus{{ID: "fast", ProcessState: "ready"}}}
	generating := &edgeInference{Live: []edgeLive{{Model: "fast", Stream: true, Phase: "generating"}}}
	working := []inferenceSource{{ID: "lm-1", Kind: kindLMStudio, Label: "LM Studio / Bionic", Activity: activityGenerating, ActivityBasis: "gpu", Models: []sourceModel{{ID: "qwen"}}}}
	holding := []inferenceSource{{ID: "ol-1", Kind: kindOllama, Label: "Ollama", Activity: activityIdle, Models: []sourceModel{{ID: "llama3"}}}}
	empty := []inferenceSource{{ID: "lm-2", Kind: kindLMStudio, Activity: activityIdle, Status: sourceOK}}
	noAPI := []inferenceSource{{ID: "lm-3", Kind: kindLMStudio, Label: "LM Studio / Bionic", Activity: activityGenerating, Status: sourceNoAPI}}
	// Bionic's own server: protected by a key, idle, but holding 7.4 GiB.
	protectedHolding := []inferenceSource{{ID: "lm-4", Kind: kindLMStudio, Label: "LM Studio / Bionic", Activity: activityIdle, Status: sourceProtected, VRAMDedicatedMiB: ptr(7400)}}
	protectedSmall := []inferenceSource{{ID: "lm-5", Kind: kindLMStudio, Label: "LM Studio / Bionic", Activity: activityIdle, Status: sourceProtected, VRAMDedicatedMiB: ptr(40)}}

	for _, tc := range []struct {
		name      string
		reachable bool
		status    *edgeStatus
		inference *edgeInference
		sources   []inferenceSource
		phase     string
		model     string
		external  bool
	}{
		{"no other tool", true, loaded, &edgeInference{}, nil, phaseReady, "fast", false},
		{"the edge generating wins over another tool", true, loaded, generating, working, phaseGenerating, "fast", true},
		{"another tool working while the edge rests", true, loaded, &edgeInference{}, working, phaseExternal, "qwen", true},
		{"another tool working with the edge down", false, nil, nil, working, phaseExternal, "qwen", true},
		{"another tool working with nothing loaded in the edge", true, &edgeStatus{}, &edgeInference{}, working, phaseExternal, "qwen", true},
		{"another tool holding a model, edge idle", true, &edgeStatus{}, &edgeInference{}, holding, phaseExternalReady, "llama3", true},
		{"another tool holding a model, edge down", false, nil, nil, holding, phaseExternalReady, "llama3", true},
		{"the edge ready is not replaced by an idle tool", true, loaded, &edgeInference{}, holding, phaseReady, "fast", true},
		{"an idle tool with no model says nothing", true, &edgeStatus{}, &edgeInference{}, empty, phaseIdle, "", false},
		{"a tool without an API that is working", true, &edgeStatus{}, &edgeInference{}, noAPI, phaseExternal, "", true},
		{"a protected tool holding GPU memory has a model loaded", true, &edgeStatus{}, &edgeInference{}, protectedHolding, phaseExternalReady, "", true},
		{"a protected tool holding almost nothing says nothing", true, &edgeStatus{}, &edgeInference{}, protectedSmall, phaseIdle, "", false},
	} {
		got := deriveActivity(tc.reachable, tc.status, tc.inference, tc.sources)
		if got.Phase != tc.phase || got.Model != tc.model || (got.External != nil) != tc.external {
			t.Errorf("%s: got phase %q model %q external %v, want %q %q %v", tc.name, got.Phase, got.Model, got.External != nil, tc.phase, tc.model, tc.external)
		}
	}
}

func TestGenerationRateForAnotherToolIsUnknownNotZero(t *testing.T) {
	if got := generationRate(activity{Phase: phaseExternal, External: &externalView{}}); got != nil {
		t.Fatalf("an external tool with no rate = %v, want nil", *got)
	}
	if got := generationRate(activity{Phase: phaseExternal, External: &externalView{TokensPerSecond: ptr(41)}}); got == nil || *got != 41 {
		t.Fatalf("an external tool with a rate = %v, want 41", got)
	}
}

func TestEdgePortsAreTheOnesDiscoveryLeavesAlone(t *testing.T) {
	control, _ := url.Parse("http://127.0.0.1:18091")
	data, _ := url.Parse("http://127.0.0.1:18090")
	got := edgePorts(Options{ControlURL: control, DataURL: data})
	if len(got) != 2 || got[0] != 18091 || got[1] != 18090 {
		t.Fatalf("ports = %v", got)
	}
	if len(edgePorts(Options{})) != 0 {
		t.Fatal("missing URLs produced ports")
	}
}
