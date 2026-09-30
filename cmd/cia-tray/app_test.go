package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sitr3n/local-ai-provider/internal/claudedesktop"
	"github.com/sitr3n/local-ai-provider/internal/panel"
	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

type fakeClaudePolicy struct{}

func (fakeClaudePolicy) ManagedInference(context.Context) (bool, error) { return false, nil }

type fakeClaudeRestarter struct{ calls int }

func (f *fakeClaudeRestarter) Restart(context.Context, claudedesktop.Identity) error {
	f.calls++
	return nil
}

type recordingClaudeVerifier struct{ modes []claudedesktop.Mode }

func (v *recordingClaudeVerifier) Verify(_ context.Context, mode claudedesktop.Mode, _ claudedesktop.Gateway) error {
	v.modes = append(v.modes, mode)
	return nil
}

type fakeClaudeProtector struct{}

func (fakeClaudeProtector) Protect(data []byte) ([]byte, error) {
	return append([]byte("protected:"), data...), nil
}
func (fakeClaudeProtector) Unprotect(data []byte) ([]byte, error) {
	return append([]byte(nil), data[len("protected:"):]...), nil
}

type managerClaudeDesktopService struct {
	manager     *claudedesktop.Manager
	launchCalls int
}

func (s *managerClaudeDesktopService) CurrentMode() (claudedesktop.Mode, error) {
	return s.manager.CurrentMode()
}
func (s *managerClaudeDesktopService) Apply(ctx context.Context, mode claudedesktop.Mode, gateway claudedesktop.Gateway) error {
	return s.manager.Apply(ctx, mode, gateway)
}
func (s *managerClaudeDesktopService) Launch(context.Context) error {
	s.launchCalls++
	return nil
}

type fakeClaudeDesktopService struct {
	mode         claudedesktop.Mode
	applyMode    claudedesktop.Mode
	applyGateway claudedesktop.Gateway
	applyCalls   int
	launchCalls  int
	applyErr     error
	launchErr    error
}

func (f *fakeClaudeDesktopService) CurrentMode() (claudedesktop.Mode, error) { return f.mode, nil }
func (f *fakeClaudeDesktopService) Apply(_ context.Context, mode claudedesktop.Mode, gateway claudedesktop.Gateway) error {
	f.applyCalls++
	f.applyMode = mode
	f.applyGateway = gateway
	return f.applyErr
}
func (f *fakeClaudeDesktopService) Launch(context.Context) error {
	f.launchCalls++
	return f.launchErr
}

func testClaudeController(service claudeDesktopService) *appController {
	return &appController{
		config: panel.Config{DataURL: "http://127.0.0.1:18090"},
		claude: service,
		readCredential: func(kind string) (string, error) {
			if kind != "claude-gateway" {
				return "", errors.New("unexpected credential kind")
			}
			return "claude-gateway-test-token-000000000000", nil
		},
		probeGateway: func(context.Context, claudedesktop.Gateway) error { return nil },
	}
}

func TestSetClaudeLocalRunsCredentialProbeAndApplyAsOneFlow(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	var probed claudedesktop.Gateway
	controller.probeGateway = func(_ context.Context, gateway claudedesktop.Gateway) error {
		probed = gateway
		return nil
	}

	if err := controller.SetClaudeMode(context.Background(), trayui.ClaudeModeLocal); err != nil {
		t.Fatal(err)
	}
	if service.applyCalls != 1 || service.applyMode != claudedesktop.ModeLocal {
		t.Fatalf("apply calls=%d mode=%q", service.applyCalls, service.applyMode)
	}
	if probed != service.applyGateway || probed.BaseURL != controller.config.DataURL || probed.APIKey == "" {
		t.Fatalf("probe=%+v apply=%+v", probed, service.applyGateway)
	}
}

func TestSetClaudeLocalStopsBeforeMutationWhenProbeFails(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	controller.probeGateway = func(context.Context, claudedesktop.Gateway) error { return errors.New("gateway unavailable") }

	err := controller.SetClaudeMode(context.Background(), trayui.ClaudeModeLocal)
	if err == nil || service.applyCalls != 0 {
		t.Fatalf("error=%v apply calls=%d", err, service.applyCalls)
	}
}

func TestSetClaudeAnthropicDoesNotReadOrProbeGateway(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	controller.readCredential = func(string) (string, error) { t.Fatal("Anthropic mode read gateway credential"); return "", nil }
	controller.probeGateway = func(context.Context, claudedesktop.Gateway) error {
		t.Fatal("Anthropic mode probed gateway")
		return nil
	}

	if err := controller.SetClaudeMode(context.Background(), trayui.ClaudeModeAnthropic); err != nil {
		t.Fatal(err)
	}
	if service.applyCalls != 1 || service.applyMode != claudedesktop.ModeAnthropic || service.applyGateway != (claudedesktop.Gateway{}) {
		t.Fatalf("unexpected apply: calls=%d mode=%q gateway=%+v", service.applyCalls, service.applyMode, service.applyGateway)
	}
}

func TestLaunchClaudeDesktopUsesSameDiscoveredService(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	if err := controller.LaunchClaudeDesktop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.launchCalls != 1 {
		t.Fatalf("launch calls=%d, want 1", service.launchCalls)
	}
}

func TestTrayClaudeEndToEndLocalOpenAnthropicPreservesFirstPartyState(t *testing.T) {
	root := t.TempDir()
	thirdParty := filepath.Join(root, "Local", "Claude-3p")
	firstParty := filepath.Join(root, "Roaming", "Claude", "claude_desktop_config.json")
	paths := claudedesktop.Paths{
		ThirdPartyStateDir: thirdParty,
		FirstPartyConfig:   firstParty,
		BackupDir:          filepath.Join(root, "cia-state"),
	}
	if err := os.MkdirAll(filepath.Dir(firstParty), 0o700); err != nil {
		t.Fatal(err)
	}
	firstPartyBefore := []byte(`{"mcpServers":{"keep":{"command":"keep.exe"}},"account":"untouched"}`)
	if err := os.WriteFile(firstParty, firstPartyBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(thirdParty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(thirdParty, "claude_desktop_config.json"), []byte(`{"deploymentMode":"1p"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	restarter := &fakeClaudeRestarter{}
	verifier := &recordingClaudeVerifier{}
	manager, err := claudedesktop.NewManager(paths, claudedesktop.Identity{
		PackageFamilyName: "Claude_test",
		AppUserModelID:    "Claude_test!Claude",
		Version:           "1.37937.1.0",
		InstallLocation:   filepath.Join(root, "WindowsApps", "Claude_test"),
	}, fakeClaudePolicy{}, restarter, verifier, fakeClaudeProtector{})
	if err != nil {
		t.Fatal(err)
	}
	service := &managerClaudeDesktopService{manager: manager}
	controller := testClaudeController(service)

	if err := controller.SetClaudeMode(context.Background(), trayui.ClaudeModeLocal); err != nil {
		t.Fatalf("1P -> Local: %v", err)
	}
	if err := controller.LaunchClaudeDesktop(context.Background()); err != nil {
		t.Fatalf("open Local: %v", err)
	}
	if err := controller.SetClaudeMode(context.Background(), trayui.ClaudeModeAnthropic); err != nil {
		t.Fatalf("Local -> 1P: %v", err)
	}

	firstPartyAfter, err := os.ReadFile(firstParty)
	if err != nil || string(firstPartyAfter) != string(firstPartyBefore) {
		t.Fatalf("first-party state changed: %q err=%v", firstPartyAfter, err)
	}
	modeAfter, err := os.ReadFile(filepath.Join(thirdParty, "claude_desktop_config.json"))
	if err != nil || !strings.Contains(string(modeAfter), `"deploymentMode": "1p"`) {
		t.Fatalf("final mode=%s err=%v", modeAfter, err)
	}
	profileAfter, err := os.ReadFile(filepath.Join(thirdParty, "configLibrary", "077f3f5a-e971-4d10-8e11-3641069cf0e1.json"))
	if err != nil || !strings.Contains(string(profileAfter), `"inferenceGatewayBaseUrl": "http://127.0.0.1:18090"`) {
		t.Fatalf("local profile was not retained for the next toggle: %s err=%v", profileAfter, err)
	}
	if restarter.calls != 2 || service.launchCalls != 1 {
		t.Fatalf("restarts=%d launches=%d, want 2 and 1", restarter.calls, service.launchCalls)
	}
	if len(verifier.modes) != 2 || verifier.modes[0] != claudedesktop.ModeLocal || verifier.modes[1] != claudedesktop.ModeAnthropic {
		t.Fatalf("verified modes=%v", verifier.modes)
	}
}

func TestHeadlessInvocationIncludesClaudeCommandsButNotNormalTrayConfig(t *testing.T) {
	for _, args := range [][]string{
		{"-config", `C:\IA\local-ai-v2\config\panel.canary.json`, "-claude-mode", "local", "-apply"},
		{"-claude-open"},
		{"--diagnose"},
	} {
		if !headlessInvocation(args) {
			t.Errorf("headlessInvocation(%v)=false", args)
		}
	}
	if headlessInvocation([]string{"-config", `C:\IA\local-ai-v2\config\panel.canary.json`}) {
		t.Fatal("normal tray startup was classified as headless")
	}
}
