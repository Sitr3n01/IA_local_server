package edge

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

func controlRequest(t *testing.T, handler http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:8091"+path, body)
	request.Host = "127.0.0.1:8091"
	request.Header.Set("Authorization", "Bearer "+testAdminToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestModelControlMapsToLlamaSwapAndUsesRouterCredential(t *testing.T) {
	type call struct {
		Method string
		Path   string
		Auth   string
	}
	var mu sync.Mutex
	var calls []call
	active := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, call{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization")})
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/running":
			w.Header().Set("Content-Type", "application/json")
			if active {
				_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready","cmd":"must-not-leak"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"running":[]}`)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/upstream/local-coding/health":
			active = true
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/models/unload/local-coding":
			active = false
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodPost && r.URL.Path == "/api/models/unload":
			active = false
			_, _ = io.WriteString(w, `{"msg":"ok"}`)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer backend.Close()
	server, err := New(testConfig(backend.URL))
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(32, 24)
	handler := server.ControlHandler()

	for _, operation := range []string{"load", "unload", "switch"} {
		recorder := controlRequest(t, handler, http.MethodPost, "/api/v1/models/local-coding:"+operation, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body=%s", operation, recorder.Code, recorder.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s result: %v", operation, err)
		}
		if result["operation"] != operation || result["status"] != "completed" {
			t.Fatalf("unexpected %s result: %v", operation, result)
		}
		if operation != "unload" && result["active_model"] != "local-coding" {
			t.Fatalf("%s active model = %v", operation, result["active_model"])
		}
	}

	mu.Lock()
	defer mu.Unlock()
	wanted := map[string]bool{
		"GET /upstream/local-coding/health":    false,
		"POST /api/models/unload/local-coding": false,
		"POST /api/models/unload":              false,
	}
	for _, item := range calls {
		key := item.Method + " " + item.Path
		if _, tracked := wanted[key]; tracked {
			wanted[key] = true
		}
		if item.Auth != "Bearer "+testRouterToken {
			t.Fatalf("router call %s used Authorization %q", key, item.Auth)
		}
	}
	for route, seen := range wanted {
		if !seen {
			t.Errorf("llama-swap route was not called: %s", route)
		}
	}
}

func TestModelControlRejectsUnknownEncodedModelAndBody(t *testing.T) {
	var upstreamCalls int
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	handler := server.ControlHandler()

	unknown := controlRequest(t, handler, http.MethodPost, "/api/v1/models/"+url.PathEscape("local/coding")+":load", nil)
	if unknown.Code != http.StatusNotFound || errorCode(t, unknown) != "model_not_found" {
		t.Fatalf("encoded unknown model: status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	withBody := controlRequest(t, handler, http.MethodPost, "/api/v1/models/local-coding:load", strings.NewReader(`{"force":true}`))
	if withBody.Code != http.StatusBadRequest {
		t.Fatalf("non-empty control body status = %d; body=%s", withBody.Code, withBody.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("rejected control requests reached upstream %d times", upstreamCalls)
	}
}

func TestModelControlReturnsConflictWhileInferenceGateBusy(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	release, err := server.gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	recorder := controlRequest(t, server.ControlHandler(), http.MethodPost, "/api/v1/models/local-coding:unload", nil)
	if recorder.Code != http.StatusConflict || errorCode(t, recorder) != "inference_busy" {
		t.Fatalf("busy control response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSwitchFinishesAfterPanelRequestIsCanceled(t *testing.T) {
	unloadStarted := make(chan struct{})
	allowUnload := make(chan struct{})
	healthCalled := make(chan struct{})
	active := "local-coding"
	var activeMu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/running":
			activeMu.Lock()
			model := active
			activeMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if model == "" {
				_, _ = io.WriteString(w, `{"running":[]}`)
			} else {
				_, _ = io.WriteString(w, `{"running":[{"model":"`+model+`","state":"ready"}]}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/models/unload":
			close(unloadStarted)
			select {
			case <-allowUnload:
				activeMu.Lock()
				active = ""
				activeMu.Unlock()
				_, _ = io.WriteString(w, `{"msg":"ok"}`)
			case <-r.Context().Done():
				return
			}
		case r.Method == http.MethodGet && r.URL.Path == "/upstream/local-fast/health":
			activeMu.Lock()
			active = "local-fast"
			activeMu.Unlock()
			close(healthCalled)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	cfg.HeaderTimeout = 5 * time.Second
	cfg.Models = append(cfg.Models, Model{
		ID: "local-fast", Object: "model", OwnedBy: "local", State: "candidate", Deployments: []string{"canary"},
	})
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(32, 24)

	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091/api/v1/models/local-fast:switch", nil).WithContext(requestContext)
	request.Host = "127.0.0.1:8091"
	request.Header.Set("Authorization", "Bearer "+testAdminToken)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ControlHandler().ServeHTTP(recorder, request)
	}()

	select {
	case <-unloadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("switch did not begin unload")
	}
	cancelRequest()
	close(allowUnload)
	select {
	case <-healthCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("switch stopped after unload when the panel request was canceled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("switch handler did not finish")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("switch status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestStatusReadsActiveModelWithoutExposingRouterDetails(t *testing.T) {
	var authorization string
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready","cmd":"private command","proxy":"private path"}]}`)
	}))
	server.memoryStatus = fixedMemory(20, 24)
	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	if authorization != "Bearer "+testRouterToken {
		t.Fatalf("/running router auth = %q", authorization)
	}
	if !strings.Contains(recorder.Body.String(), `"active_model":"local-coding"`) {
		t.Fatalf("active model missing: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private command") || strings.Contains(recorder.Body.String(), "private path") {
		t.Fatalf("router details leaked into status: %s", recorder.Body.String())
	}
}

// The load/switch path built its own refusal string instead of using
// capacityMessage, so an operator saw "commit headroom" for a VRAM overrun or an
// incomplete profile. This pins every reason to distinct, accurate text.
func TestModelControlRefusalMessageFollowsReason(t *testing.T) {
	backend, _ := runningBackend(t, `{"running":[]}`)
	commit, vram, device, ram := 8.0, 15.2, 15.92, 6.0

	for _, testCase := range []struct {
		name    string
		apply   func(*Model)
		memory  func() (memorySnapshot, error)
		wantSub string
		notSub  string
	}{
		{
			name: "vram budget overrun",
			apply: func(m *Model) {
				m.PeakCommitGiB, m.PeakVRAMGiB, m.DeviceVRAMGiB = &commit, &vram, &device
			},
			memory:  fixedMemory(60, 40),
			wantSub: "VRAM budget",
			notSub:  "commit headroom",
		},
		{
			name: "incomplete profile",
			apply: func(m *Model) {
				m.OffloadsTensors = true
				m.PeakCommitGiB = &commit
			},
			memory:  fixedMemory(60, 40),
			wantSub: "resource profile is incomplete",
			notSub:  "commit headroom",
		},
		{
			name: "physical memory shortfall",
			apply: func(m *Model) {
				m.PeakCommitGiB, m.PeakRAMGiB = &commit, &ram
			},
			memory:  fixedMemory(60, 7),
			wantSub: "physical memory",
			notSub:  "commit headroom",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := testConfig(backend.URL)
			testCase.apply(&cfg.Models[0])
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server.memoryStatus = testCase.memory

			recorder := controlRequest(t, server.ControlHandler(), http.MethodPost, "/api/v1/models/local-coding:load", nil)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("load status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, testCase.wantSub) {
				t.Errorf("refusal does not mention %q: %s", testCase.wantSub, body)
			}
			if strings.Contains(body, testCase.notSub) {
				t.Errorf("refusal still blames %q: %s", testCase.notSub, body)
			}
		})
	}
}
