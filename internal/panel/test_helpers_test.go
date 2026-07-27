package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		SchemaVersion:  2,
		Environment:    EnvironmentCanary,
		DataURL:        "http://127.0.0.1:18090",
		ControlURL:     "http://127.0.0.1:18091",
		ManifestPath:   filepath.Join(root, "models.yaml"),
		SelectionPath:  filepath.Join(root, "selection.json"),
		ModelRootsPath: filepath.Join(root, "model-roots.json"),
		ValidationPath: filepath.Join(root, "validation.json"),
		LogsPath:       filepath.Join(root, "logs"),
		Launchers: LauncherPaths{
			Codex:    filepath.Join(root, "Start-Codex.ps1"),
			OpenCode: filepath.Join(root, "Start-OpenCode.ps1"),
		},
		RefreshSeconds:          5,
		OperationTimeoutSeconds: 30,
	}
}

func testManifest(publicModel string, models ...string) string {
	return fmt.Sprintf("{\n"+
		"  \"schema_version\": 1,\n"+
		"  \"provider\": {\"public_model\": %q},\n"+
		"  \"models\": [%s]\n"+
		"}", publicModel, strings.Join(models, ","))
}

func testModel(id, state, deployments string, responses, chat, streaming, tools bool) string {
	return fmt.Sprintf("{\n"+
		"  \"id\": %q,\n"+
		"  \"display_name\": %q,\n"+
		"  \"state\": %q,\n"+
		"  \"runtime\": \"runtime\",\n"+
		"  \"artifact\": {\"path\": \"C:\\\\models\\\\model.gguf\", \"bytes\": 1024, \"sha256\": \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"},\n"+
		"  \"deployments\": %s,\n"+
		"  \"context_tokens\": 65536,\n"+
		"  \"max_output_tokens\": 8192,\n"+
		"  \"cache_type_k\": \"q4_0\",\n"+
		"  \"cache_type_v\": \"q4_0\",\n"+
		"  \"gpu_layers\": 99,\n"+
		"  \"capabilities\": {\n"+
		"    \"responses\": %t,\n"+
		"    \"chat_completions\": %t,\n"+
		"    \"streaming\": %t,\n"+
		"    \"function_calling\": %t,\n"+
		"    \"structured_output\": false\n"+
		"  }\n"+
		"}", id, "Display "+id, state, deployments, responses, chat, streaming, tools)
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := writeTestFile(t, "models.yaml", testManifest("local-coding",
		testModel("local-coding", "candidate", "[\"canary\"]", true, true, true, true),
		testModel("local-fast", "candidate", "[]", false, true, true, false),
	))
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
