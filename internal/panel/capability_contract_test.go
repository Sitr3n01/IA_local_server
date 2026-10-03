package panel

import "testing"

// capabilities.function_calling is a deployment guarantee for one artifact, not
// a description of what a chat template can do. docs/MODEL_PROMOTION.md keeps
// it false until forced calls with exact arguments and namespace round-trips
// through internal/edge/namespace.go have passed for that artifact, and adds
// that a model family's tool-call serialization is not evidence for a specific
// quantization of it.
//
// These tests exist because the 2026-08-23 campaign measured
// gemma4-12b-qat-ud-q4xl at 6/7 on the tool suite while its manifest declared
// function_calling: false, and the campaign report read that as the flag being
// over-conservative. The benchmark talks straight to llama-server's
// /v1/chat/completions; it never crosses internal/edge/namespace.go, so it did
// not produce the evidence the flag is about. Flipping the boolean to match a
// benchmark score would change what the edge admits and what four client
// catalogs advertise, on the strength of a measurement of something else.
//
// What is pinned here is the flag's contract, so the next person who reads a
// tool score does not have to re-derive it from four call sites:
//
//   - it is required, and a manifest that omits it is rejected rather than
//     defaulted, because an absent guarantee is not a false one;
//   - it is carried verbatim into the panel projection, so every consumer sees
//     the manifest's claim and not a re-interpretation of it;
//   - it does NOT gate availability. A deployed model whose tool use is
//     weaker than a harness would like stays selectable; deciding which model
//     a harness may use is the client catalogs' job, not the panel's.
//
// The edge is what gives the flag teeth. On the OpenAI routes
// internal/edge/capabilities.go refuses tools, required tool choices and tool
// history with 400 unsupported_feature; on /v1/messages internal/edge/anthropic.go
// refuses a required tool choice or tool history and omits optional tools.
// New-V2ClientCatalogs.ps1 also maps the flag to the Codex catalog's
// supports_parallel_tool_calls, and Test-V2HarnessConfig.ps1 asserts that
// mapping stays exact.

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

func TestFunctionCallingDoesNotGateAvailability(t *testing.T) {
	// gemma4-12b-qat-ud-q4xl's exact shape: the public canary default, serving
	// chat and streaming, declaring no function-calling guarantee. It has to
	// stay available -- it is the always-on model.
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
