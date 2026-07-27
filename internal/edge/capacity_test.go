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
