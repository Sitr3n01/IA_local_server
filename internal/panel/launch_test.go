package panel

import (
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeEnvironmentUsesAllowlistAndRemovesSecrets(t *testing.T) {
	input := []string{
		"Path=C:\\Windows",
		"PATH=C:\\Safe",
		"USERPROFILE=C:\\Users\\test",
		"CIA_INFERENCE_TOKEN=secret",
		"OPENAI_API_KEY=cloud-secret",
		"OPENCODE_CONFIG=redirect.json",
		"HIP_VISIBLE_DEVICES=1",
		"HTTP_PROXY=http://proxy.invalid",
		"UNRELATED=value",
	}
	want := []string{"PATH=C:\\Safe", "USERPROFILE=C:\\Users\\test"}
	if got := SanitizeEnvironment(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}

func TestLauncherBuildsShellFreeCommandSpec(t *testing.T) {
	cfg := testConfig(t)
	catalog := testCatalog(t)
	launcher, err := NewLauncher(cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	expectedPowerShell, err := powerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []Client{ClientCodex, ClientOpenCode} {
		t.Run(string(client), func(t *testing.T) {
			spec, err := launcher.Spec(client, "local-coding")
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := 8
			if client == ClientCodex {
				wantArgs = 9
			}
			if spec.Path != expectedPowerShell || len(spec.Args) != wantArgs {
				t.Fatalf("unexpected command spec: %+v", spec)
			}
			if spec.Args[len(spec.Args)-2] != "-Model" || spec.Args[len(spec.Args)-1] != "local-coding" {
				t.Fatalf("model was not passed as a separate argument: %v", spec.Args)
			}
			for _, entry := range spec.Env {
				upper := strings.ToUpper(entry)
				if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "API_KEY=") || strings.HasPrefix(upper, "HIP_") {
					t.Fatalf("secret or GPU override leaked into command: %q", entry)
				}
			}
		})
	}
}

func TestLauncherRejectsUnknownAndUnavailableModels(t *testing.T) {
	cfg := testConfig(t)
	catalog := testCatalog(t)
	launcher, err := NewLauncher(cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Spec("browser", "local-coding"); err == nil {
		t.Fatal("unknown client was accepted")
	}
	if _, err := launcher.Spec(ClientCodex, "missing"); err == nil {
		t.Fatal("unknown model was accepted")
	}
	if _, err := launcher.Spec(ClientOpenCode, "local-fast"); err == nil {
		t.Fatal("unavailable model was accepted")
	}

	chatPath := writeTestFile(t, "models.yaml", testManifest("chat-only",
		testModel("chat-only", "enabled", "[\"canary\"]", false, true, true, false),
	))
	chatCatalog, err := LoadCatalog(chatPath, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	chatLauncher, err := NewLauncher(cfg, chatCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chatLauncher.Spec(ClientCodex, "chat-only"); err != nil {
		t.Fatalf("available chat-only model should be selectable in Codex: %v", err)
	}
	if _, err := chatLauncher.Spec(ClientOpenCode, "chat-only"); err != nil {
		t.Fatalf("available chat-only model should be selectable in OpenCode: %v", err)
	}
}
