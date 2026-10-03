package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The monitor reads two unauthenticated control-plane routes and nothing else.
// It holds no credential, so there is nothing in it to steal, and it can only
// ever show what the edge already publishes to any local process.
const (
	edgeStatusPath    = "/api/v1/status"
	edgeInferencePath = "/api/v1/inference"
	maxEdgeBody       = 4 << 20
)

// errTelemetryUnavailable is an edge that predates /api/v1/inference: the
// monitor still works, without per-request numbers.
var errTelemetryUnavailable = errors.New("edge does not publish inference telemetry")

// The types below are a projection, not a mirror. Decoding into them drops
// every field the page has no use for - recent request IDs, the router URL -
// so a field added to the edge's status later is not forwarded by default.

type edgeStatus struct {
	Version       string            `json:"version"`
	Ready         bool              `json:"ready"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Upstream      edgeUpstream      `json:"upstream"`
	ActiveModel   string            `json:"active_model"`
	Gate          edgeGate          `json:"gate"`
	Capacity      edgeCapacity      `json:"capacity"`
	GPUMemory     edgeGPUMemory     `json:"gpu_memory"`
	Maintenance   edgeMaintenance   `json:"maintenance"`
	Models        []edgeModel       `json:"models"`
	ModelStatuses []edgeModelStatus `json:"model_statuses"`
	Runtimes      []edgeRuntime     `json:"runtimes"`
	Deployment    *edgeDeployment   `json:"deployment,omitempty"`
}

type edgeUpstream struct {
	Reachable bool `json:"reachable"`
}

type edgeGate struct {
	Active      int64  `json:"active"`
	Queued      int64  `json:"queued"`
	MaxActive   int    `json:"max_active"`
	MaxQueue    int    `json:"max_queue"`
	WaitSeconds int    `json:"wait_timeout_seconds"`
	Rejected    uint64 `json:"rejected_total"`
	TimedOut    uint64 `json:"timed_out_total"`
}

type edgeCapacity struct {
	Model               string   `json:"model"`
	ModelRunning        bool     `json:"model_running"`
	CommitHeadroomGiB   *float64 `json:"commit_headroom_gib"`
	RequiredCommitGiB   *float64 `json:"required_commit_gib"`
	PhysicalHeadroomGiB *float64 `json:"physical_headroom_gib"`
	RequiredPhysicalGiB *float64 `json:"required_physical_gib"`
	RequiredVRAMGiB     *float64 `json:"required_vram_gib"`
	DeviceVRAMGiB       *float64 `json:"device_vram_gib"`
	Measured            bool     `json:"measured"`
	Available           bool     `json:"available"`
	Reason              string   `json:"reason"`
}

type edgeGPUMemory struct {
	State        string   `json:"state"`
	Occupancy    *float64 `json:"occupancy"`
	DedicatedMiB *float64 `json:"dedicated_mib"`
	SharedMiB    *float64 `json:"shared_mib"`
	BudgetMiB    *float64 `json:"budget_mib"`
	Adapter      string   `json:"adapter"`
}

type edgeMaintenance struct {
	State    string `json:"state"`
	Draining bool   `json:"draining"`
	Drained  bool   `json:"drained"`
}

type edgeModel struct {
	ID           string           `json:"id"`
	DisplayName  string           `json:"display_name"`
	Capabilities edgeCapabilities `json:"capabilities"`
}

type edgeCapabilities struct {
	Responses        bool `json:"responses"`
	ChatCompletions  bool `json:"chat_completions"`
	Streaming        bool `json:"streaming"`
	FunctionCalling  bool `json:"function_calling"`
	StructuredOutput bool `json:"structured_output"`
	Reasoning        bool `json:"reasoning"`
}

// edgeArtifact says whether a model's weights file is still on disk, as the
// edge sees it. Both answers are absent when the edge could not tell, and an
// edge that predates the report sends neither.
type edgeArtifact struct {
	Present     *bool `json:"present"`
	SizeMatches *bool `json:"size_matches"`
}

type edgeModelStatus struct {
	ID            string          `json:"id"`
	Available     bool            `json:"available"`
	Active        bool            `json:"active"`
	ProcessState  string          `json:"process_state"`
	Reason        string          `json:"reason"`
	Artifact      edgeArtifact    `json:"artifact"`
	Capacity      edgeCapacity    `json:"capacity"`
	Runtime       edgeRuntime     `json:"runtime"`
	ContextTokens *int            `json:"context_tokens"`
	Profile       edgeProfile     `json:"profile"`
	Checkpoints   edgeCheckpoints `json:"checkpoints"`
}

type edgeRuntime struct {
	ID                string `json:"id"`
	Engine            string `json:"engine"`
	Variant           string `json:"variant"`
	Backend           string `json:"backend"`
	Commit            string `json:"commit"`
	CheckpointCapable bool   `json:"checkpoint_capable"`
}

type edgeProfile struct {
	Weights          string          `json:"weights"`
	CacheTypeK       string          `json:"cache_type_k"`
	CacheTypeV       string          `json:"cache_type_v"`
	MaxOutputTokens  *int            `json:"max_output_tokens"`
	NPredict         *int            `json:"n_predict"`
	ReasoningBudget  *int            `json:"reasoning_budget"`
	CompactThreshold *int            `json:"compact_threshold_tokens"`
	MoEOffload       *edgeMoEOffload `json:"moe_offload,omitempty"`
}

type edgeMoEOffload struct {
	CPULayers *int  `json:"cpu_layers,omitempty"`
	CPUAll    *bool `json:"cpu_all,omitempty"`
}

type edgeCheckpoints struct {
	Configured     bool `json:"configured"`
	Count          *int `json:"ctx_checkpoints"`
	RuntimeCapable bool `json:"runtime_capable"`
}

type edgeDeployment struct {
	Environment string `json:"environment"`
	Release     string `json:"release"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	SourceDirty bool   `json:"source_dirty"`
	Status      string `json:"status"`
	CreatedUTC  string `json:"created_utc"`
}

type edgeInference struct {
	Gate   edgeGate     `json:"gate"`
	Live   []edgeLive   `json:"live"`
	Recent []edgeRecord `json:"recent"`
	Totals edgeTotals   `json:"totals"`
}

type edgeLive struct {
	Model           string   `json:"model"`
	Route           string   `json:"route"`
	Stream          bool     `json:"stream"`
	ColdStart       bool     `json:"cold_start"`
	Phase           string   `json:"phase"`
	StartedAt       string   `json:"started_at"`
	ElapsedMS       int64    `json:"elapsed_ms"`
	TTFTMS          *int64   `json:"ttft_ms"`
	OutputTokens    int      `json:"output_tokens"`
	TokensPerSecond *float64 `json:"tokens_per_second"`
	ContextTokens   *int     `json:"context_tokens"`
	MaxOutputTokens *int     `json:"max_output_tokens"`
}

type edgeRecord struct {
	StartedAt       string             `json:"started_at"`
	Model           string             `json:"model"`
	Route           string             `json:"route"`
	Stream          bool               `json:"stream"`
	ColdStart       bool               `json:"cold_start"`
	Status          int                `json:"status"`
	Finish          string             `json:"finish"`
	PromptTokens    *int               `json:"prompt_tokens"`
	CachedTokens    *int               `json:"cached_tokens"`
	OutputTokens    *int               `json:"output_tokens"`
	OutputEstimated bool               `json:"output_estimated"`
	PromptPerSecond *float64           `json:"prompt_tokens_per_second"`
	DecodePerSecond *float64           `json:"decode_tokens_per_second"`
	TTFTMS          *int64             `json:"ttft_ms"`
	DurationMS      int64              `json:"duration_ms"`
	ContextTokens   *int               `json:"context_tokens"`
	Measurements    recordMeasurements `json:"measurements"`
}

type edgeTotals struct {
	Since                 string  `json:"since"`
	Requests              int64   `json:"requests"`
	Failed                int64   `json:"failed"`
	PromptTokens          int64   `json:"prompt_tokens"`
	CachedTokens          int64   `json:"cached_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	EstimatedOutputEvents int64   `json:"estimated_output_events"`
	TimedPromptTokens     int64   `json:"timed_prompt_tokens"`
	PromptMS              float64 `json:"prompt_ms"`
	TimedOutputTokens     int64   `json:"timed_output_tokens"`
	DecodeMS              float64 `json:"decode_ms"`
}

// edgeClient reads the control plane. Redirects are refused: the control URL
// is validated as loopback once, and a redirect would be a second, unvalidated
// destination.
type edgeClient struct {
	base   *url.URL
	client *http.Client
}

func newEdgeClient(base *url.URL) *edgeClient {
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableCompression:    true,
	}
	return &edgeClient{
		base: base,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *edgeClient) status(ctx context.Context) (*edgeStatus, error) {
	var status edgeStatus
	if err := c.get(ctx, edgeStatusPath, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *edgeClient) inference(ctx context.Context) (*edgeInference, error) {
	var inference edgeInference
	if err := c.get(ctx, edgeInferencePath, &inference); err != nil {
		return nil, err
	}
	return &inference, nil
}

func (c *edgeClient) get(ctx context.Context, path string, into any) error {
	target := c.base.JoinPath(path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && path == edgeInferencePath {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEdgeBody))
		return errTelemetryUnavailable
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEdgeBody))
		return fmt.Errorf("edge %s returned status %d", path, response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxEdgeBody))
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode edge %s: %w", path, err)
	}
	return nil
}
