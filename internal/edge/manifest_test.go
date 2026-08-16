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

func TestLoadModelsJoinsRuntimeVRAMBudgetAndHostMemoryFields(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.yaml")
	data := []byte(`provider:
  public_model: local-coding
runtimes:
  - id: amd-rocm-baseline
    device:
      vram_mib: 16304
  - id: runtime-without-budget
    device: {}
models:
  - id: local-coding
    state: candidate
    deployments: [canary]
    runtime: amd-rocm-baseline
    cache_ram_mib: 6144
    tensor_overrides:
      - pattern: "blk\\.(4[4-9]|5[0-9]|6[0-3])\\.ffn_.*"
        buffer: CPU
    resources:
      peak_commit_gib: 22.5
      peak_vram_gib: 14.6
  - id: local-fast
    state: candidate
    deployments: [canary]
    runtime: runtime-without-budget
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}

	coding := models[0]
	if coding.DeviceVRAMGiB == nil || *coding.DeviceVRAMGiB != 16304.0/1024 {
		t.Errorf("device VRAM budget = %v, want %v", coding.DeviceVRAMGiB, 16304.0/1024)
	}
	if coding.PeakVRAMGiB == nil || *coding.PeakVRAMGiB != 14.6 {
		t.Errorf("peak VRAM = %v", coding.PeakVRAMGiB)
	}
	if coding.CacheRAMMiB == nil || *coding.CacheRAMMiB != 6144 {
		t.Errorf("cache RAM = %v", coding.CacheRAMMiB)
	}
	if !coding.OffloadsTensors {
		t.Error("tensor_overrides did not mark the model as offloading")
	}

	// A runtime that declares no budget must leave admission unconstrained
	// rather than defaulting to some assumed device size.
	fast := models[1]
	if fast.DeviceVRAMGiB != nil {
		t.Errorf("absent vram_mib produced a budget: %v", *fast.DeviceVRAMGiB)
	}
	if fast.OffloadsTensors || fast.CacheRAMMiB != nil {
		t.Errorf("unexpected host-memory flags on %+v", fast)
	}
}
