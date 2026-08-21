package edge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forkManifest is a two-runtime deployment: the pinned upstream baseline serving
// the public model, and the experimental fork serving one canary entry. It is
// the shape the fork is adopted in, and the shape every test below reasons about.
const forkManifest = `provider:
  public_model: local-coding
runtimes:
  - id: amd-rocm-baseline
    state: candidate
    engine: llama.cpp
    device:
      backend: ROCm
      vram_mib: 16304
    artifact:
      sha256: D427939A79AABAAA26B98361CBF3BB3DDE658ACBB9ACD1F59C5A95C60567B085
  - id: amd-rocm-qwen38-buun
    state: candidate
    engine: llama.cpp
    variant: fork
    device:
      backend: ROCm
      vram_mib: 16304
    artifact:
      sha256: AAAA1111BBBB2222CCCC3333DDDD4444EEEE5555FFFF6666AAAA7777BBBB8888
    provenance:
      source_repository: https://github.com/spiritbuun/buun-llama-cpp
      source_revision: 799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee
      checkpoint_fix:
        reference: https://github.com/ggml-org/llama.cpp/pull/22384
        evidence: semantic-equivalent
models:
  - id: local-coding
    state: candidate
    deployments: [canary]
    runtime: amd-rocm-baseline
    context_tokens: 131072
    resources:
      peak_commit_gib: 9.0
      peak_vram_gib: null
      peak_ram_gib: null
  - id: qwen38-27b-buun
    state: candidate
    deployments: [canary]
    runtime: amd-rocm-qwen38-buun
    context_tokens: 262144
    cache_ram_mib: 0
    ctx_checkpoints: 64
    checkpoint_min_step: 512
    tensor_overrides:
      - pattern: 'blk\.(4[4-9]|5[0-9]|6[0-3])\.ffn_.*'
        buffer: CPU
    resources:
      peak_commit_gib: null
      peak_vram_gib: null
      peak_ram_gib: null
`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadForkDeployment(t *testing.T) map[string]Model {
	t.Helper()
	models, _, err := LoadModels(writeManifest(t, forkManifest), "canary")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	return byID
}

func TestForkRuntimeIsIdentifiedByProvenanceNotByName(t *testing.T) {
	models := loadForkDeployment(t)
	fork := models["qwen38-27b-buun"].Runtime

	if fork.Variant != "fork" {
		t.Fatalf("runtime variant = %q, want fork", fork.Variant)
	}
	if fork.Engine != "llama.cpp" {
		t.Fatalf("engine = %q; a fork preserving the llama-server interface is still llama.cpp", fork.Engine)
	}
	if fork.Commit != "799e3995cd4f19aa9f6a3fa9fb5b4674422bf0ee" {
		t.Fatalf("commit = %q, want the pinned revision", fork.Commit)
	}
	if fork.ArtifactSHA256Prefix != "aaaa1111bbbb" {
		t.Fatalf("artifact prefix = %q, want the first twelve hex digits of the hash", fork.ArtifactSHA256Prefix)
	}
	if !fork.CheckpointCapable {
		t.Fatal("a runtime carrying checkpoint-fix evidence was not reported as checkpoint capable")
	}
	if fork.Backend != "ROCm" {
		t.Fatalf("backend = %q, want ROCm", fork.Backend)
	}
}

// The baseline must be untouched by the fork's arrival. Reporting it as an
// unknown variant, or as checkpoint capable, would make the distinction the
// whole change exists to draw meaningless.
func TestUpstreamRuntimeKeepsItsIdentityWhenAForkIsPresent(t *testing.T) {
	models := loadForkDeployment(t)
	baseline := models["local-coding"].Runtime

	if baseline.Variant != "upstream" {
		t.Fatalf("baseline variant = %q, want upstream", baseline.Variant)
	}
	if baseline.CheckpointCapable {
		t.Fatal("the upstream baseline was reported as able to restore hybrid checkpoints")
	}
	if baseline.Commit != "" || baseline.SourceRepository != "" {
		t.Fatalf("the baseline acquired fork provenance: %+v", baseline)
	}
	if models["local-coding"].Checkpoints.Configured() {
		t.Fatal("the baseline model acquired a checkpoint configuration")
	}
}

func TestCheckpointConfigurationIsReportedOnlyWhereDeclared(t *testing.T) {
	models := loadForkDeployment(t)

	fork := models["qwen38-27b-buun"].Checkpoints
	if !fork.Configured() {
		t.Fatal("a declared checkpoint policy was not reported")
	}
	if fork.Count == nil || *fork.Count != 64 || fork.MinStep == nil || *fork.MinStep != 512 {
		t.Fatalf("checkpoint policy = %+v, want 64 checkpoints at a 512-token minimum step", fork)
	}
	if summary := models["local-coding"].Checkpoints; summary.Count != nil || summary.MinStep != nil {
		t.Fatalf("an undeclared checkpoint policy was reported as %+v; absent is not zero", summary)
	}
}

// A host prompt cache pinned to zero has to survive the round trip as zero. If
// it read back as absent, admission would stop distinguishing "the operator
// disabled the cache" from "nobody said", and on a fork whose default is 8 GiB
// those are opposite configurations.
func TestDisabledPromptCacheIsCarriedAsZeroNotAsAbsent(t *testing.T) {
	models := loadForkDeployment(t)
	cache := models["qwen38-27b-buun"].CacheRAMMiB
	if cache == nil {
		t.Fatal("cache_ram_mib: 0 was read as absent")
	}
	if *cache != 0 {
		t.Fatalf("cache_ram_mib = %d, want 0", *cache)
	}
	if models["local-coding"].CacheRAMMiB != nil {
		t.Fatal("the baseline model acquired a prompt cache setting")
	}
}

// The fork profile offloads tensors, so admission needs all three measurements
// plus the device budget. Until the runtime has actually been built and measured
// on the target GPU there are none, and the canary escape hatch must not cover
// it: a 27B holding weights in system RAM is the case the gate exists for.
func TestForkProfileFailsClosedWithoutMeasurements(t *testing.T) {
	models := loadForkDeployment(t)
	fork := models["qwen38-27b-buun"]

	missing := missingProfileFields(fork)
	want := []string{
		"resources.peak_commit_gib",
		"resources.peak_vram_gib",
		"resources.peak_ram_gib",
	}
	for _, field := range want {
		if !containsString(missing, field) {
			t.Fatalf("missing profile fields = %v, want %s among them", missing, field)
		}
	}

	status := capacityFrom(fork, []Model{fork}, map[string]string{}, nil, memorySnapshot{CommitGiB: 40, PhysicalGiB: 20}, nil)
	if status.Available {
		t.Fatal("an unmeasured offloading fork profile was admitted")
	}
	if status.Reason != "resource_profile_incomplete" {
		t.Fatalf("refusal reason = %q, want resource_profile_incomplete", status.Reason)
	}
	if status.Measured {
		t.Fatal("an incomplete profile was reported as measured")
	}
}

// Measured, the same profile is admitted or refused on its numbers alone. There
// is no fork-specific allowance anywhere in this path, which is the point.
func TestForkProfileIsAdmittedOnMeasurementsLikeAnyOther(t *testing.T) {
	models := loadForkDeployment(t)
	fork := models["qwen38-27b-buun"]
	fork.PeakCommitGiB = floatPointer(23.3)
	fork.PeakVRAMGiB = floatPointer(12.45)
	fork.PeakRAMGiB = floatPointer(7.4)

	roomy := capacityFrom(fork, []Model{fork}, map[string]string{}, nil, memorySnapshot{CommitGiB: 40, PhysicalGiB: 20}, nil)
	if !roomy.Available || roomy.Reason != "commit_headroom_available" {
		t.Fatalf("a measured profile with headroom was refused: %+v", roomy)
	}
	if !roomy.Measured {
		t.Fatal("a complete profile was not reported as measured")
	}

	for name, test := range map[string]struct {
		memory memorySnapshot
		mutate func(*Model)
		reason string
	}{
		"commit exhausted":          {memory: memorySnapshot{CommitGiB: 10, PhysicalGiB: 20}, reason: "insufficient_commit_headroom"},
		"physical memory exhausted": {memory: memorySnapshot{CommitGiB: 40, PhysicalGiB: 8}, reason: "insufficient_physical_memory"},
		"beyond the device budget": {
			memory: memorySnapshot{CommitGiB: 40, PhysicalGiB: 20},
			mutate: func(model *Model) { model.PeakVRAMGiB = floatPointer(15.6) },
			reason: "insufficient_vram_budget",
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := fork
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			status := capacityFrom(candidate, []Model{candidate}, map[string]string{}, nil, test.memory, nil)
			if status.Available {
				t.Fatalf("the fork profile was admitted despite %s", name)
			}
			if status.Reason != test.reason {
				t.Fatalf("refusal reason = %q, want %q", status.Reason, test.reason)
			}
		})
	}
}

// Adopting the fork changes the fork's model and nothing else. This is asserted
// against the real repository manifest so it keeps holding as that file evolves.
func TestAdoptingAForkLeavesTheRepositoryDeploymentUnchanged(t *testing.T) {
	path := filepath.Join("..", "..", "config", "models.yaml")
	before, publicModel, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if publicModel != "local-coding" {
		t.Fatalf("provider.public_model = %q; adopting a fork must not move it", publicModel)
	}
	for _, model := range before {
		if model.Runtime.Variant != "upstream" {
			t.Fatalf("model %q already runs on a %q runtime; the fork is not meant to be adopted in the repository manifest until it is built and hashed",
				model.ID, model.Runtime.Variant)
		}
		if model.Runtime.CheckpointCapable {
			t.Fatalf("model %q claims a checkpoint-capable runtime without provenance", model.ID)
		}
	}
}

// The status document is the operator's view of which build is serving. It must
// name the runtime and never leak where it was installed.
func TestStatusReportsRuntimeIdentityWithoutInstallationPaths(t *testing.T) {
	models := loadForkDeployment(t)
	config := testConfig("http://127.0.0.1:19292")
	config.Models = []Model{models["local-coding"], models["qwen38-27b-buun"]}
	config.PublicModelID = "local-coding"
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = func() (memorySnapshot, error) {
		return memorySnapshot{CommitGiB: 40, PhysicalGiB: 20}, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Host = "127.0.0.1:18091"
	request.Header.Set("Authorization", "Bearer "+config.AdminToken)
	recorder := httptest.NewRecorder()
	server.ControlHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Runtimes []RuntimeSummary `json:"runtimes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Runtimes) != 2 {
		t.Fatalf("status reported %d runtimes, want both", len(payload.Runtimes))
	}
	variants := map[string]RuntimeSummary{}
	for _, runtime := range payload.Runtimes {
		variants[runtime.Variant] = runtime
	}
	if variants["fork"].Commit == "" || variants["fork"].ArtifactSHA256Prefix == "" {
		t.Fatalf("the fork runtime is not identifiable from the status: %+v", variants["fork"])
	}
	if variants["upstream"].ID == "" {
		t.Fatal("the upstream baseline disappeared from the status")
	}

	body := recorder.Body.String()
	for _, forbidden := range []string{`C:\`, "llama-server.exe", config.AdminToken, config.InferenceToken} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the status document leaked %q", forbidden)
		}
	}
}

// /v1/models is a public contract. Runtime identity is operator-facing and must
// not appear there.
func TestPublicModelListDoesNotCarryRuntimeIdentity(t *testing.T) {
	models := loadForkDeployment(t)
	config := testConfig("http://127.0.0.1:19292")
	config.Models = []Model{models["qwen38-27b-buun"]}
	config.PublicModelID = "qwen38-27b-buun"
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Host = "127.0.0.1:18090"
	request.Header.Set("Authorization", "Bearer "+config.InferenceToken)
	recorder := httptest.NewRecorder()
	server.DataHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("models returned %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"variant", "commit", "artifact_sha256_prefix", "checkpoint"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("the public model list exposed %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
