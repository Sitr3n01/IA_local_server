package edge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModelsPublishesEveryModelInEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.yaml")
	data := []byte(`provider:
  public_model: local-coding
models:
  - id: local-coding
    state: candidate
    deployments: [canary]
  - id: local-fast
    state: candidate
    deployments: [canary]
  - id: retired-model
    state: retired
    deployments: []
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "local-coding" || models[1].ID != "local-fast" {
		t.Fatalf("published models = %+v", models)
	}
}

func TestRepositoryManifestExposesCanaryModels(t *testing.T) {
	path := filepath.Join("..", "..", "config", "models.yaml")
	models, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatalf("LoadModels(%s): %v", path, err)
	}
	want := []string{
		"local-coding",
		"local-fast",
		"qwen35-9b-q4km",
		"qwen35-9b-ud-q4xl",
		"gemma4-12b-qat-q4_0",
		"gemma4-12b-qat-ud-q4xl",
	}
	if len(models) != len(want) {
		t.Fatalf("repository allowlist = %+v, want %d canary models", models, len(want))
	}
	for i, id := range want {
		if models[i].ID != id {
			t.Fatalf("repository allowlist[%d] = %q, want %q; full allowlist = %+v", i, models[i].ID, id, models)
		}
	}
}
