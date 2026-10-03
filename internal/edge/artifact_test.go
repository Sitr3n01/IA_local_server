package edge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func int64Ptr(value int64) *int64 { return &value }

func TestCheckArtifactSeparatesMissingFromUnknownFromPresent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(file, make([]byte, 128), 0o600); err != nil {
		t.Fatal(err)
	}

	present := checkArtifact(file, int64Ptr(128))
	if present.Present == nil || !*present.Present || present.SizeMatches == nil || !*present.SizeMatches || present.refuse() != "" {
		t.Fatalf("present = %+v", present)
	}
	noSize := checkArtifact(file, nil)
	if noSize.Present == nil || !*noSize.Present || noSize.SizeMatches != nil || noSize.refuse() != "" {
		t.Fatalf("a manifest without a size claimed one: %+v", noSize)
	}
	truncated := checkArtifact(file, int64Ptr(4096))
	if truncated.SizeMatches == nil || *truncated.SizeMatches || truncated.refuse() != reasonArtifactSizeMismatch {
		t.Fatalf("truncated = %+v", truncated)
	}
	missing := checkArtifact(filepath.Join(dir, "gone.gguf"), int64Ptr(128))
	if missing.Present == nil || *missing.Present || missing.SizeMatches != nil || missing.refuse() != reasonArtifactMissing {
		t.Fatalf("missing = %+v", missing)
	}
	// A directory where a file should be is not the model.
	if asDirectory := checkArtifact(dir, nil); asDirectory.refuse() != reasonArtifactMissing {
		t.Fatalf("a directory counted as the artifact: %+v", asDirectory)
	}
	// No path in the manifest is "nothing known", never "missing".
	if unknown := checkArtifact("  ", int64Ptr(1)); unknown.Present != nil || unknown.refuse() != "" {
		t.Fatalf("an empty path = %+v", unknown)
	}
}

func TestStatusReportsAMissingWeightsFileAndNeverItsPath(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present-Q4_K_M.gguf")
	if err := os.WriteFile(present, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "deleted-Q4_K_M.gguf")

	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"running":[]}`))
	}))
	server.memoryStatus = fixedMemory(20, 24)
	template := server.cfg.Models[0]
	first, second, third := template, template, template
	first.ID, first.ArtifactPath, first.ArtifactBytes = "present", present, int64Ptr(64)
	second.ID, second.ArtifactPath, second.ArtifactBytes = "deleted", gone, int64Ptr(64)
	third.ID, third.ArtifactPath, third.ArtifactBytes = "shrunk", present, int64Ptr(9999)
	server.cfg.Models = []Model{first, second, third}
	server.cfg.PublicModelID = "present"

	recorder := controlRequest(t, server.ControlHandler(), http.MethodGet, "/api/v1/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var status struct {
		ModelStatuses []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
			Artifact  struct {
				Present     *bool `json:"present"`
				SizeMatches *bool `json:"size_matches"`
			} `json:"artifact"`
		} `json:"model_statuses"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for index, entry := range status.ModelStatuses {
		byID[entry.ID] = index
	}
	got := status.ModelStatuses[byID["deleted"]]
	if got.Available || got.Reason != reasonArtifactMissing || got.Artifact.Present == nil || *got.Artifact.Present {
		t.Fatalf("deleted = %+v", got)
	}
	got = status.ModelStatuses[byID["shrunk"]]
	if got.Available || got.Reason != reasonArtifactSizeMismatch || got.Artifact.SizeMatches == nil || *got.Artifact.SizeMatches {
		t.Fatalf("shrunk = %+v", got)
	}
	got = status.ModelStatuses[byID["present"]]
	if got.Reason == reasonArtifactMissing || got.Reason == reasonArtifactSizeMismatch || got.Artifact.Present == nil || !*got.Artifact.Present {
		t.Fatalf("present = %+v", got)
	}
	// The path identifies the machine and is never published.
	for _, leaked := range []string{dir, "deleted-Q4_K_M", filepath.ToSlash(dir)} {
		if strings.Contains(recorder.Body.String(), leaked) {
			t.Fatalf("the status leaked %q: %s", leaked, recorder.Body.String())
		}
	}
}

func TestLoadModelsCarriesTheArtifactLocationWithoutPublishingIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.yaml")
	data := []byte(`provider:
  public_model: m
models:
  - id: m
    state: candidate
    deployments: [canary]
    artifact:
      path: 'C:\models\m-Q4_K_M.gguf'
      bytes: 4096
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, _, err := LoadModels(path, "canary")
	if err != nil {
		t.Fatal(err)
	}
	if models[0].ArtifactPath != `C:\models\m-Q4_K_M.gguf` || models[0].ArtifactBytes == nil || *models[0].ArtifactBytes != 4096 {
		t.Fatalf("model = %+v", models[0])
	}
	published, err := json.Marshal(models[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(published), "Q4_K_M.gguf") || strings.Contains(string(published), "4096") {
		t.Fatalf("the artifact location reached /v1/models: %s", published)
	}
}
