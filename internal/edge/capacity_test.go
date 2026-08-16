package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCapacityRejectsFinalModelBelowMeasuredCommitRequirement(t *testing.T) {
	peak := 10.0
	var inferenceCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[]}`)
			return
		}
		inferenceCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	cfg := testConfig(backend.URL)
	cfg.Models[0].State = "qualified"
	cfg.Models[0].Deployments = []string{"final"}
	cfg.Models[0].PeakCommitGiB = &peak
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.commitHeadroom = func() (float64, error) { return 13.99, nil }

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
		t.Fatalf("capacity response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if inferenceCalls.Load() != 0 {
		t.Fatalf("insufficient capacity reached inference upstream %d times", inferenceCalls.Load())
	}
}

func TestCapacityAllowsRunningModelAndReportsMeasuredValues(t *testing.T) {
	peak := 10.0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer backend.Close()
	cfg := testConfig(backend.URL)
	cfg.Models[0].State = "qualified"
	cfg.Models[0].Deployments = []string{"final"}
	cfg.Models[0].PeakCommitGiB = &peak
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.commitHeadroom = func() (float64, error) { return 1, nil }

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("running model inference status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	for _, expected := range []string{`"model_running":true`, `"required_commit_gib":14`, `"commit_headroom_gib":1`, `"measured":true`, `"available":true`} {
		if !strings.Contains(status.Body.String(), expected) {
			t.Errorf("capacity status missing %s: %s", expected, status.Body.String())
		}
	}
}

// runningBackend serves the router /running probe and records how often the
// inference path was actually reached.
func runningBackend(t *testing.T, running string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var inferenceCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, running)
			return
		}
		inferenceCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(backend.Close)
	return backend, &inferenceCalls
}

func TestCapacityRejectsModelExceedingDeviceVRAMBudget(t *testing.T) {
	backend, inferenceCalls := runningBackend(t, `{"running":[]}`)
	commit, vram, device := 8.0, 15.2, 15.92

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].PeakVRAMGiB = &vram
	cfg.Models[0].DeviceVRAMGiB = &device
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Commit headroom is abundant: only the VRAM budget can reject this.
	server.commitHeadroom = func() (float64, error) { return 30, nil }

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
		t.Fatalf("vram budget response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if inferenceCalls.Load() != 0 {
		t.Fatalf("vram overrun reached inference upstream %d times", inferenceCalls.Load())
	}

	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	for _, expected := range []string{`"reason":"insufficient_vram_budget"`, `"required_vram_gib":16.2`, `"device_vram_gib":15.92`} {
		if !strings.Contains(status.Body.String(), expected) {
			t.Errorf("capacity status missing %s: %s", expected, status.Body.String())
		}
	}
}

func TestCapacityAdmitsModelInsideDeviceVRAMBudget(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit, vram, device := 8.0, 14.5, 15.92

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].PeakVRAMGiB = &vram
	cfg.Models[0].DeviceVRAMGiB = &device
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.commitHeadroom = func() (float64, error) { return 30, nil }

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("in-budget model status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCapacityChargesPromptCacheAgainstCommitHeadroom(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit := 10.0
	cacheRAM := 6144 // 6 GiB of host-RAM prompt cache

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].CacheRAMMiB = &cacheRAM
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 10 peak + 4 reserve = 14, which the old accounting would have admitted.
	// The prompt cache pushes the requirement to 20.
	server.commitHeadroom = func() (float64, error) { return 15, nil }

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("prompt cache ignored by admission: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if !strings.Contains(status.Body.String(), `"required_commit_gib":20`) {
		t.Errorf("required commit does not include the prompt cache: %s", status.Body.String())
	}

	server.commitHeadroom = func() (float64, error) { return 21, nil }
	recorder = dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("sufficient headroom rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUnmeasuredCanaryWithHostMemoryFailsClosed(t *testing.T) {
	backend, inferenceCalls := runningBackend(t, `{"running":[]}`)

	for _, testCase := range []struct {
		name  string
		apply func(*Model)
	}{
		{"tensor offload", func(m *Model) { m.OffloadsTensors = true }},
		{"prompt cache", func(m *Model) { size := 6144; m.CacheRAMMiB = &size }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := testConfig(backend.URL)
			testCase.apply(&cfg.Models[0])
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server.commitHeadroom = func() (float64, error) { return 0, errCapacityUnavailable }

			before := inferenceCalls.Load()
			recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
			if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
				t.Fatalf("unmeasured host-memory model admitted: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if inferenceCalls.Load() != before {
				t.Fatalf("unmeasured host-memory model reached inference upstream")
			}
			status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
			if !strings.Contains(status.Body.String(), `"reason":"resource_measurement_required_for_host_memory"`) {
				t.Errorf("unexpected capacity reason: %s", status.Body.String())
			}
		})
	}
}

func TestUnmeasuredCanaryAllowedButUnmeasuredFinalFailsClosed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[]}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	canaryServer, err := New(testConfig(backend.URL))
	if err != nil {
		t.Fatal(err)
	}
	canaryServer.commitHeadroom = func() (float64, error) { return 0, errCapacityUnavailable }
	canary := dataRequest(t, canaryServer.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if canary.Code != http.StatusOK {
		t.Fatalf("unmeasured canary status=%d body=%s", canary.Code, canary.Body.String())
	}

	finalCfg := testConfig(backend.URL)
	finalCfg.Models[0].State = "qualified"
	finalCfg.Models[0].Deployments = []string{"final"}
	finalServer, err := New(finalCfg)
	if err != nil {
		t.Fatal(err)
	}
	finalServer.commitHeadroom = func() (float64, error) { return 0, errCapacityUnavailable }
	final := dataRequest(t, finalServer.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if final.Code != http.StatusServiceUnavailable || errorCode(t, final) != "insufficient_capacity" {
		t.Fatalf("unmeasured final response: status=%d body=%s", final.Code, final.Body.String())
	}
}
