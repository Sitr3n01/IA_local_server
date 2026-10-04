package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sitr3n01/local-ai-provider/internal/claudedesktop"
	"github.com/Sitr3n01/local-ai-provider/internal/panel"
)

type fakeClaudePolicy struct{}

func (fakeClaudePolicy) ManagedInference(context.Context) (bool, error) { return false, nil }

type fakeClaudeProtector struct{}

func (fakeClaudeProtector) Protect(data []byte) ([]byte, error) {
	return append([]byte("protected:"), data...), nil
}
func (fakeClaudeProtector) Unprotect(data []byte) ([]byte, error) {
	return append([]byte(nil), data[len("protected:"):]...), nil
}

// recordingDesktop stands in for the Windows shell. Each Activate records the
// selector on disk, which is the deployment the new process would start in.
type recordingDesktop struct {
	selectorPath string
	running      map[string]bool
	launchedAs   []string
}

func (d *recordingDesktop) Show(_ context.Context, dataDir string) (bool, error) {
	return d.running[dataDir], nil
}

func (d *recordingDesktop) Activate(context.Context) error {
	var document struct {
		DeploymentMode string `json:"deploymentMode"`
	}
	data, err := os.ReadFile(d.selectorPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	d.launchedAs = append(d.launchedAs, document.DeploymentMode)
	return nil
}

func (d *recordingDesktop) WaitShown(_ context.Context, dataDir string) error {
	d.running[dataDir] = true
	return nil
}

type fakeClaudeDesktopService struct {
	localGateway   claudedesktop.Gateway
	localCalls     int
	anthropicCalls int
	err            error
}

func (f *fakeClaudeDesktopService) OpenAnthropic(context.Context) error {
	f.anthropicCalls++
	return f.err
}
func (f *fakeClaudeDesktopService) OpenLocal(_ context.Context, gateway claudedesktop.Gateway) error {
	f.localCalls++
	f.localGateway = gateway
	return f.err
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

func TestOpenClaudeLocalRunsCredentialProbeAndOpenAsOneFlow(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	var probed claudedesktop.Gateway
	controller.probeGateway = func(_ context.Context, gateway claudedesktop.Gateway) error {
		probed = gateway
		return nil
	}

	if err := controller.OpenClaudeLocal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.localCalls != 1 || service.anthropicCalls != 0 {
		t.Fatalf("local calls=%d anthropic calls=%d", service.localCalls, service.anthropicCalls)
	}
	if probed != service.localGateway || probed.BaseURL != controller.config.DataURL || probed.APIKey == "" {
		t.Fatalf("probe=%+v open=%+v", probed, service.localGateway)
	}
}

func TestOpenClaudeLocalWarmsTheProbeModelBeforeOpening(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	var order []string
	controller.warmClaude = func(context.Context) error {
		order = append(order, "warm")
		return nil
	}
	controller.probeGateway = func(context.Context, claudedesktop.Gateway) error {
		order = append(order, "probe")
		return nil
	}
	if err := controller.OpenClaudeLocal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "probe" || order[1] != "warm" || service.localCalls != 1 {
		t.Fatalf("order=%v local calls=%d, want probe then warm then open", order, service.localCalls)
	}
}

func TestOpenClaudeLocalDoesNotOpenWhenWarmingFails(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	controller.warmClaude = func(context.Context) error { return errors.New("insufficient physical memory") }
	err := controller.OpenClaudeLocal(context.Background())
	if err == nil || !strings.Contains(err.Error(), "insufficient physical memory") || service.localCalls != 0 {
		t.Fatalf("error=%v local calls=%d", err, service.localCalls)
	}
}

func TestOpenClaudeLocalStopsBeforeOpeningWhenProbeFails(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	controller.probeGateway = func(context.Context, claudedesktop.Gateway) error { return errors.New("gateway unavailable") }

	err := controller.OpenClaudeLocal(context.Background())
	if err == nil || service.localCalls != 0 {
		t.Fatalf("error=%v local calls=%d", err, service.localCalls)
	}
}

func TestLaunchClaudeDesktopOpensTheSignedInInstanceWithoutTheGateway(t *testing.T) {
	service := &fakeClaudeDesktopService{}
	controller := testClaudeController(service)
	controller.readCredential = func(string) (string, error) { t.Fatal("opening Claude read the gateway credential"); return "", nil }
	controller.probeGateway = func(context.Context, claudedesktop.Gateway) error {
		t.Fatal("opening Claude probed the gateway")
		return nil
	}
	if err := controller.LaunchClaudeDesktop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.anthropicCalls != 1 || service.localCalls != 0 {
		t.Fatalf("anthropic calls=%d local calls=%d", service.anthropicCalls, service.localCalls)
	}
}

func TestTrayClaudeEndToEndOpensLocalBesideTheSignedInInstance(t *testing.T) {
	root := t.TempDir()
	thirdParty := filepath.Join(root, "Local", "Claude-3p")
	firstParty := filepath.Join(root, "Roaming", "Claude", "claude_desktop_config.json")
	selectorPath := filepath.Join(thirdParty, "claude_desktop_config.json")
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
	if err := os.WriteFile(selectorPath, []byte(`{"deploymentMode":"1p"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The signed-in instance is already running, as it is while an operator
	// works in it; nothing here may launch it again or stop it.
	desktop := &recordingDesktop{selectorPath: selectorPath, running: map[string]bool{filepath.Dir(firstParty): true}}
	manager, err := claudedesktop.NewManager(paths, fakeClaudePolicy{}, desktop, fakeClaudeProtector{})
	if err != nil {
		t.Fatal(err)
	}
	controller := testClaudeController(manager)

	if err := controller.OpenClaudeLocal(context.Background()); err != nil {
		t.Fatalf("open Claude Local: %v", err)
	}
	if err := controller.LaunchClaudeDesktop(context.Background()); err != nil {
		t.Fatalf("show Claude: %v", err)
	}
	if err := controller.OpenClaudeLocal(context.Background()); err != nil {
		t.Fatalf("show Claude Local again: %v", err)
	}

	if len(desktop.launchedAs) != 1 || desktop.launchedAs[0] != "3p" {
		t.Fatalf("launches=%v, want exactly one, of the local deployment", desktop.launchedAs)
	}
	firstPartyAfter, err := os.ReadFile(firstParty)
	if err != nil || string(firstPartyAfter) != string(firstPartyBefore) {
		t.Fatalf("first-party state changed: %q err=%v", firstPartyAfter, err)
	}
	selectorAfter, err := os.ReadFile(selectorPath)
	if err != nil || !strings.Contains(string(selectorAfter), `"deploymentMode": "1p"`) {
		t.Fatalf("selector at rest=%s err=%v", selectorAfter, err)
	}
	profileAfter, err := os.ReadFile(filepath.Join(thirdParty, "configLibrary", "077f3f5a-e971-4d10-8e11-3641069cf0e1.json"))
	if err != nil || !strings.Contains(string(profileAfter), `"inferenceGatewayBaseUrl": "http://127.0.0.1:18090"`) {
		t.Fatalf("local profile was not written: %s err=%v", profileAfter, err)
	}
}

func TestHeadlessInvocationIncludesClaudeCommandsButNotNormalTrayConfig(t *testing.T) {
	for _, args := range [][]string{
		{"-config", `C:\IA\local-ai-v2\config\panel.canary.json`, "-claude-local"},
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
