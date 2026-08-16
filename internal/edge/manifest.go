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
	ID     string `yaml:"id"`
	Device struct {
		VRAMMiB *int `yaml:"vram_mib"`
	} `yaml:"device"`
}

type manifestModel struct {
	ID          string   `yaml:"id"`
	State       string   `yaml:"state"`
	Status      string   `yaml:"status"`
	Enabled     *bool    `yaml:"enabled"`
	OwnedBy     string   `yaml:"owned_by"`
	Deployments []string `yaml:"deployments"`
	Runtime     string   `yaml:"runtime"`
	CacheRAMMiB *int     `yaml:"cache_ram_mib"`
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
	for _, runtime := range manifest.Runtimes {
		id := strings.TrimSpace(runtime.ID)
		if id == "" {
			continue
		}
		if runtime.Device.VRAMMiB != nil {
			budget := float64(*runtime.Device.VRAMMiB) / 1024
			deviceVRAM[id] = &budget
		}
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
