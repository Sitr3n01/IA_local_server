package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sitr3n/local-ai-provider/internal/adminpipe"
)

func TestEnvironmentDefaultsStayOnLoopbackAndBesideTheirEdge(t *testing.T) {
	for name, defaults := range environmentDefaults {
		host, _, err := net.SplitHostPort(defaults.listen)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			t.Errorf("%s listen = %q, want a literal loopback IP with a port", name, defaults.listen)
		}
		for label, raw := range map[string]string{"control": defaults.control, "data": defaults.data} {
			if !strings.HasPrefix(raw, "http://127.0.0.1:") {
				t.Errorf("%s %s = %q, want a loopback http URL", name, label, raw)
			}
		}
	}
	if environmentDefaults["canary"].listen == environmentDefaults["final"].listen {
		t.Error("canary and final share a listen address, so both could not be watched at once")
	}
}

func TestControlOptions(t *testing.T) {
	edge := filepath.Join(t.TempDir(), "cia-edge.exe")

	t.Run("off disables the buttons without validating the server", func(t *testing.T) {
		got, err := controlOptions("canary", "off", "not-absolute")
		if err != nil || got.Pipe != "" || got.Server != "" {
			t.Fatalf("controlOptions(off) = %+v, %v; want zero options", got, err)
		}
	})

	t.Run("auto and empty resolve the environment's pipe", func(t *testing.T) {
		for _, pipe := range []string{"auto", "", "  auto  "} {
			got, err := controlOptions("canary", pipe, edge)
			if err != nil {
				t.Fatalf("controlOptions(%q): %v", pipe, err)
			}
			if want := adminpipe.DefaultName("canary"); got.Pipe != want || got.Server != edge {
				t.Errorf("controlOptions(%q) = %+v, want pipe %q server %q", pipe, got, want, edge)
			}
		}
	})

	t.Run("an explicit pipe must be a named pipe path", func(t *testing.T) {
		if _, err := controlOptions("canary", `C:\temp\pipe`, edge); err == nil {
			t.Error("a filesystem path was accepted as the administrative pipe")
		}
		got, err := controlOptions("canary", `\\.\pipe\custom`, edge)
		if err != nil || got.Pipe != `\\.\pipe\custom` {
			t.Errorf("controlOptions(explicit) = %+v, %v", got, err)
		}
	})

	t.Run("the pinned server must be an absolute executable", func(t *testing.T) {
		for _, server := range []string{"", "cia-edge.exe", `C:\IA\local-ai-v2\bin\cia-edge`, `C:\IA\local-ai-v2\bin\cia-edge.dll`} {
			if _, err := controlOptions("canary", `\\.\pipe\custom`, server); err == nil {
				t.Errorf("server %q was accepted", server)
			}
		}
	})
}

func TestIsMonitor(t *testing.T) {
	serve := func(handler http.HandlerFunc) string {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return strings.TrimPrefix(server.URL, "http://")
	}

	t.Run("a snapshot carrying a monitor version counts", func(t *testing.T) {
		address := serve(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/snapshot" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"monitor":{"version":"dev-abc"}}`))
		})
		if !isMonitor(address) {
			t.Error("a real snapshot was not recognised as a monitor")
		}
	})

	for name, body := range map[string]string{
		"a snapshot without a version": `{"monitor":{}}`,
		"an unrelated JSON document":   `{"hello":"world"}`,
		"a non-JSON page":              `<html>hi</html>`,
	} {
		t.Run(name+" does not count", func(t *testing.T) {
			address := serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			if isMonitor(address) {
				t.Errorf("%s was recognised as a monitor", name)
			}
		})
	}

	t.Run("a non-200 status does not count", func(t *testing.T) {
		address := serve(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusTeapot) })
		if isMonitor(address) {
			t.Error("an error response was recognised as a monitor")
		}
	})

	t.Run("nothing listening does not count", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		if isMonitor(address) {
			t.Error("a closed port was recognised as a monitor")
		}
	})
}
