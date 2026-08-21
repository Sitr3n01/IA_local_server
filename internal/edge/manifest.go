package edge

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type modelManifest struct {
	Provider struct {
		PublicModel string `yaml:"public_model"`
	} `yaml:"provider"`
	Runtimes []manifestRuntime `yaml:"runtimes"`
	Models   []manifestModel   `yaml:"models"`
}

type manifestRuntime struct {
	ID      string `yaml:"id"`
	State   string `yaml:"state"`
	Engine  string `yaml:"engine"`
	Variant string `yaml:"variant"`
	Device  struct {
		VRAMMiB *int   `yaml:"vram_mib"`
		Backend string `yaml:"backend"`
	} `yaml:"device"`
	Artifact struct {
		SHA256 string `yaml:"sha256"`
	} `yaml:"artifact"`
	Provenance *struct {
		SourceRepository string `yaml:"source_repository"`
		SourceRevision   string `yaml:"source_revision"`
		CheckpointFix    *struct {
			Reference string `yaml:"reference"`
			Evidence  string `yaml:"evidence"`
		} `yaml:"checkpoint_fix"`
	} `yaml:"provenance"`
}

type manifestModel struct {
	ID               string   `yaml:"id"`
	State            string   `yaml:"state"`
	Status           string   `yaml:"status"`
	Enabled          *bool    `yaml:"enabled"`
	OwnedBy          string   `yaml:"owned_by"`
	Deployments      []string `yaml:"deployments"`
	Runtime          string   `yaml:"runtime"`
	CacheRAMMiB      *int     `yaml:"cache_ram_mib"`
	ContextTokens    *int     `yaml:"context_tokens"`
	MaxOutputTokens  *int     `yaml:"max_output_tokens"`
	NPredict         *int     `yaml:"n_predict"`
	ReasoningBudget  *int     `yaml:"reasoning_budget"`
	CompactThreshold *int     `yaml:"compact_threshold_tokens"`
	CacheTypeK       string   `yaml:"cache_type_k"`
	CacheTypeV       string   `yaml:"cache_type_v"`
	Artifact         struct {
		Path string `yaml:"path"`
	} `yaml:"artifact"`
	CtxCheckpoints    *int `yaml:"ctx_checkpoints"`
	CheckpointMinStep *int `yaml:"checkpoint_min_step"`
	// Presence, not content: the edge only needs to know that part of the model
	// lives outside VRAM, which makes an unmeasured capacity profile unsafe.
	TensorOverrides []struct {
		Pattern string `yaml:"pattern"`
	} `yaml:"tensor_overrides"`
	Resources struct {
		PeakCommitGiB *float64 `yaml:"peak_commit_gib"`
		PeakVRAMGiB   *float64 `yaml:"peak_vram_gib"`
		PeakRAMGiB    *float64 `yaml:"peak_ram_gib"`
	} `yaml:"resources"`
}

// weightsName reduces a GGUF path to the filename an operator recognises. The
// full path is deliberately not reported: /api/v1/status is reachable by the
// MCP adapter, and a local filesystem layout is not something it needs.
func weightsName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if idx := strings.LastIndexAny(trimmed, `\/`); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.TrimSuffix(trimmed, ".gguf")
}

// LoadModels reads the generated, version-controlled source of truth used to
// construct the public model allowlist for one explicit deployment. It also
// returns provider.public_model, because array order carries no meaning: the
// public model is named, not positional.
func LoadModels(path, environment string) ([]Model, string, error) {
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment != "canary" && environment != "final" {
		return nil, "", errorsForManifest("environment must be canary or final")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read models config: %w", err)
	}
	var manifest modelManifest
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(false)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, "", fmt.Errorf("decode models config: %w", err)
	}

	publicModel := strings.TrimSpace(manifest.Provider.PublicModel)
	if publicModel == "" {
		return nil, "", errorsForManifest("provider.public_model is required")
	}
	deviceVRAM := make(map[string]*float64, len(manifest.Runtimes))
	runtimes := make(map[string]RuntimeSummary, len(manifest.Runtimes))
	for _, runtime := range manifest.Runtimes {
		id := strings.TrimSpace(runtime.ID)
		if id == "" {
			continue
		}
		if runtime.Device.VRAMMiB != nil {
			budget := float64(*runtime.Device.VRAMMiB) / 1024
			deviceVRAM[id] = &budget
		}
		runtimes[id] = summarizeRuntime(runtime)
	}

	seen := make(map[string]struct{})
	models := make([]Model, 0, len(manifest.Models))
	publicFound := false
	for _, entry := range manifest.Models {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			return nil, "", errorsForManifest("model ID cannot be empty")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, "", errorsForManifest(fmt.Sprintf("duplicate model ID %q", id))
		}
		seen[id] = struct{}{}
		state := strings.ToLower(strings.TrimSpace(entry.State))
		if state == "" {
			state = strings.ToLower(strings.TrimSpace(entry.Status))
		}
		enabled := entry.Enabled == nil || *entry.Enabled
		if !enabled || state == "retired" || state == "disabled" {
			if id == publicModel {
				return nil, "", errorsForManifest(fmt.Sprintf("public model %q is disabled or retired", id))
			}
			continue
		}
		if state != "candidate" && state != "qualified" && state != "enabled" {
			return nil, "", errorsForManifest(fmt.Sprintf("model %q has unsupported state %q", id, state))
		}
		if !containsDeployment(entry.Deployments, environment) {
			continue
		}
		if environment == "final" && state == "candidate" {
			return nil, "", errorsForManifest(fmt.Sprintf("candidate model %q cannot be deployed to final", id))
		}
		ownedBy := strings.TrimSpace(entry.OwnedBy)
		if ownedBy == "" {
			ownedBy = "local"
		}
		models = append(models, Model{
			ID:              id,
			Object:          "model",
			OwnedBy:         ownedBy,
			State:           state,
			Deployments:     append([]string(nil), entry.Deployments...),
			PeakCommitGiB:   entry.Resources.PeakCommitGiB,
			PeakVRAMGiB:     entry.Resources.PeakVRAMGiB,
			PeakRAMGiB:      entry.Resources.PeakRAMGiB,
			DeviceVRAMGiB:   deviceVRAM[strings.TrimSpace(entry.Runtime)],
			CacheRAMMiB:     entry.CacheRAMMiB,
			OffloadsTensors: len(entry.TensorOverrides) > 0,
			Runtime:         runtimes[strings.TrimSpace(entry.Runtime)],
			ContextTokens:   entry.ContextTokens,
			Profile: ProfileSummary{
				MaxOutputTokens:  entry.MaxOutputTokens,
				NPredict:         entry.NPredict,
				ReasoningBudget:  entry.ReasoningBudget,
				CompactThreshold: entry.CompactThreshold,
				CacheTypeK:       strings.TrimSpace(entry.CacheTypeK),
				CacheTypeV:       strings.TrimSpace(entry.CacheTypeV),
				Weights:          weightsName(entry.Artifact.Path),
			},
			Checkpoints: CheckpointSummary{
				Count:   entry.CtxCheckpoints,
				MinStep: entry.CheckpointMinStep,
			},
		})
		if id == publicModel {
			publicFound = true
		}
	}
	if !publicFound {
		return nil, "", errorsForManifest(fmt.Sprintf("provider.public_model %q is not deployed to %s", publicModel, environment))
	}
	return models, publicModel, nil
}

// summarizeRuntime reduces a manifest runtime to what an operator needs to tell
// two builds apart at a glance, and nothing more. The installation path is
// deliberately absent: it identifies the machine rather than the runtime, and
// the runtime's identity is its repository, commit, and artifact hash.
//
// The artifact hash is abbreviated. The full value lives in the manifest, which
// is where a verification belongs; twelve hex digits are enough to notice that
// the running build is not the one that was qualified.
func summarizeRuntime(runtime manifestRuntime) RuntimeSummary {
	summary := RuntimeSummary{
		ID:      strings.TrimSpace(runtime.ID),
		State:   strings.ToLower(strings.TrimSpace(runtime.State)),
		Engine:  strings.TrimSpace(runtime.Engine),
		Variant: strings.ToLower(strings.TrimSpace(runtime.Variant)),
		Backend: strings.TrimSpace(runtime.Device.Backend),
	}
	if summary.Variant == "" {
		// Every runtime that predates fork support is an upstream release
		// build, and reporting it as unknown would make the distinction the
		// field exists to draw look absent rather than settled.
		summary.Variant = "upstream"
	}
	if sha := strings.ToLower(strings.TrimSpace(runtime.Artifact.SHA256)); len(sha) >= 12 {
		summary.ArtifactSHA256Prefix = sha[:12]
	}
	if runtime.Provenance != nil {
		summary.SourceRepository = strings.TrimSpace(runtime.Provenance.SourceRepository)
		summary.Commit = strings.ToLower(strings.TrimSpace(runtime.Provenance.SourceRevision))
		if fix := runtime.Provenance.CheckpointFix; fix != nil && strings.TrimSpace(fix.Evidence) != "" {
			// Capability, not configuration: it records that this build was
			// shown to restore checkpoints on a hybrid/recurrent model, which is
			// the reason a checkpoint configuration on it means anything.
			summary.CheckpointCapable = true
			summary.CheckpointFixReference = strings.TrimSpace(fix.Reference)
		}
	}
	return summary
}

func containsDeployment(deployments []string, wanted string) bool {
	for _, deployment := range deployments {
		if strings.EqualFold(strings.TrimSpace(deployment), wanted) {
			return true
		}
	}
	return false
}

func errorsForManifest(message string) error {
	return fmt.Errorf("invalid models config: %s", message)
}
