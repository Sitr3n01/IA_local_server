package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSwapAdmissionUsesActualMemoryAfterUnload(t *testing.T) {
	for _, surface := range []string{"responses", "anthropic", "control"} {
		for _, scenario := range []struct {
			name       string
			commit     float64
			physical   float64
			metricFail bool
			unloadFail bool
			stillReady bool
			routerFail bool
			allowed    bool
		}{
			{name: "actual reserves fit", commit: 18, physical: 10, allowed: true},
			{name: "peak overstates released commit", commit: 13, physical: 10},
			{name: "peak overstates released RAM", commit: 18, physical: 8},
			{name: "memory probe failed", metricFail: true},
			{name: "unload failed", unloadFail: true},
			{name: "outgoing process still loaded", stillReady: true},
			{name: "post unload router status failed", routerFail: true},
		} {
			t.Run(surface+"/"+scenario.name, func(t *testing.T) {
				var unloaded atomic.Bool
				var unloads, forwarded atomic.Int64
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+testRouterToken {
						t.Error("router credential was not injected")
					}
					switch r.URL.Path {
					case "/running":
						if unloaded.Load() && scenario.routerFail {
							http.Error(w, "unavailable", http.StatusServiceUnavailable)
						} else if !unloaded.Load() || scenario.stillReady {
							_, _ = io.WriteString(w, `{"running":[{"model":"local-fast","state":"ready"}]}`)
						} else {
							_, _ = io.WriteString(w, `{"running":[]}`)
						}
					case "/api/models/unload":
						unloads.Add(1)
						if scenario.unloadFail {
							http.Error(w, "unavailable", http.StatusServiceUnavailable)
						} else {
							unloaded.Store(true)
						}
					case "/v1/responses", "/v1/chat/completions", "/upstream/local-coding/health":
						forwarded.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"READY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
					default:
						t.Errorf("unexpected router route %s", r.URL.Path)
					}
				}))
				defer backend.Close()
				cfg := readinessConfig(backend.URL)
				cfg.Models[0].PeakCommitGiB = floatPointer(8)
				cfg.Models[0].PeakRAMGiB = floatPointer(6)
				cfg.Models[1].PeakCommitGiB = floatPointer(10)
				cfg.Models[1].PeakRAMGiB = floatPointer(7)
				server, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				server.memoryStatus = func() (memorySnapshot, error) {
					if !unloaded.Load() {
						return memorySnapshot{CommitGiB: 9, PhysicalGiB: 4}, nil
					}
					if scenario.metricFail {
						return memorySnapshot{}, errCapacityUnavailable
					}
					return memorySnapshot{CommitGiB: scenario.commit, PhysicalGiB: scenario.physical}, nil
				}
				var response *httptest.ResponseRecorder
				switch surface {
				case "responses":
					response = dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/responses", []byte(`{"model":"local-coding","input":"synthetic"}`))
				case "anthropic":
					response = anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(`{"model":"local-coding","max_tokens":10,"messages":[{"role":"user","content":"synthetic"}]}`))
				case "control":
					response = controlRequest(t, server.ControlHandler(), http.MethodPost, "/api/v1/models/local-coding:switch", nil)
				}
				if unloads.Load() != 1 {
					t.Fatalf("unload calls=%d, want 1", unloads.Load())
				}
				if scenario.allowed {
					if response.Code != http.StatusOK || forwarded.Load() != 1 {
						t.Fatalf("safe swap refused: status=%d forwarded=%d body=%s", response.Code, forwarded.Load(), response.Body.String())
					}
				} else if response.Code < 400 || forwarded.Load() != 0 {
					t.Fatalf("unsafe swap reached runtime: status=%d forwarded=%d body=%s", response.Code, forwarded.Load(), response.Body.String())
				}
				if !scenario.allowed && !scenario.unloadFail && !scenario.stillReady && !scenario.routerFail && !strings.Contains(response.Body.String(), "capacity") && surface != "anthropic" {
					t.Fatalf("capacity refusal is not explicit: %s", response.Body.String())
				}
			})
		}
	}
}

func TestMeasuredCanaryRefusesUnavailableMemoryProbe(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:9292")
	cfg.Models[0].PeakCommitGiB = floatPointer(5)
	status := capacityFrom(cfg.Models[0], cfg.Models, map[string]string{}, nil, memorySnapshot{}, errCapacityUnavailable)
	if status.Available || status.Reason != "resource_measurement_required" {
		t.Fatalf("known resource requirement bypassed failed probe: %+v", status)
	}
}

func TestConfigRejectsConcurrentInferenceDuringModelLifecycle(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:9292")
	cfg.MaxActive = 2
	if err := cfg.Validate(); err == nil {
		t.Fatal("concurrent inference would allow a swap to unload another active request")
	}
}
