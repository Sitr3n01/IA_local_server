package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/credential"
	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
	"github.com/sitr3n/local-ai-provider/internal/panel"
	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

type appController struct {
	config          panel.Config
	catalog         *panel.Catalog
	selection       *panel.SelectionStore
	statusClient    *mcpserver.ControlClient
	adminClient     *mcpadmin.Client
	launcher        *panel.Launcher
	rootStore       *panel.ModelRootStore
	validationStore *panel.ValidationStore
	hashCache       *panel.HashCache

	mu       sync.RWMutex
	selected string
}

func newAppController(config panel.Config, appVersion string) (*appController, error) {
	catalog, err := panel.LoadCatalog(config.ManifestPath, config.Environment)
	if err != nil {
		return nil, err
	}
	selection, err := panel.NewSelectionStore(config.SelectionPath, catalog)
	if err != nil {
		return nil, err
	}
	selected, err := selection.Load()
	if err != nil {
		return nil, err
	}
	launcher, err := panel.NewLauncher(config, catalog)
	if err != nil {
		return nil, err
	}
	rootStore, err := panel.NewModelRootStore(config.ModelRootsPath, `C:\IA\models`)
	if err != nil {
		return nil, err
	}
	validationStore, err := panel.NewValidationStore(config.ValidationPath)
	if err != nil {
		return nil, err
	}
	hashCache, err := panel.NewHashCache(filepath.Join(filepath.Dir(config.ValidationPath), "model-hashes."+string(config.Environment)+".json"))
	if err != nil {
		return nil, err
	}
	for name, path := range map[string]string{
		"Codex": config.Launchers.Codex, "OpenCode": config.Launchers.OpenCode,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			if statErr == nil {
				statErr = errors.New("path is a directory")
			}
			return nil, fmt.Errorf("%s launcher is unavailable: %w", name, statErr)
		}
	}

	readAdmin := func(context.Context) (string, error) {
		return credential.Read("admin")
	}
	statusClient, err := mcpserver.NewControlClient(mcpserver.Config{
		ControlURL: config.ControlURL,
		Timeout:    4 * time.Second,
	}, appVersion)
	if err != nil {
		return nil, err
	}
	adminClient, err := mcpadmin.NewClient(mcpadmin.Config{
		ControlURL:    config.ControlURL,
		Timeout:       config.OperationTimeout(),
		TokenProvider: mcpadmin.TokenProviderFunc(readAdmin),
	}, appVersion)
	if err != nil {
		return nil, err
	}

	return &appController{
		config:          config,
		catalog:         catalog,
		selection:       selection,
		statusClient:    statusClient,
		adminClient:     adminClient,
		launcher:        launcher,
		rootStore:       rootStore,
		validationStore: validationStore,
		hashCache:       hashCache,
		selected:        selected.Model,
	}, nil
}

func (c *appController) Snapshot(ctx context.Context) (trayui.Snapshot, error) {
	c.mu.RLock()
	selected := c.selected
	c.mu.RUnlock()

	snapshot := trayui.Snapshot{
		Environment:   string(c.config.Environment),
		SelectedModel: selected,
		Models:        make([]trayui.Model, 0, len(c.catalog.AllModels())),
		UpdatedAt:     time.Now().UTC(),
	}
	registeredPaths := make(map[string]struct{}, len(c.catalog.AllModels()))
	validations, _ := c.validationStore.Load()
	for _, model := range c.catalog.AllModels() {
		validation := validations[model.ID]
		registeredPaths[strings.ToLower(filepath.Clean(model.ArtifactPath))] = struct{}{}
		snapshot.Models = append(snapshot.Models, trayui.Model{
			ID:             model.ID,
			DisplayName:    model.DisplayName,
			State:          model.State,
			Available:      model.Available,
			Reason:         unavailableReason(model),
			Codex:          model.CanLaunchCodex(),
			OpenCode:       model.CanLaunchOpenCode(),
			ArtifactPath:   model.ArtifactPath,
			ArtifactBytes:  model.ArtifactBytes,
			ArtifactSHA256: model.ArtifactSHA256,
			Runtime:        model.Runtime,
			ContextTokens:  model.ContextTokens,
			GPULayers:      model.GPULayers,
			Quantization:   model.CacheTypeK + "/" + model.CacheTypeV,
			Capabilities:   capabilitySummary(model),
			Validation:     validation.Status,
		})
	}
	if roots, rootsErr := c.rootStore.Load(); rootsErr == nil {
		snapshot.ModelRoots = roots
	}
	if discovered, discoveryErr := c.rootStore.Scan(); discoveryErr == nil {
		for _, item := range discovered {
			if _, registered := registeredPaths[strings.ToLower(filepath.Clean(item.Path))]; registered {
				continue
			}
			id := discoveredModelID(item.Path)
			validation := validations[id]
			snapshot.Models = append(snapshot.Models, trayui.Model{
				ID: id, DisplayName: item.Name,
				State: "detected", Reason: "detectado — aguardando validação",
				ArtifactPath: item.Path, ArtifactBytes: item.Bytes, ArtifactSHA256: validation.SHA256,
				Discovered: true, Validation: validation.Status,
			})
		}
	}

	status, statusErr := c.statusClient.Status(ctx)
	if statusErr == nil {
		snapshot.StatusAvailable = true
		snapshot.ProviderReady = status.Ready
		snapshot.UpstreamReady = status.Upstream.Reachable
		snapshot.ActiveModel = strings.TrimSpace(status.ActiveModel)
		snapshot.Active = status.Gate.Active
		snapshot.Queued = status.Gate.Queued
		snapshot.MaxActive = status.Gate.MaxActive
		snapshot.MaxQueue = status.Gate.MaxQueue
		snapshot.CapacityOK = status.Capacity.Available
		snapshot.CapacityNote = capacityReason(status.Capacity.Reason)
		published := make(map[string]struct{}, len(status.Models))
		for _, model := range status.Models {
			published[model.ID] = struct{}{}
		}
		for index := range snapshot.Models {
			if snapshot.Models[index].Available {
				if _, ok := published[snapshot.Models[index].ID]; !ok {
					snapshot.Models[index].Available = false
					snapshot.Models[index].Reason = "não publicado pelo edge"
					snapshot.Models[index].Codex = false
					snapshot.Models[index].OpenCode = false
				}
			}
		}
		statusByID := make(map[string]mcpserver.ModelStatus, len(status.ModelStatuses))
		for _, item := range status.ModelStatuses {
			statusByID[item.ID] = item
		}
		if selectedStatus, ok := statusByID[selected]; ok {
			snapshot.CapacityOK = selectedStatus.Available
			snapshot.CapacityNote = capacityReason(selectedStatus.Reason)
		}
		for _, event := range status.RecentEvents {
			snapshot.RecentEvents = append(snapshot.RecentEvents, trayui.Event{Time: event.Time, Method: event.Method, Path: event.Path, Status: event.Status, DurationMS: event.DurationMS})
		}
		return snapshot, nil
	}

	// Readiness is intentionally public and side-effect-free. It preserves a
	// useful offline/degraded indication even when the administrative credential
	// is missing, rotated, or temporarily unavailable.
	if ready, readyErr := c.statusClient.Readiness(ctx); readyErr == nil {
		snapshot.ProviderReady = ready.Status == "ready"
		if ready.UpstreamReachable != nil {
			snapshot.UpstreamReady = *ready.UpstreamReachable
		}
	}
	return snapshot, statusErr
}

func (c *appController) SelectModel(_ context.Context, modelID string) error {
	selection, err := c.selection.Save(modelID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.selected = selection.Model
	c.mu.Unlock()
	return nil
}

func (c *appController) LoadSelected(ctx context.Context) error {
	model := c.selectedModel()
	_, err := c.adminClient.Load(ctx, model)
	return err
}

func (c *appController) SwitchSelected(ctx context.Context) error {
	model := c.selectedModel()
	_, err := c.adminClient.Switch(ctx, model)
	return err
}

func (c *appController) UnloadActive(ctx context.Context) error {
	status, err := c.statusClient.Status(ctx)
	if err != nil {
		return err
	}
	model := strings.TrimSpace(status.ActiveModel)
	if model == "" {
		return errors.New("nenhum modelo está carregado")
	}
	_, err = c.adminClient.Unload(ctx, model)
	return err
}

func (c *appController) Launch(_ context.Context, client trayui.Client, modelID string) error {
	var target panel.Client
	switch client {
	case trayui.ClientCodex:
		target = panel.ClientCodex
	case trayui.ClientOpenCode:
		target = panel.ClientOpenCode
	default:
		return fmt.Errorf("cliente não suportado: %s", client)
	}
	return c.launcher.Launch(target, modelID)
}

func (c *appController) AddModelRoot(_ context.Context, path string) error {
	_, err := c.rootStore.Add(path)
	return err
}

func (c *appController) RemoveModelRoot(_ context.Context, path string) error {
	_, err := c.rootStore.Remove(path)
	return err
}

func (c *appController) ValidateModel(ctx context.Context, modelID string) error {
	if model, ok := c.catalog.Model(modelID); ok {
		_ = c.validationStore.RecordArtifact(modelID, "validando", "", model.ArtifactPath, "")
		record, _, err := c.hashCache.HashFile(model.ArtifactPath)
		if err != nil {
			_ = c.validationStore.RecordArtifact(modelID, "falhou", err.Error(), model.ArtifactPath, "")
			return err
		}
		if !strings.EqualFold(record.SHA256, model.ArtifactSHA256) {
			err := errors.New("SHA-256 do GGUF diverge do manifesto")
			_ = c.validationStore.RecordArtifact(modelID, "falhou", err.Error(), record.Path, record.SHA256)
			return err
		}
		if err := c.validateSelected(ctx, modelID); err != nil {
			_ = c.validationStore.RecordArtifact(modelID, "falhou", err.Error(), record.Path, record.SHA256)
			return err
		}
		_ = c.validationStore.RecordArtifact(modelID, "validado", "carga e geração concluídas", record.Path, record.SHA256)
		return nil
	}

	discovered, err := c.rootStore.Scan()
	if err != nil {
		return err
	}
	for _, item := range discovered {
		if discoveredModelID(item.Path) != modelID {
			continue
		}
		_ = c.validationStore.RecordArtifact(modelID, "validando", "", item.Path, "")
		record, _, err := c.hashCache.HashFile(item.Path)
		if err != nil {
			_ = c.validationStore.RecordArtifact(modelID, "falhou", err.Error(), item.Path, "")
			return err
		}
		version, err := inspectGGUFHeader(record.Path)
		if err != nil {
			_ = c.validationStore.RecordArtifact(modelID, "falhou", err.Error(), record.Path, record.SHA256)
			return err
		}
		message := fmt.Sprintf("hash e cabeçalho GGUF v%d verificados; aguardando perfil de execução no manifesto", version)
		return c.validationStore.RecordArtifact(modelID, "inspecionado", message, record.Path, record.SHA256)
	}
	return errors.New("modelo detectado não foi encontrado nas pastas aprovadas")
}

func (c *appController) validateSelected(ctx context.Context, model string) error {
	before, err := c.statusClient.Status(ctx)
	if err != nil {
		return err
	}
	restore := func() {}
	if before.ActiveModel == "" {
		if _, err = c.adminClient.Load(ctx, model); err != nil {
			return err
		}
		restore = func() { _, _ = c.adminClient.Unload(context.Background(), model) }
	} else if before.ActiveModel != model {
		previous := before.ActiveModel
		if _, err = c.adminClient.Switch(ctx, model); err != nil {
			return err
		}
		restore = func() { _, _ = c.adminClient.Switch(context.Background(), previous) }
	}
	defer restore()
	token, err := credential.Read("inference")
	if err != nil {
		return fmt.Errorf("read inference credential: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"model": model, "messages": []map[string]string{{"role": "user", "content": "Responda somente CIA_MODEL_OK"}}, "max_tokens": 32,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.DataURL, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: c.config.OperationTimeout()}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("model validation request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("model validation returned %s", response.Status)
	}
	return nil
}

func (c *appController) selectedModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.selected
}

func discoveredModelID(path string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	return fmt.Sprintf("detected-%x", digest[:6])
}

func inspectGGUFHeader(path string) (uint32, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	header := make([]byte, 8)
	if _, err := file.Read(header); err != nil {
		return 0, fmt.Errorf("ler cabeçalho GGUF: %w", err)
	}
	if string(header[:4]) != "GGUF" {
		return 0, errors.New("arquivo não possui cabeçalho GGUF")
	}
	version := binary.LittleEndian.Uint32(header[4:8])
	if version < 2 || version > 3 {
		return 0, fmt.Errorf("versão GGUF não suportada: %d", version)
	}
	return version, nil
}

func unavailableReason(model panel.Model) string {
	if model.Available {
		return ""
	}
	if model.State == "candidate" && !model.Capabilities.FunctionCalling {
		return "candidato sem function calling"
	}
	switch model.State {
	case "disabled":
		return "desabilitado"
	case "retired":
		return "retirado"
	default:
		return "não implantado neste ambiente"
	}
}

func capacityReason(reason string) string {
	switch reason {
	case "model_already_running":
		return "modelo já carregado"
	case "commit_headroom_available":
		return "reserva de memória disponível"
	case "insufficient_commit_headroom":
		return "reserva de memória insuficiente"
	case "canary_resource_measurement_pending":
		return "medição de recursos pendente no canário"
	case "resource_measurement_required":
		return "medição de recursos obrigatória"
	default:
		return reason
	}
}

func capabilitySummary(model panel.Model) string {
	parts := make([]string, 0, 4)
	if model.Capabilities.Responses {
		parts = append(parts, "Responses")
	}
	if model.Capabilities.ChatCompletions {
		parts = append(parts, "Chat")
	}
	if model.Capabilities.Streaming {
		parts = append(parts, "streaming")
	}
	if model.Capabilities.FunctionCalling {
		parts = append(parts, "tools")
	}
	if len(parts) == 0 {
		return "não validado"
	}
	return strings.Join(parts, ", ")
}
