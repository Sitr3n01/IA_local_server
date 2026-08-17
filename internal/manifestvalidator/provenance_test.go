package manifestvalidator

import (
	"encoding/json"
	"testing"
)

// forkRuntime is the shape a source-built buun-llama-cpp runtime has to declare.
// It is written out in full here rather than read from config/models.yaml,
// because the repository manifest deliberately carries no fork runtime until a
// real artifact has been built and hashed on the target machine.
func forkRuntime() map[string]any {
	return map[string]any{
		"id":            "amd-rocm-qwen38-buun",
		"state":         "candidate",
		"engine":        "llama.cpp",
		"variant":       "fork",
		"version_label": "buun-llama-cpp 799e3995 (agentic canary)",
		"build_commit":  "799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee",
		"provenance": map[string]any{
			"source_repository": "https://github.com/spiritbuun/buun-llama-cpp",
			"source_revision":   "799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee",
			"checkpoint_fix": map[string]any{
				"reference":          "https://github.com/ggml-org/llama.cpp/pull/22384",
				"evidence":           "semantic-equivalent",
				"verified_utc":       "2026-08-17T00:00:00Z",
				"gate_report_sha256": "11" + zeros(62),
			},
			"build": map[string]any{
				"backend":       "ROCm",
				"gpu_targets":   []any{"gfx1201"},
				"configuration": "Release",
				"targets":       []any{"llama-server"},
			},
		},
		"artifact": map[string]any{
			"path":   `C:\IA\local-llama\amd\buun_llama_cpp_799e3995_rocm_gfx1201\llama-server.exe`,
			"bytes":  10305536,
			"sha256": "22" + zeros(62),
		},
		"device": map[string]any{
			"backend":  "ROCm",
			"selector": "ROCm0",
			"gpu":      "AMD Radeon RX 9070 XT (gfx1201)",
			"vram_mib": 16304,
		},
		"environment": map[string]any{"ROCBLAS_USE_HIPBLASLT": "0"},
	}
}

func zeros(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}

func TestSchemaAcceptsForkRuntime(t *testing.T) {
	schema, manifest := repositoryFiles(t)
	candidate := withRuntime(t, manifest, forkRuntime())
	if err := Validate(schema, candidate); err != nil {
		t.Fatalf("a complete fork runtime was rejected: %v", err)
	}
}

// Each case below is a way the fork runtime can lose the identity the pin
// exists to guarantee. All of them must fail closed at schema validation, which
// is the only check that runs before anything is generated or built.
func TestSchemaRejectsIncompleteForkProvenance(t *testing.T) {
	schema, manifest := repositoryFiles(t)

	tests := map[string]func(map[string]any){
		"provenance absent when the variant is fork": func(runtime map[string]any) {
			delete(runtime, "provenance")
		},
		"variant absent while provenance is declared": func(runtime map[string]any) {
			delete(runtime, "variant")
		},
		"unknown runtime variant": func(runtime map[string]any) {
			runtime["variant"] = "vendor"
		},
		"revision pinned to a branch": func(runtime map[string]any) {
			provenance(runtime)["source_revision"] = "master"
		},
		"revision pinned to HEAD": func(runtime map[string]any) {
			provenance(runtime)["source_revision"] = "HEAD"
		},
		"revision abbreviated": func(runtime map[string]any) {
			provenance(runtime)["source_revision"] = "799e3995"
		},
		"revision not lowercase hex": func(runtime map[string]any) {
			provenance(runtime)["source_revision"] = "799E3995CD4F19AA9F6A3FA9FB5B4674422BF0EE"
		},
		"source repository outside github": func(runtime map[string]any) {
			provenance(runtime)["source_repository"] = "https://example.invalid/spiritbuun/buun-llama-cpp"
		},
		"checkpoint evidence absent": func(runtime map[string]any) {
			delete(provenance(runtime), "checkpoint_fix")
		},
		"checkpoint evidence unclassified": func(runtime map[string]any) {
			checkpointFix(runtime)["evidence"] = "looks-right"
		},
		"checkpoint evidence without a gate report": func(runtime map[string]any) {
			delete(checkpointFix(runtime), "gate_report_sha256")
		},
		"build configuration absent": func(runtime map[string]any) {
			delete(provenance(runtime), "build")
		},
		"build target beyond llama-server": func(runtime map[string]any) {
			build(runtime)["targets"] = []any{"llama-server", "llama-cli"}
		},
		"build gpu target malformed": func(runtime map[string]any) {
			build(runtime)["gpu_targets"] = []any{"RX 9070 XT"}
		},
		"debug build configuration": func(runtime map[string]any) {
			build(runtime)["configuration"] = "Debug"
		},
		"arbitrary provenance field": func(runtime map[string]any) {
			provenance(runtime)["extra_args"] = []any{"--turbo"}
		},
		"arbitrary runtime field": func(runtime map[string]any) {
			runtime["metadata"] = map[string]any{"anything": "goes"}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			runtime := forkRuntime()
			mutate(runtime)
			if err := Validate(schema, withRuntime(t, manifest, runtime)); err == nil {
				t.Fatal("an unqualifiable fork runtime passed schema validation")
			}
		})
	}
}

// An upstream runtime must stay expressible without provenance, otherwise
// adopting the fork would force an edit to the pinned baseline entries.
func TestSchemaKeepsUpstreamRuntimesFreeOfProvenance(t *testing.T) {
	schema, manifest := repositoryFiles(t)
	var decoded map[string]any
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, entry := range decoded["runtimes"].([]any) {
		runtime := entry.(map[string]any)
		if _, declared := runtime["provenance"]; declared {
			t.Fatalf("runtime %v unexpectedly declares provenance", runtime["id"])
		}
		runtime["variant"] = "upstream"
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(schema, encoded); err != nil {
		t.Fatalf("an upstream runtime without provenance was rejected: %v", err)
	}
}

func withRuntime(t *testing.T, manifest []byte, runtime map[string]any) []byte {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["runtimes"] = append(decoded["runtimes"].([]any), runtime)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func provenance(runtime map[string]any) map[string]any {
	return runtime["provenance"].(map[string]any)
}

func checkpointFix(runtime map[string]any) map[string]any {
	return provenance(runtime)["checkpoint_fix"].(map[string]any)
}

func build(runtime map[string]any) map[string]any {
	return provenance(runtime)["build"].(map[string]any)
}
