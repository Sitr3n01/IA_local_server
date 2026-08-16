package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const commitReserveGiB = 4.0

// vramReserveGiB is left to the Windows desktop compositor and to allocations
// llama.cpp makes outside its own accounting. Unlike commit headroom there is no
// cheap live probe here, so admission compares a measured per-model peak against
// the device budget declared by the runtime. provider.max_loaded_models is
// pinned to 1, which is what makes a static budget sound: exactly one model is
// ever resident on the device.
const vramReserveGiB = 1.0

// physicalReserveGiB is deliberately smaller than the commit reserve. The two
// axes overlap: every allocation counted against commit is also counted here
// once it is touched, so charging 4 GiB twice would refuse configurations that
// actually fit. What this reserve protects is different — it keeps the OS file
// cache alive and keeps a model's CPU-resident weights from being paged out,
// which would turn every token into an SSD read instead of a DDR5 read.
const physicalReserveGiB = 2.0

// memorySnapshot is the host memory state admission control reasons about.
// Commit and physical answer different questions: commit bounds what may be
// reserved, physical bounds what may stay resident. A model that offloads
// weights to system RAM can satisfy the first and still thrash on the second.
type memorySnapshot struct {
	CommitGiB   float64
	PhysicalGiB float64
}

type capacityStatus struct {
	Admission           string   `json:"admission"`
	Model               string   `json:"model"`
	ModelRunning        bool     `json:"model_running"`
	CommitHeadroomGiB   *float64 `json:"commit_headroom_gib"`
	RequiredCommitGiB   *float64 `json:"required_commit_gib"`
	ReserveCommitGiB    float64  `json:"reserve_commit_gib"`
	PhysicalHeadroomGiB *float64 `json:"physical_headroom_gib"`
	RequiredPhysicalGiB *float64 `json:"required_physical_gib"`
	ReservePhysicalGiB  float64  `json:"reserve_physical_gib"`
	RequiredVRAMGiB     *float64 `json:"required_vram_gib"`
	DeviceVRAMGiB       *float64 `json:"device_vram_gib"`
	ReserveVRAMGiB      float64  `json:"reserve_vram_gib"`
	// MissingProfileFields names the measurements this execution mode requires
	// and does not have. Empty for a complete profile.
	MissingProfileFields []string `json:"missing_profile_fields,omitempty"`
	Measured             bool     `json:"measured"`
	Available            bool     `json:"available"`
	Reason               string   `json:"reason"`
}

type runningResponse struct {
	Running []struct {
		Model string `json:"model"`
		State string `json:"state"`
	} `json:"running"`
}

func (s *Server) capacityFor(ctx context.Context, model Model) (capacityStatus, map[string]string) {
	running, runningErr := s.runningModels(ctx)
	memory, metricErr := s.memoryStatus()
	return capacityFrom(model, running, runningErr, memory, metricErr), running
}

func capacityFrom(model Model, running map[string]string, runningErr error, memory memorySnapshot, metricErr error) capacityStatus {
	_, isRunning := running[model.ID]

	result := capacityStatus{
		Admission:          "commit-headroom",
		Model:              model.ID,
		ModelRunning:       isRunning,
		ReserveCommitGiB:   commitReserveGiB,
		ReservePhysicalGiB: physicalReserveGiB,
		ReserveVRAMGiB:     vramReserveGiB,
	}
	if metricErr == nil {
		result.CommitHeadroomGiB = floatPointer(roundGiB(memory.CommitGiB))
		result.PhysicalHeadroomGiB = floatPointer(roundGiB(memory.PhysicalGiB))
	}
	requiredCommit, haveCommit := requiredCommitGiB(model)
	if haveCommit {
		result.RequiredCommitGiB = floatPointer(roundGiB(requiredCommit))
	}
	incomplete := missingProfileFields(model)
	result.MissingProfileFields = incomplete
	if model.PeakRAMGiB != nil {
		result.RequiredPhysicalGiB = floatPointer(roundGiB(*model.PeakRAMGiB + physicalReserveGiB))
	}
	if model.PeakVRAMGiB != nil {
		result.RequiredVRAMGiB = floatPointer(roundGiB(*model.PeakVRAMGiB + vramReserveGiB))
	}
	if model.DeviceVRAMGiB != nil {
		result.DeviceVRAMGiB = floatPointer(roundGiB(*model.DeviceVRAMGiB))
	}
	// Measured means "every resource this execution mode is gated on has a
	// number", not "commit happens to be known". A tensor-offloading model whose
	// peak_ram_gib is null was previously reported as measured, which made an
	// unchecked limit look verified.
	result.Measured = runningErr == nil && metricErr == nil && haveCommit && len(incomplete) == 0

	switch {
	case isRunning:
		result.Available = true
		result.Reason = "model_already_running"
	case len(incomplete) > 0:
		// Ahead of every headroom test: an incomplete profile means at least one
		// limit is not being enforced at all, so passing the others proves
		// nothing. Previously this sat after the commit branch, and a model with
		// commit measured but RAM null was admitted on commit alone.
		result.Available = false
		result.Reason = "resource_profile_incomplete"
	case exceedsVRAMBudget(model):
		// A device budget overrun is a property of the manifest, not of live
		// load, so it is checked before headroom and reported distinctly.
		result.Available = false
		result.Reason = "insufficient_vram_budget"
	case exceedsPhysicalMemory(model, memory, metricErr):
		// Checked before commit: a model can pass the commit test and still be
		// unable to keep its CPU-resident weights in RAM, which is a much worse
		// outcome than a 503 because it degrades silently to disk speed.
		result.Available = false
		result.Reason = "insufficient_physical_memory"
	case haveCommit && metricErr == nil:
		result.Available = memory.CommitGiB >= requiredCommit
		if result.Available {
			result.Reason = "commit_headroom_available"
		} else {
			result.Reason = "insufficient_commit_headroom"
		}
	case isCanaryCandidate(model):
		result.Available = true
		result.Reason = "canary_resource_measurement_pending"
	default:
		result.Available = false
		result.Reason = "resource_measurement_required"
	}
	return result
}

// requiredCommitGiB is the measured peak plus the reserve, plus any prompt cache
// the model asks llama-server to hold in host RAM. --cache-ram is charged
// against the Windows commit limit exactly like the process working set, so
// leaving it out understates the requirement by its full size.
//
// The cache is added rather than expected to appear in the measurement because
// it is a ceiling that fills over the life of a session, not an allocation made
// at load. This makes the measurement discipline load-bearing:
// resources.peak_commit_gib must be captured with a cold prompt cache, or the
// same gibibytes are charged twice. See docs/MODEL_PROMOTION.md.
func requiredCommitGiB(model Model) (float64, bool) {
	if model.PeakCommitGiB == nil {
		return 0, false
	}
	required := *model.PeakCommitGiB + commitReserveGiB
	if model.CacheRAMMiB != nil {
		required += float64(*model.CacheRAMMiB) / 1024
	}
	return required, true
}

// exceedsPhysicalMemory reports whether the model's resident footprint cannot
// stay in RAM. It is deliberately inert unless resources.peak_ram_gib has been
// measured, so the existing small candidates keep their current verdict.
func exceedsPhysicalMemory(model Model, memory memorySnapshot, metricErr error) bool {
	if metricErr != nil || model.PeakRAMGiB == nil {
		return false
	}
	return *model.PeakRAMGiB+physicalReserveGiB > memory.PhysicalGiB
}

// missingProfileFields lists the measurements this model's execution mode
// requires but does not have. The required set is a property of *how the model
// runs*, not of its size: a model that keeps weights in system RAM is admitted
// against three different limits, and a partial profile silently disables
// whichever check its missing field belongs to.
//
// nil is never read as zero. An absent measurement means "unknown", which is
// stricter than any number, not more permissive.
func missingProfileFields(model Model) []string {
	required := []string{"resources.peak_commit_gib"}
	switch {
	case model.OffloadsTensors:
		// Weights deliberately outside VRAM: every limit is load-bearing, and
		// the device budget is what the VRAM peak is compared against.
		required = append(required,
			"resources.peak_vram_gib",
			"resources.peak_ram_gib",
			"runtimes[].device.vram_mib")
	case model.CacheRAMMiB != nil && *model.CacheRAMMiB > 0:
		// A host-RAM prompt cache is charged to commit and must stay resident,
		// but it does not by itself change the VRAM footprint.
		required = append(required, "resources.peak_ram_gib")
	default:
		return nil
	}

	present := map[string]bool{
		"resources.peak_commit_gib":  model.PeakCommitGiB != nil,
		"resources.peak_vram_gib":    model.PeakVRAMGiB != nil,
		"resources.peak_ram_gib":     model.PeakRAMGiB != nil,
		"runtimes[].device.vram_mib": model.DeviceVRAMGiB != nil,
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if !present[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

// capacityMessage keeps the operator-facing text aligned with capacity.reason.
// A single "commit headroom" sentence for every refusal made VRAM, physical
// memory, and an incomplete profile indistinguishable from a full pagefile.
func capacityMessage(reason string) string {
	switch reason {
	case "insufficient_vram_budget":
		return "configured model exceeds the runtime's declared VRAM budget"
	case "insufficient_physical_memory":
		return "configured model cannot stay resident in the available physical memory"
	case "insufficient_commit_headroom":
		return "configured model cannot be admitted with the current commit headroom"
	case "resource_profile_incomplete":
		return "configured model uses host memory and its resource profile is incomplete"
	case "resource_measurement_required":
		return "configured model has no measured resource profile"
	default:
		return "configured model cannot be admitted"
	}
}

func exceedsVRAMBudget(model Model) bool {
	if model.PeakVRAMGiB == nil || model.DeviceVRAMGiB == nil {
		return false
	}
	return *model.PeakVRAMGiB+vramReserveGiB > *model.DeviceVRAMGiB
}

func (s *Server) requireCapacity(w http.ResponseWriter, ctx context.Context, model Model) bool {
	capacity, _ := s.capacityFor(ctx, model)
	if capacity.Available {
		return true
	}
	s.writeError(w, http.StatusServiceUnavailable, "insufficient_capacity", capacityMessage(capacity.Reason), "model")
	return false
}

func (s *Server) runningModels(ctx context.Context) (map[string]string, error) {
	request, err := s.newRouterRequest(ctx, http.MethodGet, "/running")
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("router running endpoint returned status %d", response.StatusCode)
	}

	var payload runningResponse
	if err := decodeLimitedJSON(response.Body, 1<<20, &payload); err != nil {
		return nil, err
	}
	running := make(map[string]string, len(payload.Running))
	for _, item := range payload.Running {
		if _, allowed := s.allowed[item.Model]; !allowed {
			continue
		}
		running[item.Model] = strings.TrimSpace(item.State)
	}
	return running, nil
}

func (s *Server) activeModel(running map[string]string) string {
	models := make([]string, 0, len(running))
	for model := range running {
		if _, allowed := s.allowed[model]; allowed {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

func (s *Server) modelByID(id string) (Model, bool) {
	for _, model := range s.cfg.Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

func isCanaryCandidate(model Model) bool {
	if !strings.EqualFold(model.State, "candidate") {
		return false
	}
	return containsDeployment(model.Deployments, "canary")
}

func floatPointer(value float64) *float64 { return &value }

func roundGiB(value float64) float64 {
	if value < 0 {
		return 0
	}
	return float64(int64(value*100+0.5)) / 100
}

func decodeLimitedJSON(reader io.Reader, limit int64, destination any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("router response exceeds the configured limit")
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return fmt.Errorf("decode router response: %w", err)
	}
	return nil
}

var errCapacityUnavailable = errors.New("commit headroom is unavailable on this platform")
