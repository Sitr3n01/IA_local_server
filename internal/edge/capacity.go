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

type capacityStatus struct {
	Admission         string   `json:"admission"`
	Model             string   `json:"model"`
	ModelRunning      bool     `json:"model_running"`
	CommitHeadroomGiB *float64 `json:"commit_headroom_gib"`
	RequiredCommitGiB *float64 `json:"required_commit_gib"`
	ReserveCommitGiB  float64  `json:"reserve_commit_gib"`
	RequiredVRAMGiB   *float64 `json:"required_vram_gib"`
	DeviceVRAMGiB     *float64 `json:"device_vram_gib"`
	ReserveVRAMGiB    float64  `json:"reserve_vram_gib"`
	Measured          bool     `json:"measured"`
	Available         bool     `json:"available"`
	Reason            string   `json:"reason"`
}

type runningResponse struct {
	Running []struct {
		Model string `json:"model"`
		State string `json:"state"`
	} `json:"running"`
}

func (s *Server) capacityFor(ctx context.Context, model Model) (capacityStatus, map[string]string) {
	running, runningErr := s.runningModels(ctx)
	headroom, metricErr := s.commitHeadroom()
	return capacityFrom(model, running, runningErr, headroom, metricErr), running
}

func capacityFrom(model Model, running map[string]string, runningErr error, headroom float64, metricErr error) capacityStatus {
	_, isRunning := running[model.ID]

	result := capacityStatus{
		Admission:        "commit-headroom",
		Model:            model.ID,
		ModelRunning:     isRunning,
		ReserveCommitGiB: commitReserveGiB,
		ReserveVRAMGiB:   vramReserveGiB,
	}
	if metricErr == nil {
		result.CommitHeadroomGiB = floatPointer(roundGiB(headroom))
	}
	requiredCommit, haveCommit := requiredCommitGiB(model)
	if haveCommit {
		result.RequiredCommitGiB = floatPointer(roundGiB(requiredCommit))
	}
	if model.PeakVRAMGiB != nil {
		result.RequiredVRAMGiB = floatPointer(roundGiB(*model.PeakVRAMGiB + vramReserveGiB))
	}
	if model.DeviceVRAMGiB != nil {
		result.DeviceVRAMGiB = floatPointer(roundGiB(*model.DeviceVRAMGiB))
	}
	result.Measured = runningErr == nil && metricErr == nil && haveCommit

	switch {
	case isRunning:
		result.Available = true
		result.Reason = "model_already_running"
	case exceedsVRAMBudget(model):
		// A device budget overrun is a property of the manifest, not of live
		// load, so it is checked before headroom and reported distinctly.
		result.Available = false
		result.Reason = "insufficient_vram_budget"
	case haveCommit && metricErr == nil:
		result.Available = headroom >= requiredCommit
		if result.Available {
			result.Reason = "commit_headroom_available"
		} else {
			result.Reason = "insufficient_commit_headroom"
		}
	case requiresMeasuredProfile(model):
		// Partial weight offload and a host-RAM prompt cache both consume commit
		// that the pending-measurement escape hatch cannot see. Admitting them
		// unmeasured would trade a 503 for an out-of-memory stall.
		result.Available = false
		result.Reason = "resource_measurement_required_for_host_memory"
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

func exceedsVRAMBudget(model Model) bool {
	if model.PeakVRAMGiB == nil || model.DeviceVRAMGiB == nil {
		return false
	}
	return *model.PeakVRAMGiB+vramReserveGiB > *model.DeviceVRAMGiB
}

func requiresMeasuredProfile(model Model) bool {
	if model.OffloadsTensors {
		return true
	}
	return model.CacheRAMMiB != nil && *model.CacheRAMMiB > 0
}

func (s *Server) requireCapacity(w http.ResponseWriter, ctx context.Context, model Model) bool {
	capacity, _ := s.capacityFor(ctx, model)
	if capacity.Available {
		return true
	}
	s.writeError(w, http.StatusServiceUnavailable, "insufficient_capacity", "configured model cannot be admitted with the current commit headroom", "model")
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
