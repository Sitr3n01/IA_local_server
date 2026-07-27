package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const maxManifestBytes = 16 << 20

var modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// Capabilities are the protocol features qualified for a model. They are used
// both for display and to keep a launcher from selecting an incompatible model.
type Capabilities struct {
	Responses        bool `json:"responses"`
	ChatCompletions  bool `json:"chat_completions"`
	Streaming        bool `json:"streaming"`
	FunctionCalling  bool `json:"function_calling"`
	StructuredOutput bool `json:"structured_output"`
}

// Model is the panel-safe projection of one manifest model. Runtime paths,
// hashes, templates, and resource internals remain owned by the manifest and
// router rather than being copied into panel state.
type Model struct {
	ID                string       `json:"id"`
	DisplayName       string       `json:"display_name"`
	State             string       `json:"state"`
	Runtime           string       `json:"runtime"`
	ArtifactPath      string       `json:"artifact_path"`
	ArtifactBytes     int64        `json:"artifact_bytes"`
	ArtifactSHA256    string       `json:"artifact_sha256"`
	Deployments       []string     `json:"deployments"`
	Capabilities      Capabilities `json:"capabilities"`
	ContextTokens     int          `json:"context_tokens"`
	MaxOutputTokens   int          `json:"max_output_tokens"`
	CacheTypeK        string       `json:"cache_type_k"`
	CacheTypeV        string       `json:"cache_type_v"`
	GPULayers         int          `json:"gpu_layers"`
	Available         bool         `json:"available"`
	UnavailableReason string       `json:"unavailable_reason,omitempty"`
}

// CanLaunchCodex reports whether the model may be selected by the local Codex
// harness. Capability flags remain descriptive: operators may intentionally
// open a client with a model that has weaker tool-use behavior.
func (m Model) CanLaunchCodex() bool {
	return m.Available
}

// CanLaunchOpenCode follows the same operator-selection policy as Codex. The
// edge supplies the protocol surface; model-level capability badges describe
// expected behavior without hiding the launcher.
func (m Model) CanLaunchOpenCode() bool {
	return m.Available
}

// Catalog preserves manifest order while attaching deployment-specific
// availability to every model, including candidates that cannot yet launch.
type Catalog struct {
	PublicModel string      `json:"public_model"`
	Environment Environment `json:"environment"`

	models []Model
	byID   map[string]int
}

type manifestProjection struct {
	SchemaVersion int `json:"schema_version"`
	Provider      struct {
		PublicModel string `json:"public_model"`
	} `json:"provider"`
	Models []manifestModelProjection `json:"models"`
}

type manifestModelProjection struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	State       string `json:"state"`
	Runtime     string `json:"runtime"`
	Artifact    struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"artifact"`
	Deployments     []string `json:"deployments"`
	ContextTokens   *int     `json:"context_tokens"`
	MaxOutputTokens *int     `json:"max_output_tokens"`
	CacheTypeK      string   `json:"cache_type_k"`
	CacheTypeV      string   `json:"cache_type_v"`
	GPULayers       *int     `json:"gpu_layers"`
	Capabilities    struct {
		Responses        *bool `json:"responses"`
		ChatCompletions  *bool `json:"chat_completions"`
		Streaming        *bool `json:"streaming"`
		FunctionCalling  *bool `json:"function_calling"`
		StructuredOutput *bool `json:"structured_output"`
	} `json:"capabilities"`
}

// LoadCatalog reads the JSON-compatible models.yaml source of truth. YAML-only
// features are intentionally rejected so every consumer sees identical data.
func LoadCatalog(path string, environment Environment) (*Catalog, error) {
	if !environment.Valid() {
		return nil, errors.New("catalog environment must be canary or final")
	}
	data, err := readLimitedFile(path, maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("open model manifest: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	var manifest manifestProjection
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode JSON-compatible model manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode JSON-compatible model manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return nil, errors.New("model manifest schema_version must be 1")
	}
	publicModel := strings.TrimSpace(manifest.Provider.PublicModel)
	if !modelIDPattern.MatchString(publicModel) || publicModel != manifest.Provider.PublicModel {
		return nil, errors.New("model manifest provider.public_model is required")
	}
	if len(manifest.Models) == 0 {
		return nil, errors.New("model manifest must contain at least one model")
	}

	catalog := &Catalog{
		PublicModel: publicModel,
		Environment: environment,
		models:      make([]Model, 0, len(manifest.Models)),
		byID:        make(map[string]int, len(manifest.Models)),
	}
	for _, entry := range manifest.Models {
		model, err := projectModel(entry, environment)
		if err != nil {
			return nil, err
		}
		if _, duplicate := catalog.byID[model.ID]; duplicate {
			return nil, fmt.Errorf("model manifest contains duplicate model ID %q", model.ID)
		}
		catalog.byID[model.ID] = len(catalog.models)
		catalog.models = append(catalog.models, model)
	}
	if _, found := catalog.byID[catalog.PublicModel]; !found {
		return nil, fmt.Errorf("public model %q is not present in model manifest", catalog.PublicModel)
	}
	return catalog, nil
}

func projectModel(entry manifestModelProjection, environment Environment) (Model, error) {
	id := strings.TrimSpace(entry.ID)
	if !modelIDPattern.MatchString(id) || id != entry.ID {
		return Model{}, fmt.Errorf("model manifest contains invalid model ID %q", entry.ID)
	}
	displayName := strings.TrimSpace(entry.DisplayName)
	if displayName == "" || displayName != entry.DisplayName {
		return Model{}, fmt.Errorf("model %q display_name is required", id)
	}
	state := entry.State
	switch state {
	case "candidate", "qualified", "enabled", "disabled", "retired":
	default:
		return Model{}, fmt.Errorf("model %q has unsupported state %q", id, entry.State)
	}
	if entry.ContextTokens == nil || *entry.ContextTokens <= 0 {
		return Model{}, fmt.Errorf("model %q context_tokens must be positive", id)
	}
	if entry.MaxOutputTokens == nil || *entry.MaxOutputTokens <= 0 {
		return Model{}, fmt.Errorf("model %q max_output_tokens must be positive", id)
	}
	if strings.TrimSpace(entry.Runtime) == "" || strings.TrimSpace(entry.Artifact.Path) == "" || entry.Artifact.Bytes <= 0 {
		return Model{}, fmt.Errorf("model %q runtime and artifact are required", id)
	}
	if !regexp.MustCompile(`^[A-Fa-f0-9]{64}$`).MatchString(entry.Artifact.SHA256) {
		return Model{}, fmt.Errorf("model %q artifact sha256 is invalid", id)
	}
	if entry.GPULayers == nil || *entry.GPULayers < 0 || strings.TrimSpace(entry.CacheTypeK) == "" || strings.TrimSpace(entry.CacheTypeV) == "" {
		return Model{}, fmt.Errorf("model %q execution profile is incomplete", id)
	}
	if *entry.MaxOutputTokens > *entry.ContextTokens {
		return Model{}, fmt.Errorf("model %q max_output_tokens cannot exceed context_tokens", id)
	}
	if entry.Capabilities.Responses == nil || entry.Capabilities.ChatCompletions == nil ||
		entry.Capabilities.Streaming == nil || entry.Capabilities.FunctionCalling == nil ||
		entry.Capabilities.StructuredOutput == nil {
		return Model{}, fmt.Errorf("model %q capabilities are incomplete", id)
	}

	deployments := make([]string, len(entry.Deployments))
	deployed := false
	seenDeployments := make(map[string]struct{}, len(entry.Deployments))
	for index, deployment := range entry.Deployments {
		if deployment != string(EnvironmentCanary) && deployment != string(EnvironmentFinal) {
			return Model{}, fmt.Errorf("model %q has unsupported deployment %q", id, deployment)
		}
		if _, duplicate := seenDeployments[deployment]; duplicate {
			return Model{}, fmt.Errorf("model %q repeats deployment %q", id, deployment)
		}
		seenDeployments[deployment] = struct{}{}
		deployments[index] = deployment
		if deployment == string(environment) {
			deployed = true
		}
	}

	available := deployed && state != "disabled" && state != "retired"
	reason := ""
	if state == "disabled" || state == "retired" {
		reason = "model state is " + state
	} else if !deployed {
		reason = "model is not deployed to " + string(environment)
	}
	return Model{
		ID:             id,
		DisplayName:    displayName,
		State:          state,
		Runtime:        entry.Runtime,
		ArtifactPath:   entry.Artifact.Path,
		ArtifactBytes:  entry.Artifact.Bytes,
		ArtifactSHA256: strings.ToUpper(entry.Artifact.SHA256),
		Deployments:    deployments,
		Capabilities: Capabilities{
			Responses:        *entry.Capabilities.Responses,
			ChatCompletions:  *entry.Capabilities.ChatCompletions,
			Streaming:        *entry.Capabilities.Streaming,
			FunctionCalling:  *entry.Capabilities.FunctionCalling,
			StructuredOutput: *entry.Capabilities.StructuredOutput,
		},
		ContextTokens:     *entry.ContextTokens,
		MaxOutputTokens:   *entry.MaxOutputTokens,
		CacheTypeK:        entry.CacheTypeK,
		CacheTypeV:        entry.CacheTypeV,
		GPULayers:         *entry.GPULayers,
		Available:         available,
		UnavailableReason: reason,
	}, nil
}

// Model returns a defensive copy of one catalog entry.
func (c *Catalog) Model(id string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	index, found := c.byID[id]
	if !found {
		return Model{}, false
	}
	return cloneModel(c.models[index]), true
}

// AllModels returns all entries in manifest order.
func (c *Catalog) AllModels() []Model {
	if c == nil {
		return nil
	}
	models := make([]Model, len(c.models))
	for index := range c.models {
		models[index] = cloneModel(c.models[index])
	}
	return models
}

// AvailableModels returns only models eligible for this catalog environment,
// preserving manifest order.
func (c *Catalog) AvailableModels() []Model {
	if c == nil {
		return nil
	}
	models := make([]Model, 0, len(c.models))
	for _, model := range c.models {
		if model.Available {
			models = append(models, cloneModel(model))
		}
	}
	return models
}

func cloneModel(model Model) Model {
	model.Deployments = append([]string(nil), model.Deployments...)
	return model
}
