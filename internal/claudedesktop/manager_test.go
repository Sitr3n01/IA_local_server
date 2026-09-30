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
)

type testPolicy struct {
	managed bool
	err     error
}

func (p testPolicy) ManagedInference(context.Context) (bool, error) { return p.managed, p.err }

type testRestarter struct {
	calls int
	err   error
}

func (r *testRestarter) Restart(context.Context, Identity) error {
	r.calls++
	return r.err
}

type testVerifier struct{ err error }

func (v testVerifier) Verify(context.Context, Mode, Gateway) error { return v.err }

type testProtector struct{}

func (testProtector) Protect(data []byte) ([]byte, error) {
	return append([]byte("protected:"), data...), nil
}
func (testProtector) Unprotect(data []byte) ([]byte, error) {
	return append([]byte(nil), data[len("protected:"):]...), nil
}

func testManager(t *testing.T, policy PolicyReader, restarter Restarter, verifier Verifier) (*Manager, Paths) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		ThirdPartyStateDir: filepath.Join(root, "Local", "Claude-3p"),
		FirstPartyConfig:   filepath.Join(root, "Roaming", "Claude", "claude_desktop_config.json"),
		BackupDir:          filepath.Join(root, "cia-state"),
	}
	manager, err := NewManager(paths, Identity{PackageFamilyName: "Claude_test", AppUserModelID: "Claude_test!Claude", Version: "1.37937.0.0", InstallLocation: filepath.Join(root, "WindowsApps", "Claude_test")}, policy, restarter, verifier, testProtector{})
	if err != nil {
		t.Fatal(err)
	}
	return manager, paths
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

func gatewayForTest() Gateway {
	return Gateway{BaseURL: "http://127.0.0.1:18090", APIKey: "claude-gateway-test-token-000000000000"}
}

func TestApplyLocalWritesOnlyIsolatedThirdPartyState(t *testing.T) {
	restarter := &testRestarter{}
	manager, paths := testManager(t, testPolicy{}, restarter, testVerifier{})
	firstParty := `{"mcpServers":{"keep":{"command":"keep.exe"}}}`
	writeTestFile(t, paths.FirstPartyConfig, firstParty)
	writeTestFile(t, paths.metaPath(), `{"appliedId":"11111111-1111-4111-8111-111111111111","entries":[{"id":"11111111-1111-4111-8111-111111111111","name":"Other provider"}],"foreign":true}`)
	writeTestFile(t, paths.modePath(), `{"unrelated":true}`)

	if err := manager.Apply(context.Background(), ModeLocal, gatewayForTest()); err != nil {
		t.Fatal(err)
	}
	if restarter.calls != 1 {
		t.Fatalf("restart calls=%d, want 1", restarter.calls)
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
	mode, err := os.ReadFile(paths.modePath())
	if err != nil || !strings.Contains(string(mode), `"deploymentMode": "3p"`) {
		t.Fatalf("mode=%s err=%v", mode, err)
	}
	meta, err := os.ReadFile(paths.metaPath())
	if err != nil || !strings.Contains(string(meta), `"foreign": true`) || !strings.Contains(string(meta), profileID) {
		t.Fatalf("metadata did not preserve foreign entry/state: %s err=%v", meta, err)
	}
	if err := manager.ReadBackup(); err != nil {
		t.Fatalf("DPAPI-equivalent backup did not verify: %v", err)
	}
}

func TestApplyRollsBackExactlyAfterVerificationFailure(t *testing.T) {
	restarter := &testRestarter{}
	manager, paths := testManager(t, testPolicy{}, restarter, testVerifier{err: errors.New("desktop did not reach gateway")})
	writeTestFile(t, paths.profilePath(), `{"old":"profile"}`)
	writeTestFile(t, paths.metaPath(), `{"appliedId":"old","entries":[]}`)
	writeTestFile(t, paths.modePath(), `{"deploymentMode":"1p","old":"mode"}`)
	beforeProfile, _ := os.ReadFile(paths.profilePath())
	beforeMeta, _ := os.ReadFile(paths.metaPath())
	beforeMode, _ := os.ReadFile(paths.modePath())
	if err := manager.Apply(context.Background(), ModeLocal, gatewayForTest()); err == nil || !strings.Contains(err.Error(), "desktop did not reach gateway") {
		t.Fatalf("Apply error=%v", err)
	}
	for path, before := range map[string][]byte{paths.profilePath(): beforeProfile, paths.metaPath(): beforeMeta, paths.modePath(): beforeMode} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Errorf("rollback did not restore %s: got %q err=%v want %q", path, after, err, before)
		}
	}
	if restarter.calls != 2 {
		t.Fatalf("restart calls=%d, want forward + rollback", restarter.calls)
	}
}

func TestApplyAnthropicOnlySelectsFirstPartyInThirdPartyState(t *testing.T) {
	restarter := &testRestarter{}
	manager, paths := testManager(t, testPolicy{}, restarter, testVerifier{})
	writeTestFile(t, paths.profilePath(), `{"inferenceProvider":"gateway","custom":true}`)
	writeTestFile(t, paths.modePath(), `{"old":true}`)
	if err := manager.Apply(context.Background(), ModeAnthropic, Gateway{}); err != nil {
		t.Fatal(err)
	}
	profile, _ := os.ReadFile(paths.profilePath())
	if string(profile) != `{"inferenceProvider":"gateway","custom":true}` {
		t.Fatalf("Anthropic mode rewrote isolated provider profile: %s", profile)
	}
	mode, _ := os.ReadFile(paths.modePath())
	if !strings.Contains(string(mode), `"deploymentMode": "1p"`) || !strings.Contains(string(mode), `"old": true`) {
		t.Fatalf("Anthropic selector did not preserve isolated state: %s", mode)
	}
}

func TestApplySameLocalModeIsIdempotentWhenLiveVerificationPasses(t *testing.T) {
	restarter := &testRestarter{}
	manager, _ := testManager(t, testPolicy{}, restarter, testVerifier{})
	gateway := gatewayForTest()
	if err := manager.Apply(context.Background(), ModeLocal, gateway); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply(context.Background(), ModeLocal, gateway); err != nil {
		t.Fatal(err)
	}
	if restarter.calls != 1 {
		t.Fatalf("repeated matching Local mode restarted Desktop %d times, want 1 initial restart", restarter.calls)
	}
}

func TestApplySameAnthropicModeDoesNotCreateBackupOrRestart(t *testing.T) {
	restarter := &testRestarter{}
	manager, paths := testManager(t, testPolicy{}, restarter, testVerifier{})
	writeTestFile(t, paths.modePath(), `{"deploymentMode":"1p"}`)
	if err := manager.Apply(context.Background(), ModeAnthropic, Gateway{}); err != nil {
		t.Fatal(err)
	}
	if restarter.calls != 0 {
		t.Fatalf("matching Anthropic mode restarted Desktop %d times", restarter.calls)
	}
	if _, err := os.Stat(paths.backupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching Anthropic mode created a backup: %v", err)
	}
}

func TestApplyStopsBeforeBackupWhenPolicyIsManaged(t *testing.T) {
	restarter := &testRestarter{}
	manager, paths := testManager(t, testPolicy{managed: true}, restarter, testVerifier{})
	if err := manager.Apply(context.Background(), ModeLocal, gatewayForTest()); err == nil || !strings.Contains(err.Error(), "managed inference policy") {
		t.Fatalf("Apply error=%v", err)
	}
	if restarter.calls != 0 {
		t.Fatalf("managed precheck restarted Desktop %d times", restarter.calls)
	}
	if _, err := os.Stat(paths.backupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed precheck created backup: %v", err)
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
