package mcpserver

import (
	"context"
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
