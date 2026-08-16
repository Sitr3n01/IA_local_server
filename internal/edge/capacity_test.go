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
	server.memoryStatus = fixedMemory(13.99, 24)

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
	server.memoryStatus = fixedMemory(1, 24)

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

// fixedMemory pins both host memory axes. Tests that only exercise commit pass a
// generous physical figure, which keeps the physical dimension inert for them —
// as it also is in production until a model records resources.peak_ram_gib.
func fixedMemory(commitGiB, physicalGiB float64) func() (memorySnapshot, error) {
	return func() (memorySnapshot, error) {
		return memorySnapshot{CommitGiB: commitGiB, PhysicalGiB: physicalGiB}, nil
	}
}

func unavailableMemory() (memorySnapshot, error) {
	return memorySnapshot{}, errCapacityUnavailable
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
	server.memoryStatus = fixedMemory(30, 24)

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
	server.memoryStatus = fixedMemory(30, 24)

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("in-budget model status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCapacityChargesPromptCacheAgainstCommitHeadroom(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit, ram := 10.0, 8.0
	cacheRAM := 6144 // 6 GiB of host-RAM prompt cache

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	// A prompt cache also requires a measured resident footprint; this test is
	// about the commit arithmetic, so the profile is completed deliberately.
	cfg.Models[0].PeakRAMGiB = &ram
	cfg.Models[0].CacheRAMMiB = &cacheRAM
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 10 peak + 4 reserve = 14, which the old accounting would have admitted.
	// The prompt cache pushes the requirement to 20.
	server.memoryStatus = fixedMemory(15, 40)

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("prompt cache ignored by admission: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if !strings.Contains(status.Body.String(), `"required_commit_gib":20`) {
		t.Errorf("required commit does not include the prompt cache: %s", status.Body.String())
	}

	server.memoryStatus = fixedMemory(21, 40)
	recorder = dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("sufficient headroom rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCapacityRejectsModelThatCannotStayResident(t *testing.T) {
	backend, inferenceCalls := runningBackend(t, `{"running":[]}`)
	commit, ram := 10.0, 12.4

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	cfg.Models[0].PeakRAMGiB = &ram
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Commit is abundant (needs 14, has 30) but only 13 GiB of RAM is free
	// against a 12.4 GiB resident footprint plus the 2 GiB reserve. Commit alone
	// would have admitted this and let the weights page out to disk.
	server.memoryStatus = fixedMemory(30, 13)

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
		t.Fatalf("physical memory response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if inferenceCalls.Load() != 0 {
		t.Fatalf("physical memory overrun reached inference upstream %d times", inferenceCalls.Load())
	}

	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	for _, expected := range []string{
		`"reason":"insufficient_physical_memory"`,
		`"required_physical_gib":14.4`,
		`"physical_headroom_gib":13`,
	} {
		if !strings.Contains(status.Body.String(), expected) {
			t.Errorf("capacity status missing %s: %s", expected, status.Body.String())
		}
	}

	// Enough free RAM and the same model is admitted.
	server.memoryStatus = fixedMemory(30, 20)
	recorder = dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("resident model rejected with ample RAM: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUnmeasuredRAMLeavesVerdictUnchanged(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit := 10.0

	cfg := testConfig(backend.URL)
	cfg.Models[0].PeakCommitGiB = &commit
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Physical headroom well below any plausible footprint. Without a measured
	// peak_ram_gib the dimension must stay inert rather than guess a footprint,
	// so the six existing candidates keep their current behaviour.
	server.memoryStatus = fixedMemory(30, 0.2)

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unmeasured RAM changed the verdict: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if !strings.Contains(status.Body.String(), `"reason":"commit_headroom_available"`) {
		t.Errorf("unexpected reason: %s", status.Body.String())
	}
	if !strings.Contains(status.Body.String(), `"required_physical_gib":null`) {
		t.Errorf("unmeasured RAM should report a null requirement: %s", status.Body.String())
	}
}

func TestHostMemoryModelRequiresCompleteResourceProfile(t *testing.T) {
	backend, inferenceCalls := runningBackend(t, `{"running":[]}`)
	commit, vram, ram, device := 10.0, 12.0, 8.0, 15.92

	// Every partial combination for a tensor-offloading model. The dangerous one
	// is "commit present, RAM absent": commit alone used to admit it.
	for _, testCase := range []struct {
		name    string
		apply   func(*Model)
		missing string
	}{
		{"offload, no commit", func(m *Model) {
			m.OffloadsTensors = true
			m.PeakVRAMGiB, m.PeakRAMGiB, m.DeviceVRAMGiB = &vram, &ram, &device
		}, "peak_commit_gib"},
		{"offload, no vram", func(m *Model) {
			m.OffloadsTensors = true
			m.PeakCommitGiB, m.PeakRAMGiB, m.DeviceVRAMGiB = &commit, &ram, &device
		}, "peak_vram_gib"},
		{"offload, no ram", func(m *Model) {
			m.OffloadsTensors = true
			m.PeakCommitGiB, m.PeakVRAMGiB, m.DeviceVRAMGiB = &commit, &vram, &device
		}, "peak_ram_gib"},
		{"offload, no device budget", func(m *Model) {
			m.OffloadsTensors = true
			m.PeakCommitGiB, m.PeakVRAMGiB, m.PeakRAMGiB = &commit, &vram, &ram
		}, "device.vram_mib"},
		{"prompt cache, no ram", func(m *Model) {
			size := 6144
			m.CacheRAMMiB = &size
			m.PeakCommitGiB = &commit
		}, "peak_ram_gib"},
		{"prompt cache, no commit", func(m *Model) {
			size := 6144
			m.CacheRAMMiB = &size
			m.PeakRAMGiB = &ram
		}, "peak_commit_gib"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := testConfig(backend.URL)
			testCase.apply(&cfg.Models[0])
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Headroom is abundant on every axis: only profile completeness can reject.
			server.memoryStatus = fixedMemory(60, 40)

			before := inferenceCalls.Load()
			recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
			if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "insufficient_capacity" {
				t.Fatalf("partial profile admitted: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if inferenceCalls.Load() != before {
				t.Fatalf("partial profile reached inference upstream")
			}

			status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
			body := status.Body.String()
			if !strings.Contains(body, `"reason":"resource_profile_incomplete"`) {
				t.Errorf("unexpected reason: %s", body)
			}
			if !strings.Contains(body, testCase.missing) {
				t.Errorf("status does not name the missing field %q: %s", testCase.missing, body)
			}
			// An incomplete profile must never be reported as measured.
			if strings.Contains(body, `"measured":true`) {
				t.Errorf("incomplete profile reported as measured: %s", body)
			}
		})
	}
}

func TestCompleteHostMemoryProfileIsEvaluatedNormally(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit, vram, ram, device := 10.0, 12.0, 8.0, 15.92

	cfg := testConfig(backend.URL)
	m := &cfg.Models[0]
	m.OffloadsTensors = true
	m.PeakCommitGiB, m.PeakVRAMGiB, m.PeakRAMGiB, m.DeviceVRAMGiB = &commit, &vram, &ram, &device
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(60, 40)

	recorder := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("complete profile rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	for _, expected := range []string{`"reason":"commit_headroom_available"`, `"measured":true`} {
		if !strings.Contains(status.Body.String(), expected) {
			t.Errorf("capacity status missing %s: %s", expected, status.Body.String())
		}
	}
	if strings.Contains(status.Body.String(), "missing_profile_fields") {
		t.Errorf("complete profile reported missing fields: %s", status.Body.String())
	}
}

func TestCapacityMessageFollowsReason(t *testing.T) {
	// A single "commit headroom" sentence for every refusal made VRAM, physical
	// memory, and an incomplete profile indistinguishable from a full pagefile.
	for reason, want := range map[string]string{
		"insufficient_vram_budget":     "VRAM budget",
		"insufficient_physical_memory": "physical memory",
		"insufficient_commit_headroom": "commit headroom",
		"resource_profile_incomplete":  "resource profile is incomplete",
	} {
		if got := capacityMessage(reason); !strings.Contains(got, want) {
			t.Errorf("capacityMessage(%q) = %q, want it to mention %q", reason, got, want)
		}
	}
	if capacityMessage("insufficient_vram_budget") == capacityMessage("insufficient_commit_headroom") {
		t.Error("distinct refusal reasons produced identical operator text")
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
	canaryServer.memoryStatus = unavailableMemory
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
	finalServer.memoryStatus = unavailableMemory
	final := dataRequest(t, finalServer.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding"}`))
	if final.Code != http.StatusServiceUnavailable || errorCode(t, final) != "insufficient_capacity" {
		t.Fatalf("unmeasured final response: status=%d body=%s", final.Code, final.Body.String())
	}
}

// readinessConfig builds a two-model allowlist whose public model is the second
// entry, so any code path that reaches for Models[0] fails these tests.
func readinessConfig(upstream string) Config {
	cfg := testConfig(upstream)
	cfg.Models = []Model{
		{ID: "local-fast", Object: "model", OwnedBy: "local", State: "candidate", Deployments: []string{"canary"}},
		{ID: "local-coding", Object: "model", OwnedBy: "local", State: "candidate", Deployments: []string{"canary"}},
	}
	cfg.PublicModelID = "local-coding"
	return cfg
}

func TestReadinessFollowsPublicModelNotArrayOrder(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	fits, doesNot := 1.0, 100.0

	t.Run("public model fits", func(t *testing.T) {
		cfg := readinessConfig(backend.URL)
		cfg.Models[0].PeakCommitGiB = &doesNot // first entry cannot be admitted
		cfg.Models[1].PeakCommitGiB = &fits    // public model can
		server, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		server.memoryStatus = fixedMemory(20, 40)

		recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/readyz", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("readyz=%d body=%s; readiness followed Models[0] instead of the public model", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("public model does not fit", func(t *testing.T) {
		cfg := readinessConfig(backend.URL)
		cfg.Models[0].PeakCommitGiB = &fits    // first entry would be admitted
		cfg.Models[1].PeakCommitGiB = &doesNot // public model would not
		server, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		server.memoryStatus = fixedMemory(20, 40)

		recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/readyz", nil)
		if recorder.Code == http.StatusOK {
			t.Fatalf("readyz reported ready while the public model cannot be admitted: %s", recorder.Body.String())
		}

		status := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
		if !strings.Contains(status.Body.String(), `"model":"local-coding"`) {
			t.Errorf("headline capacity is not reported for the public model: %s", status.Body.String())
		}
	})
}

func TestConfigRejectsPublicModelOutsideAllowlist(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:9292")
	cfg.PublicModelID = "not-in-list"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a public model outside the allowlist was accepted")
	}
}
