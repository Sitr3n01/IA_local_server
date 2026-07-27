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

type capacityStatus struct {
	Admission         string   `json:"admission"`
	Model             string   `json:"model"`
	ModelRunning      bool     `json:"model_running"`
	CommitHeadroomGiB *float64 `json:"commit_headroom_gib"`
	RequiredCommitGiB *float64 `json:"required_commit_gib"`
	ReserveCommitGiB  float64  `json:"reserve_commit_gib"`
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
	}
	if metricErr == nil {
		result.CommitHeadroomGiB = floatPointer(roundGiB(headroom))
	}
	if model.PeakCommitGiB != nil {
		required := *model.PeakCommitGiB + commitReserveGiB
		result.RequiredCommitGiB = floatPointer(roundGiB(required))
	}
	result.Measured = runningErr == nil && metricErr == nil && model.PeakCommitGiB != nil

	switch {
	case isRunning:
		result.Available = true
		result.Reason = "model_already_running"
	case model.PeakCommitGiB != nil && metricErr == nil:
		result.Available = headroom >= *model.PeakCommitGiB+commitReserveGiB
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
