package claudedesktop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testPolicy struct {
	managed bool
	err     error
}

func (p testPolicy) ManagedInference(context.Context) (bool, error) { return p.managed, p.err }

// testDesktop records what a launch would have seen: the selector each
// Activate call found on disk is the deployment the new process would start.
type testDesktop struct {
	paths     Paths
	shown     map[string]bool
	activated []string
	waited    []string
	waitErr   error
	// shownHook runs when the awaited window shows, as Desktop's own writes
	// to the selector's file do.
	shownHook func()
}

func (d *testDesktop) Show(_ context.Context, dataDir string) (bool, error) {
	return d.shown[dataDir], nil
}

func (d *testDesktop) Activate(context.Context) error {
	document, err := readJSONObject(d.paths.modePath())
	if err != nil {
		return err
	}
	selector, _ := document["deploymentMode"].(string)
	d.activated = append(d.activated, selector)
	return nil
}

func (d *testDesktop) WaitShown(_ context.Context, dataDir string) error {
	d.waited = append(d.waited, dataDir)
	if d.shownHook != nil {
		d.shownHook()
	}
	return d.waitErr
}

type testProtector struct{}

func (testProtector) Protect(data []byte) ([]byte, error) {
	return append([]byte("protected:"), data...), nil
}
func (testProtector) Unprotect(data []byte) ([]byte, error) {
	return append([]byte(nil), data[len("protected:"):]...), nil
}

func testManager(t *testing.T, policy PolicyReader) (*Manager, Paths, *testDesktop) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		ThirdPartyStateDir: filepath.Join(root, "Local", "Claude-3p"),
		FirstPartyConfig:   filepath.Join(root, "Roaming", "Claude", "claude_desktop_config.json"),
		BackupDir:          filepath.Join(root, "cia-state"),
	}
	desktop := &testDesktop{paths: paths, shown: map[string]bool{}}
	manager, err := NewManager(paths, policy, desktop, testProtector{})
	if err != nil {
		t.Fatal(err)
	}
	manager.settle = 0
	return manager, paths, desktop
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSelector(t *testing.T, paths Paths) string {
	t.Helper()
	document, err := readJSONObject(paths.modePath())
	if err != nil {
		t.Fatal(err)
	}
	selector, _ := document["deploymentMode"].(string)
	return selector
}

func gatewayForTest() Gateway {
	return Gateway{BaseURL: "http://127.0.0.1:18090", APIKey: "claude-gateway-test-token-000000000000"}
}

func TestOpenLocalLaunchesBesideTheSignedInInstanceAndRestsTheSelector(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	firstParty := `{"mcpServers":{"keep":{"command":"keep.exe"}}}`
	writeTestFile(t, paths.FirstPartyConfig, firstParty)
	writeTestFile(t, paths.metaPath(), `{"appliedId":"11111111-1111-4111-8111-111111111111","entries":[{"id":"11111111-1111-4111-8111-111111111111","name":"Other provider"}],"foreign":true}`)
	writeTestFile(t, paths.modePath(), `{"deploymentMode":"1p","preferences":{"kept":true}}`)
	// The signed-in instance is running and must be neither shown nor touched.
	desktop.shown[paths.firstPartyDataDir()] = true

	if err := manager.OpenLocal(context.Background(), gatewayForTest()); err != nil {
		t.Fatal(err)
	}
	if len(desktop.activated) != 1 || desktop.activated[0] != selectorLocal {
		t.Fatalf("launches saw selectors %v, want one launch at 3p", desktop.activated)
	}
	if len(desktop.waited) != 1 || desktop.waited[0] != paths.ThirdPartyStateDir {
		t.Fatalf("waited for %v, want the local instance's data directory", desktop.waited)
	}
	if selector := readSelector(t, paths); selector != selectorAnthropic {
		t.Fatalf("selector after the launch is %q, want 1p", selector)
	}
	mode, _ := os.ReadFile(paths.modePath())
	if !strings.Contains(string(mode), `"kept": true`) {
		t.Fatalf("selector rewrite dropped Desktop's own keys: %s", mode)
	}
	firstPartyAfter, err := os.ReadFile(paths.FirstPartyConfig)
	if err != nil || string(firstPartyAfter) != firstParty {
		t.Fatalf("first-party profile changed: %q err=%v", firstPartyAfter, err)
	}
	profile, err := os.ReadFile(paths.profilePath())
	if err != nil {
		t.Fatal(err)
	}
	profileText := string(profile)
	for _, wanted := range []string{`"inferenceProvider": "gateway"`, `"inferenceGatewayBaseUrl": "http://127.0.0.1:18090"`, `"inferenceGatewayAuthScheme": "bearer"`} {
		if !strings.Contains(profileText, wanted) {
			t.Errorf("profile missing %s: %s", wanted, profileText)
		}
	}
	if strings.Contains(profileText, "inferenceModels") {
		t.Fatalf("local profile pinned models instead of allowing dynamic discovery: %s", profileText)
	}
	meta, err := os.ReadFile(paths.metaPath())
	if err != nil || !strings.Contains(string(meta), `"foreign": true`) || !strings.Contains(string(meta), profileID) {
		t.Fatalf("metadata did not preserve foreign entry/state: %s err=%v", meta, err)
	}
	if err := manager.ReadBackup(); err != nil {
		t.Fatalf("DPAPI-equivalent backup did not verify: %v", err)
	}
}

func TestOpenLocalRestsTheSelectorWhenTheLaunchFails(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	desktop.waitErr = errors.New("no window")
	if err := manager.OpenLocal(context.Background(), gatewayForTest()); err == nil || !strings.Contains(err.Error(), "no window") {
		t.Fatalf("OpenLocal error=%v", err)
	}
	if selector := readSelector(t, paths); selector != selectorAnthropic {
		t.Fatalf("a failed launch left the selector at %q, want 1p", selector)
	}
}

func TestOpenLocalPutsTheSelectorBackWhenDesktopRewritesAStaleRead(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	manager.settle = time.Second
	// The new instance read the file while it said 3p and writes it back
	// after OpenLocal has already returned it to 1p.
	desktop.shownHook = func() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = writeAtomic(paths.modePath(), []byte(`{"deploymentMode":"3p","preferences":{"earlyWindowShowLatched":true}}`))
		}()
	}
	if err := manager.OpenLocal(context.Background(), gatewayForTest()); err != nil {
		t.Fatal(err)
	}
	if selector := readSelector(t, paths); selector != selectorAnthropic {
		t.Fatalf("a stale Desktop write left the selector at %q, want 1p", selector)
	}
	mode, _ := os.ReadFile(paths.modePath())
	if !strings.Contains(string(mode), "earlyWindowShowLatched") {
		t.Fatalf("putting 1p back dropped Desktop's own preference: %s", mode)
	}
}

func TestOpenLocalShowsARunningLocalInstanceWithoutLaunching(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	gateway := gatewayForTest()
	if err := manager.OpenLocal(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	backupBefore, err := os.ReadFile(paths.backupPath())
	if err != nil {
		t.Fatal(err)
	}
	desktop.shown[paths.ThirdPartyStateDir] = true
	if err := manager.OpenLocal(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	if len(desktop.activated) != 1 {
		t.Fatalf("a running local instance was launched again: %v", desktop.activated)
	}
	backupAfter, err := os.ReadFile(paths.backupPath())
	if err != nil || string(backupAfter) != string(backupBefore) {
		t.Fatalf("an applied profile was backed up and written again: err=%v", err)
	}
}

func TestOpenLocalStopsBeforeBackupWhenPolicyIsManaged(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{managed: true})
	if err := manager.OpenLocal(context.Background(), gatewayForTest()); err == nil || !strings.Contains(err.Error(), "managed inference policy") {
		t.Fatalf("OpenLocal error=%v", err)
	}
	if len(desktop.activated) != 0 {
		t.Fatalf("managed precheck launched Desktop %d times", len(desktop.activated))
	}
	if _, err := os.Stat(paths.backupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed precheck created backup: %v", err)
	}
}

func TestOpenAnthropicRestsALeftoverLocalSelectorBeforeLaunching(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	writeTestFile(t, paths.profilePath(), `{"inferenceProvider":"gateway","custom":true}`)
	writeTestFile(t, paths.modePath(), `{"deploymentMode":"3p","old":true}`)
	if err := manager.OpenAnthropic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(desktop.activated) != 1 || desktop.activated[0] != selectorAnthropic {
		t.Fatalf("launches saw selectors %v, want one launch at 1p", desktop.activated)
	}
	if len(desktop.waited) != 1 || desktop.waited[0] != paths.firstPartyDataDir() {
		t.Fatalf("waited for %v, want the signed-in instance's data directory", desktop.waited)
	}
	profile, _ := os.ReadFile(paths.profilePath())
	if string(profile) != `{"inferenceProvider":"gateway","custom":true}` {
		t.Fatalf("opening the signed-in instance rewrote the isolated provider profile: %s", profile)
	}
	mode, _ := os.ReadFile(paths.modePath())
	if !strings.Contains(string(mode), `"old": true`) {
		t.Fatalf("selector rewrite dropped Desktop's own keys: %s", mode)
	}
}

func TestOpenAnthropicTreatsAMissingSelectorAsLocalOnlyWithAnAppliedProfile(t *testing.T) {
	manager, paths, _ := testManager(t, testPolicy{})
	if err := manager.OpenAnthropic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.ThirdPartyStateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening the signed-in instance created 3P state on a clean machine: %v", err)
	}

	writeTestFile(t, paths.metaPath(), `{"appliedId":"`+profileID+`","entries":[]}`)
	if err := manager.OpenAnthropic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if selector := readSelector(t, paths); selector != selectorAnthropic {
		t.Fatalf("a missing selector with an applied local profile stayed %q, want 1p", selector)
	}
}

func TestOpenAnthropicShowsTheRunningInstanceWithoutLaunching(t *testing.T) {
	manager, paths, desktop := testManager(t, testPolicy{})
	writeTestFile(t, paths.modePath(), `{"deploymentMode":"1p"}`)
	desktop.shown[paths.firstPartyDataDir()] = true
	if err := manager.OpenAnthropic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(desktop.activated) != 0 {
		t.Fatalf("a running signed-in instance was launched again: %v", desktop.activated)
	}
	if _, err := os.Stat(paths.backupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening the signed-in instance created a backup: %v", err)
	}
}

func TestGatewayValidationRejectsAnythingButLoopbackOrigin(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:18090", "http://localhost:18090", "http://192.168.1.5:18090", "http://127.0.0.1:18090/v1"} {
		gateway := gatewayForTest()
		gateway.BaseURL = raw
		if err := gateway.Validate(); err == nil {
			t.Errorf("Gateway(%q) unexpectedly passed", raw)
		}
	}
}

func TestProbeGatewayUsesDedicatedBearerCredential(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%s, want /v1/models", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "1000" || len(r.URL.Query()) != 1 {
			t.Errorf("query=%q, want limit=1000 only", r.URL.RawQuery)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer claude-gateway-test-token-000000000000"; got != want {
			t.Errorf("Authorization=%q, want %q", got, want)
		}
		if got, want := r.Header.Get("anthropic-version"), "2023-06-01"; got != want {
			t.Errorf("anthropic-version=%q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model","anthropic_family_tier":"sonnet"}]}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	gateway := gatewayForTest()
	gateway.BaseURL = server.URL
	if err := ProbeGateway(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
}

func TestProbeGatewayRejectsModelsClaudeDesktopWouldFilter(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"real-local-id"}]}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	gateway := gatewayForTest()
	gateway.BaseURL = server.URL
	if err := ProbeGateway(context.Background(), gateway); err == nil || !strings.Contains(err.Error(), "no eligible models") {
		t.Fatalf("ProbeGateway error=%v, want Claude eligibility failure", err)
	}
}

func TestProbeModelRunsAuthenticatedAnthropicGenerationWithoutReturningText(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("request=%s %s, want POST /v1/messages", r.Method, r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer claude-gateway-test-token-000000000000"; got != want {
			t.Errorf("Authorization=%q, want %q", got, want)
		}
		if got, want := r.Header.Get("anthropic-version"), "2023-06-01"; got != want {
			t.Errorf("anthropic-version=%q, want %q", got, want)
		}
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []any  `json:"messages"`
			Tools     []any  `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "claude-local-model" || len(request.Messages) != 1 || len(request.Tools) != 1 {
			t.Errorf("probe request=%+v", request)
		}
		if request.MaxTokens != 2048 {
			t.Errorf("probe max_tokens=%d, want 2048", request.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","model":"claude-local-model","stop_reason":"end_turn","content":[{"type":"text","text":"sensitive model output"}]}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	gateway := gatewayForTest()
	gateway.BaseURL = server.URL
	result, err := ProbeModel(context.Background(), gateway, "claude-local-model")
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "claude-local-model" || result.StopReason != "end_turn" || len(result.ContentKinds) != 1 || result.ContentKinds[0] != "text" {
		t.Fatalf("unexpected model probe result: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sensitive model output") {
		t.Fatalf("probe result leaked response text: %s", encoded)
	}
}

func TestProbeModelRejectsEmptyContent(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	gateway := gatewayForTest()
	gateway.BaseURL = server.URL
	if _, err := ProbeModel(context.Background(), gateway, "claude-local-model"); err == nil || !strings.Contains(err.Error(), "no content blocks") {
		t.Fatalf("ProbeModel error=%v, want empty-content failure", err)
	}
}
