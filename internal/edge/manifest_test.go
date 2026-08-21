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
	models, _, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "local-coding" || models[1].ID != "local-fast" {
		t.Fatalf("published models = %+v", models)
	}
}

func TestRepositoryManifestExposesCanaryModels(t *testing.T) {
	path := filepath.Join("..", "..", "config", "models.yaml")
	models, _, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatalf("LoadModels(%s): %v", path, err)
	}
	// Ordered and exact on purpose: this is the deployment's model list, and a
	// model appearing in it by accident is exactly what the assertion catches.
	// Extending it is a deliberate act, which is why adding the workstation
	// profiles required editing this line.
	//
	// The five qwen38-27b-ws-* profiles were retired in favour of three named
	// classes - Deep, Agent, Huge - and retired models carry no deployments, so
	// they drop out of this list while remaining in the manifest as evidence.
	want := []string{
		"local-coding",
		"local-fast",
		"qwen35-9b-q4km",
		"qwen35-9b-ud-q4xl",
		"gemma4-12b-qat-q4_0",
		"gemma4-12b-qat-ud-q4xl",
		"qwen38-27b-deep-32k",
		"qwen38-27b-agent-128k",
		"qwen38-27b-huge-256k",
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
      peak_ram_gib: 12.4
  - id: local-fast
    state: candidate
    deployments: [canary]
    runtime: runtime-without-budget
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, _, err := LoadModels(path, "canary")
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
	if coding.PeakRAMGiB == nil || *coding.PeakRAMGiB != 12.4 {
		t.Errorf("peak RAM = %v", coding.PeakRAMGiB)
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

func TestLoadModelsReturnsPublicModelRegardlessOfOrder(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.yaml")
	// public_model is deliberately the *second* entry.
	data := []byte(`provider:
  public_model: local-coding
models:
  - id: local-fast
    state: candidate
    deployments: [canary]
  - id: local-coding
    state: candidate
    deployments: [canary]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, public, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if public != "local-coding" {
		t.Fatalf("public model = %q, want local-coding", public)
	}
	if models[0].ID != "local-fast" {
		t.Fatalf("array order changed: %+v", models)
	}
}

// TestLoadModelsReportsProfileSummary covers the fields an operator needs to
// tell the three Qwen3.8 classes apart on /api/v1/status. Context alone does
// not: Deep and Agent differ by weights and cache precision, Agent and Huge by
// output ceiling and reasoning budget. A profile that reports only its context
// window is indistinguishable from the one next to it.
func TestLoadModelsReportsProfileSummary(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.yaml")
	data := []byte(`provider:
  public_model: huge
runtimes:
  - id: rt
    device:
      vram_mib: 16304
models:
  - id: huge
    state: candidate
    deployments: [canary]
    runtime: rt
    context_tokens: 262144
    max_output_tokens: 32768
    n_predict: 32768
    reasoning_budget: 24576
    compact_threshold_tokens: 221184
    cache_type_k: q4_0
    cache_type_v: q4_0
    artifact:
      path: C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf
  - id: plain
    state: candidate
    deployments: [canary]
    runtime: rt
    context_tokens: 32768
    max_output_tokens: 8192
    cache_type_k: q8_0
    cache_type_v: q8_0
    artifact:
      path: /models/other.gguf
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	models, _, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}

	huge := models[0].Profile
	if huge.Weights != "Qwen3.8-27B-UD-Q2_K_XL" {
		t.Errorf("weights = %q, want the bare GGUF name without path or extension", huge.Weights)
	}
	if huge.CacheTypeK != "q4_0" || huge.CacheTypeV != "q4_0" {
		t.Errorf("cache types = %q/%q, want q4_0/q4_0", huge.CacheTypeK, huge.CacheTypeV)
	}
	for name, got := range map[string]*int{
		"max_output_tokens":        huge.MaxOutputTokens,
		"n_predict":                huge.NPredict,
		"reasoning_budget":         huge.ReasoningBudget,
		"compact_threshold_tokens": huge.CompactThreshold,
	} {
		if got == nil {
			t.Errorf("%s is nil, want it reported", name)
		}
	}
	if huge.ReasoningBudget != nil && *huge.ReasoningBudget != 24576 {
		t.Errorf("reasoning_budget = %d, want 24576", *huge.ReasoningBudget)
	}

	// A model that declares none of the optional fields must report them as
	// absent rather than as zero: unset and "zero tokens of thinking" are
	// different configurations and omitempty has to be able to tell them apart.
	plain := models[1].Profile
	if plain.NPredict != nil || plain.ReasoningBudget != nil || plain.CompactThreshold != nil {
		t.Errorf("undeclared optional fields surfaced as set: %+v", plain)
	}
	if plain.Weights != "other" {
		t.Errorf("weights = %q, want %q", plain.Weights, "other")
	}
}
