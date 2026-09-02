package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewControlClientRejectsNonLoopback(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://127.0.0.1:8091",
		"http://192.168.1.10:8091",
		"http://example.com:8091",
		"http://localhost:8091",
		"http://user:pass@127.0.0.1:8091",
		"http://127.0.0.1:8091/control",
		"http://127.0.0.1:8091?token=secret",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := NewControlClient(Config{ControlURL: rawURL}, "test")
			if err == nil {
				t.Fatalf("NewControlClient(%q) succeeded, want error", rawURL)
			}
		})
	}
}

func TestControlClientStatusSendsNoCredentialAndNormalizesSlices(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no credential on read-only status", got)
		}
		if got := r.Header.Get("User-Agent"); got != "cia-mcp/test-version" {
			t.Errorf("User-Agent = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"service":"cia-edge",
			"version":"dev",
			"ready":true,
			"upstream":{"url":"http://127.0.0.1:19292","reachable":true},
			"models":null,
			"active_model":"",
			"gate":{"active":0,"queued":0,"max_active":1,"max_queue":4,"wait_timeout_seconds":120},
			"capacity":{"admission":"configured-profile","available":true},
			"recent_events":null
		}`)
	}))
	defer server.Close()

	client, err := NewControlClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
	}, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Models == nil || status.RecentEvents == nil {
		t.Fatal("nil slices were not normalized to empty slices")
	}
	if status.Gate.MaxQueue != 4 || !status.Capacity.Available {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestControlClientHealthAcceptsNotReady(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("health request sent Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/livez":
			_, _ = fmt.Fprint(w, `{"status":"ok","service":"cia-edge"}`)
		case "/readyz":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"status":"not_ready","service":"cia-edge","upstream_reachable":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewControlClient(Config{ControlURL: server.URL, Timeout: time.Second}, "test")
	if err != nil {
		t.Fatal(err)
	}
	live, err := client.Liveness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := client.Readiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live.Status != "ok" || ready.Status != "not_ready" || ready.UpstreamReachable == nil || *ready.UpstreamReachable {
		t.Fatalf("unexpected probes: live=%+v ready=%+v", live, ready)
	}
}

func TestControlClientDoesNotFollowRedirectOrExposeBody(t *testing.T) {
	t.Parallel()

	const sensitiveBody = "Bearer must-never-appear"
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			http.Redirect(w, r, "/redirected", http.StatusFound)
			_, _ = fmt.Fprint(w, sensitiveBody)
		case "/redirected":
			redirected.Add(1)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewControlClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background())
	if err == nil {
		t.Fatal("Status succeeded through redirect")
	}
	if redirected.Load() != 0 {
		t.Fatal("control client followed redirect")
	}
	if strings.Contains(err.Error(), sensitiveBody) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposed sensitive data: %v", err)
	}
}

// StatusRaw exists because Status is a lossy projection, and this test pins
// both halves of that: the same response body, read twice, must survive
// intact through StatusRaw and must NOT survive intact through Status.
//
// The second assertion is the important one. A test that only checked
// StatusRaw would still pass if someone later "simplified" the console back
// onto Status, which is exactly the regression that shipped a console
// rendering an error state while the bridge reported success. Asserting that
// Status really does drop these keys keeps the reason StatusRaw exists
// visible in the test suite rather than only in a comment.
func TestControlClientStatusRawPreservesFieldsStatusDrops(t *testing.T) {
	t.Parallel()

	// Every key here is one cia-edge sends and the frontend's Zod schema
	// requires, but mcpserver.Status does not declare.
	const body = `{
		"service":"cia-edge",
		"version":"dev",
		"ready":true,
		"uptime_seconds":1234,
		"upstream":{"url":"http://127.0.0.1:19292","reachable":true},
		"models":[{"id":"m1","object":"model","owned_by":"local","display_name":"M One","capabilities":{"chat_completions":true}}],
		"runtimes":[{"id":"r1","state":"idle","engine":"llama","variant":"rocm","backend":"hip","artifact_sha256_prefix":"abc","checkpoint_capable":false}],
		"active_model":"",
		"gate":{"active":0,"queued":0,"max_active":1,"max_queue":4,"wait_timeout_seconds":120,"rejected_total":0,"timed_out_total":0},
		"capacity":{"admission":"ok","model":"m1","model_running":false,"commit_headroom_gib":1.0,"required_commit_gib":2.0,"reserve_commit_gib":3.0,"physical_headroom_gib":4.0,"required_physical_gib":5.0,"reserve_physical_gib":6.0,"required_vram_gib":7.0,"device_vram_gib":8.0,"reserve_vram_gib":9.0,"measured":true,"available":true},
		"gpu_memory":{"state":"measured","dedicated_mib":100,"shared_mib":200},
		"maintenance":{"state":"active","draining":false,"drained":false,"active":0,"queued":0,"rejected_total":0},
		"model_statuses":[],
		"recent_events":[]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no credential on a read-only status", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()

	client, err := NewControlClient(Config{ControlURL: server.URL}, "test")
	if err != nil {
		t.Fatalf("NewControlClient: %v", err)
	}

	// The keys the console's schema requires and Status omits.
	dropped := []string{
		"uptime_seconds",
		"runtimes",
		"gpu_memory",
		"maintenance",
		"display_name",
		"capabilities",
		"physical_headroom_gib",
		"required_physical_gib",
		"reserve_physical_gib",
		"required_vram_gib",
		"device_vram_gib",
		"reserve_vram_gib",
	}

	raw, err := client.StatusRaw(context.Background())
	if err != nil {
		t.Fatalf("StatusRaw: %v", err)
	}
	for _, key := range dropped {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("StatusRaw dropped %q; it must carry the body verbatim", key)
		}
	}

	typed, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	reencoded, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("marshal Status: %v", err)
	}
	for _, key := range dropped {
		if strings.Contains(string(reencoded), `"`+key+`"`) {
			t.Errorf("Status unexpectedly preserved %q - if the struct grew this field, "+
				"update this test's expectations deliberately rather than deleting the assertion", key)
		}
	}
}

// A well-formed JSON body that is not an object is refused rather than handed
// on to the page, which expects an object and would fail less clearly.
func TestControlClientStatusRawRejectsNonObjectBody(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`[]`, `"a string"`, `42`, `null`} {
		body := body
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			client, err := NewControlClient(Config{ControlURL: server.URL}, "test")
			if err != nil {
				t.Fatalf("NewControlClient: %v", err)
			}
			if _, err := client.StatusRaw(context.Background()); err == nil {
				t.Fatalf("StatusRaw(%s) succeeded, want an error", body)
			}
		})
	}
}
