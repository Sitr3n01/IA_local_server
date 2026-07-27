package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const modelControlPrefix = "/api/v1/models/"

var errNonEmptyControlBody = errors.New("model control requests must have an empty body")

func parseModelControlPath(escapedPath string) (modelID, operation string, matched bool, err error) {
	if !strings.HasPrefix(escapedPath, modelControlPrefix) {
		return "", "", false, nil
	}
	remainder := strings.TrimPrefix(escapedPath, modelControlPrefix)
	for _, candidate := range []string{"load", "unload", "switch"} {
		suffix := ":" + candidate
		if !strings.HasSuffix(remainder, suffix) {
			continue
		}
		escapedID := strings.TrimSuffix(remainder, suffix)
		if escapedID == "" {
			return "", "", true, errors.New("model ID is missing")
		}
		id, unescapeErr := url.PathUnescape(escapedID)
		if unescapeErr != nil || id == "" {
			return "", "", true, errors.New("model ID is invalid")
		}
		return id, candidate, true, nil
	}
	return "", "", true, errors.New("model operation is unknown")
}

func (s *Server) handleModelControl(w http.ResponseWriter, r *http.Request, modelID, operation string) {
	if err := ensureEmptyBody(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "model control requests require an empty body", "")
		return
	}
	model, allowed := s.modelByID(modelID)
	if !allowed {
		s.writeError(w, http.StatusNotFound, "model_not_found", "requested model is not available", "model")
		return
	}

	endControl, available := s.gate.beginControl()
	if !available {
		s.writeError(w, http.StatusConflict, "inference_busy", "model control is unavailable while inference is active or queued", "")
		return
	}
	defer endControl()

	activeModel := ""
	if operation == "load" || operation == "switch" {
		capacity, running := s.capacityFor(r.Context(), model)
		if !capacity.Available {
			s.writeError(w, http.StatusServiceUnavailable, "insufficient_capacity", "configured model cannot be admitted with the current commit headroom", "model")
			return
		}
		activeModel = s.activeModel(running)
		if operation == "load" && activeModel != "" && activeModel != modelID {
			s.writeError(w, http.StatusConflict, "model_conflict", "another model is already loaded; use switch", "model")
			return
		}
	}

	// Once an authenticated operation is admitted, finish it independently of
	// the panel connection. In particular, a switch must not stop after unload
	// merely because the client window closed or its HTTP timeout elapsed.
	operationCtx, cancelOperation := context.WithTimeout(context.Background(), s.cfg.HeaderTimeout)
	defer cancelOperation()

	var err error
	switch operation {
	case "load":
		err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
	case "unload":
		err = s.routerOperation(operationCtx, http.MethodPost, "/api/models/unload/"+url.PathEscape(modelID))
	case "switch":
		if activeModel == modelID {
			err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
		} else if err = s.routerOperation(operationCtx, http.MethodPost, "/api/models/unload"); err == nil {
			err = s.routerOperation(operationCtx, http.MethodGet, "/upstream/"+url.PathEscape(modelID)+"/health")
		}
	default:
		s.writeError(w, http.StatusNotFound, "unknown_path", "route not found", "")
		return
	}
	if err != nil {
		s.metrics.upstreamFailures.Add(1)
		s.writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "local model control operation failed", "")
		return
	}

	running, _ := s.runningModels(operationCtx)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"operation":    operation,
		"model":        modelID,
		"status":       "completed",
		"active_model": s.activeModel(running),
	})
}

func ensureEmptyBody(r *http.Request) error {
	if r.ContentLength > 0 {
		return errNonEmptyControlBody
	}
	if r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return err
	}
	if len(data) != 0 {
		return errNonEmptyControlBody
	}
	return nil
}

func (s *Server) routerOperation(ctx context.Context, method, path string) error {
	request, err := s.newRouterRequest(ctx, method, path)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("router operation returned status %d", response.StatusCode)
	}
	return nil
}

func (s *Server) newRouterRequest(ctx context.Context, method, path string) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return nil, errors.New("invalid router path")
	}
	target := strings.TrimSuffix(s.cfg.UpstreamURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cia-edge/"+s.cfg.Version)
	if s.cfg.RouterToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.cfg.RouterToken)
	}
	return request, nil
}
