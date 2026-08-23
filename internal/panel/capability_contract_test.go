package panel

import "testing"

// capabilities.function_calling is a deployment guarantee for one artifact, not
// a description of what a chat template can do. docs/MODEL_PROMOTION.md defines
// it as "false until the stress evaluation demonstrates a valid forced tool call
// through internal/edge/namespace.go", and adds that a model family's tool-call
// serialization is not evidence for a specific quantization of it.
//
// These tests exist because the 2026-08-23 campaign measured
// gemma4-12b-qat-ud-q4xl at 6/7 on the tool suite while its manifest declared
// function_calling: false, and the campaign report read that as the flag being
// over-conservative. The benchmark talks straight to llama-server's
// /v1/chat/completions; it never crosses internal/edge/namespace.go, so it did
// not produce the evidence the flag is about. Flipping the boolean to match a
// benchmark score would change what four client catalogs advertise on the
// strength of a measurement of something else.
//
// What is pinned here is the flag's contract, so the next person who reads a
// tool score does not have to re-derive it from four call sites:
//
//   - it is required, and a manifest that omits it is rejected rather than
//     defaulted, because an absent guarantee is not a false one;
//   - it is carried verbatim into the panel projection, so every consumer sees
//     the manifest's claim and not a re-interpretation of it;
//   - it does NOT gate launching. An operator may deliberately open a client
//     with a model whose tool use is weaker than a harness would like, and
//     hiding the launcher would take that decision away from them.
//
// The consumer that gives the flag teeth is New-V2ClientCatalogs.ps1, which
// maps it to the Codex catalog's supports_parallel_tool_calls;
// Test-V2HarnessConfig.ps1 asserts that mapping stays exact.

func TestFunctionCallingIsCarriedVerbatimIntoTheProjection(t *testing.T) {
	path := writeTestFile(t, "models.yaml", testManifest("declares-tools",
		testModel("declares-tools", "candidate", "[\"canary\"]", false, true, true, true),
		testModel("withholds-tools", "candidate", "[\"canary\"]", false, true, true, false),
	))
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}

	declares, ok := catalog.Model("declares-tools")
	if !ok {
		t.Fatal("declares-tools missing from the catalog")
	}
	if !declares.Capabilities.FunctionCalling {
		t.Error("a manifest claim of function_calling: true was not carried into the projection")
	}

	withholds, ok := catalog.Model("withholds-tools")
	if !ok {
		t.Fatal("withholds-tools missing from the catalog")
	}
	if withholds.Capabilities.FunctionCalling {
		t.Error("a manifest claim of function_calling: false was upgraded somewhere in the projection")
	}
}

func TestFunctionCallingDoesNotGateLaunching(t *testing.T) {
	// gemma4-12b-qat-ud-q4xl's exact shape: the public canary default, serving
	// chat and streaming, declaring no function-calling guarantee. It has to
	// stay launchable -- it is the always-on model.
	path := writeTestFile(t, "models.yaml", testManifest("withholds-tools",
		testModel("withholds-tools", "candidate", "[\"canary\"]", false, true, true, false),
	))
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := catalog.Model("withholds-tools")
	if !ok {
		t.Fatal("withholds-tools missing from the catalog")
	}
	if !model.Available {
		t.Fatalf("a deployed candidate was made unavailable by its capability flags: %+v", model)
	}
	if !model.CanLaunchCodex() || !model.CanLaunchOpenCode() {
		t.Error("function_calling: false hid a launcher; capability flags describe, they do not gate")
	}
}

func TestFunctionCallingMustBeStatedRatherThanDefaulted(t *testing.T) {
	// An absent guarantee is not a false one. A manifest that forgets the field
	// has to fail loudly, or a model ships advertising whatever the zero value
	// happens to be.
	const noFunctionCalling = `{
  "id": "no-capability-block",
  "display_name": "No capability block",
  "state": "candidate",
  "runtime": "runtime",
  "artifact": {"path": "C:\\models\\model.gguf", "bytes": 1024, "sha256": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
  "deployments": ["canary"],
  "context_tokens": 65536,
  "max_output_tokens": 8192,
  "cache_type_k": "q4_0",
  "cache_type_v": "q4_0",
  "gpu_layers": 99,
  "capabilities": {
    "responses": false,
    "chat_completions": true,
    "streaming": true,
    "structured_output": false
  }
}`
	path := writeTestFile(t, "models.yaml",
		testManifest("no-capability-block", noFunctionCalling))
	if _, err := LoadCatalog(path, EnvironmentCanary); err == nil {
		t.Fatal("a manifest omitting function_calling was accepted and silently defaulted")
	}
}
