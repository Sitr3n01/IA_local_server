package edge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// maintenanceBackend is a router stand-in that blocks an inference until the
// test releases it, so a drain can be observed with work genuinely in flight.
func maintenanceBackend(hold <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/running":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":[]}`)
		case r.URL.Path == "/v1/chat/completions":
			if hold != nil {
				<-hold
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion"}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		}
	})
}

func maintenanceState(t *testing.T, recorder *httptest.ResponseRecorder) maintenanceSnapshot {
	t.Helper()
	var payload struct {
		Operation   string              `json:"operation"`
		Status      string              `json:"status"`
		Maintenance maintenanceSnapshot `json:"maintenance"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode maintenance response: %v; body=%s", err, recorder.Body.String())
	}
	if payload.Status != "completed" {
		t.Fatalf("maintenance status = %q, want completed", payload.Status)
	}
	return payload.Maintenance
}

func TestDrainRefusesNewInferenceAndResumeRestoresIt(t *testing.T) {
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)
	control := server.ControlHandler()
	data := server.DataHandler()

	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	if recorder := dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body); recorder.Code != http.StatusOK {
		t.Fatalf("baseline inference status = %d; body=%s", recorder.Code, recorder.Body.String())
	}

	drained := maintenanceState(t, controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:drain", nil))
	if drained.State != maintenanceDrained || !drained.Draining || !drained.Drained {
		t.Fatalf("unexpected drain snapshot: %+v", drained)
	}
	if drained.Since == "" || drained.Seconds == nil {
		t.Fatalf("drain snapshot omits its start time: %+v", drained)
	}

	refused := dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body)
	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("drained inference status = %d, want 503; body=%s", refused.Code, refused.Body.String())
	}
	if code := errorCode(t, refused); code != "maintenance_draining" {
		t.Fatalf("drained inference error code = %q", code)
	}
	if retry := refused.Header().Get("Retry-After"); retry != maintenanceRetryAfterS {
		t.Fatalf("drained inference Retry-After = %q, want %q", retry, maintenanceRetryAfterS)
	}

	// A drained provider is deliberately not ready, so anything gating traffic
	// on readiness stops sending it work.
	ready := controlRequest(t, control, http.MethodGet, "/readyz", nil)
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("drained readiness status = %d, want 503", ready.Code)
	}

	// Reads stay available: the model list is side-effect free and a harness
	// still needs it during maintenance.
	if models := dataRequest(t, data, http.MethodGet, "/v1/models", nil); models.Code != http.StatusOK {
		t.Fatalf("drained model list status = %d", models.Code)
	}

	resumed := maintenanceState(t, controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:resume", nil))
	if resumed.State != maintenanceRunning || resumed.Draining || resumed.Since != "" {
		t.Fatalf("unexpected resume snapshot: %+v", resumed)
	}
	if recorder := dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body); recorder.Code != http.StatusOK {
		t.Fatalf("resumed inference status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDrainPreservesActiveAndQueuedRequests(t *testing.T) {
	hold := make(chan struct{})
	server, _ := newTestServer(t, maintenanceBackend(hold))
	server.memoryStatus = fixedMemory(32, 24)
	server.cfg.QueueWait = 5 * time.Second
	server.gate = newGate(1, 4, 5*time.Second)
	control := server.ControlHandler()
	data := server.DataHandler()

	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	results := make(chan int, 2)
	for range 2 {
		go func() {
			results <- dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body).Code
		}()
	}

	// Wait until one request holds the slot and the other is queued behind it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot := server.gate.snapshot()
		if snapshot.Active == 1 && snapshot.Queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("requests never reached active=1 queued=1: %+v", snapshot)
		}
		time.Sleep(2 * time.Millisecond)
	}

	draining := maintenanceState(t, controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:drain", nil))
	if draining.State != maintenanceDraining || draining.Drained {
		t.Fatalf("drain with work in flight reported %+v", draining)
	}
	if draining.Active != 1 || draining.Queued != 1 {
		t.Fatalf("drain snapshot lost in-flight counts: %+v", draining)
	}

	// A third request arriving after the drain is refused; the two already in
	// the system are not.
	if refused := dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body); refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("post-drain admission status = %d, want 503", refused.Code)
	}

	close(hold)
	for range 2 {
		select {
		case code := <-results:
			if code != http.StatusOK {
				t.Fatalf("in-flight request finished with %d, want 200", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("in-flight request never completed after the drain")
		}
	}

	// Once both finish the provider reports the drained state the deployment
	// transaction waits for.
	final := server.gate.maintenance(time.Now())
	if final.State != maintenanceDrained || !final.Drained || final.Active != 0 || final.Queued != 0 {
		t.Fatalf("provider never reached the drained state: %+v", final)
	}
}

func TestMaintenanceRequiresAdminAndRejectsBodiesAndQueries(t *testing.T) {
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)
	control := server.ControlHandler()

	unauthorized := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091/api/v1/maintenance:drain", nil)
	unauthorized.Host = "127.0.0.1:8091"
	unauthorized.Header.Set("Authorization", "Bearer "+testInferenceToken)
	recorder := httptest.NewRecorder()
	control.ServeHTTP(recorder, unauthorized)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("inference credential accepted for drain: %d", recorder.Code)
	}
	if server.gate.maintenance(time.Now()).Draining {
		t.Fatal("unauthorized request changed the maintenance state")
	}

	if got := controlRequest(t, control, http.MethodGet, "/api/v1/maintenance:drain", nil); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET drain status = %d, want 405", got.Code)
	}
	if got := controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:pause", nil); got.Code != http.StatusNotFound {
		t.Fatalf("unknown maintenance verb status = %d, want 404", got.Code)
	}
	if got := controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:drain", strings.NewReader(`{}`)); got.Code != http.StatusBadRequest {
		t.Fatalf("drain with a body status = %d, want 400", got.Code)
	}

	query := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091/api/v1/maintenance:drain?force=1", nil)
	query.Host = "127.0.0.1:8091"
	query.Header.Set("Authorization", "Bearer "+testAdminToken)
	queryRecorder := httptest.NewRecorder()
	control.ServeHTTP(queryRecorder, query)
	if queryRecorder.Code != http.StatusBadRequest {
		t.Fatalf("drain with a query parameter status = %d, want 400", queryRecorder.Code)
	}
	if server.gate.maintenance(time.Now()).Draining {
		t.Fatal("a refused request changed the maintenance state")
	}
}

func TestModelControlPolicyWhileDraining(t *testing.T) {
	hold := make(chan struct{})
	server, _ := newTestServer(t, maintenanceBackend(hold))
	server.memoryStatus = fixedMemory(32, 24)
	control := server.ControlHandler()
	data := server.DataHandler()

	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	done := make(chan int, 1)
	go func() { done <- dataRequest(t, data, http.MethodPost, "/v1/chat/completions", body).Code }()

	deadline := time.Now().Add(3 * time.Second)
	for server.gate.snapshot().Active != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request never became active")
		}
		time.Sleep(2 * time.Millisecond)
	}
	controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:drain", nil)

	// Policy: draining does not relax the idle-gate requirement. A control
	// operation issued while a request is still running is refused rather than
	// allowed to kill it.
	busy := controlRequest(t, control, http.MethodPost, "/api/v1/models/local-coding:unload", nil)
	if busy.Code != http.StatusConflict {
		t.Fatalf("unload during an active request status = %d, want 409", busy.Code)
	}
	if code := errorCode(t, busy); code != "inference_busy" {
		t.Fatalf("unload during an active request code = %q", code)
	}

	close(hold)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("active request finished with %d", code)
	}

	// Once drained, the same operation is admitted. This is the maintenance
	// window the deployment transaction relies on.
	quiet := controlRequest(t, control, http.MethodPost, "/api/v1/models/local-coding:unload", nil)
	if quiet.Code != http.StatusOK {
		t.Fatalf("unload after drain status = %d; body=%s", quiet.Code, quiet.Body.String())
	}
}

func TestDrainIsIdempotentAndPreservesItsStartTime(t *testing.T) {
	gate := newGate(1, 1, time.Second)
	first := gate.drain(time.Now().Add(-90 * time.Second))
	second := gate.drain(time.Now())
	if first.Since != second.Since {
		t.Fatalf("repeated drain moved its start time: %q -> %q", first.Since, second.Since)
	}
	if second.Seconds == nil || *second.Seconds < 89 {
		t.Fatalf("repeated drain lost elapsed time: %+v", second.Seconds)
	}

	// Resume is idempotent too, and a restarted process comes back running.
	gate.resume()
	if again := gate.resume(); again.State != maintenanceRunning || again.Draining {
		t.Fatalf("repeated resume reported %+v", again)
	}
}

func TestGateRefusesAdmissionWhileDraining(t *testing.T) {
	gate := newGate(1, 1, time.Second)
	gate.drain(time.Now())
	if _, err := gate.acquire(context.Background()); !errors.Is(err, errDraining) {
		t.Fatalf("acquire error = %v, want draining", err)
	}
	if snapshot := gate.maintenance(time.Now()); snapshot.Rejected != 1 {
		t.Fatalf("drain rejection was not counted: %+v", snapshot)
	}
	gate.resume()
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after resume: %v", err)
	}
	release()
}
