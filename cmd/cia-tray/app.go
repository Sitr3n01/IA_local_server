package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/claudedesktop"
	"github.com/sitr3n/local-ai-provider/internal/credential"
	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
	"github.com/sitr3n/local-ai-provider/internal/panel"
	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

// gatewayProbeInterval spaces out the Claude gateway check. The flyout
// refreshes every two seconds while open, and a discovery request that often
// would crowd the edge's recent activity for nothing.
const gatewayProbeInterval = time.Minute

type appController struct {
	config         panel.Config
	catalog        *panel.Catalog
	selection      *panel.SelectionStore
	statusClient   *mcpserver.ControlClient
	adminClient    *mcpadmin.Client
	launcher       *panel.Launcher
	server         serverControl
	claude         claudeDesktopService
	readCredential func(string) (string, error)
	probeGateway   func(context.Context, claudedesktop.Gateway) error
	claudeDetail   string
	now            func() time.Time

	mu            sync.RWMutex
	selected      string
	selectionNote string
	gatewayOK     bool
	gatewayNote   string
	gatewayAt     time.Time
}

// serverControl starts and stops the processes that make up one deployment
// and opens its browser monitor.
type serverControl interface {
	Start(context.Context) error
	Stop(context.Context) error
	OpenPanel(context.Context) error
}

// claudeDesktopService keeps tray actions testable as one end-to-end unit:
// mode selection and launch go through the same discovered Desktop identity.
type claudeDesktopService interface {
	CurrentMode() (claudedesktop.Mode, error)
	Apply(context.Context, claudedesktop.Mode, claudedesktop.Gateway) error
	Launch(context.Context) error
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
	selected, selectionNote, err := loadSelection(selection)
	if err != nil {
		return nil, err
	}
	launcher, err := panel.NewLauncher(config, catalog)
	if err != nil {
		return nil, err
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
	// The installation root is the parent of the installed manifest's config
	// directory, which the panel configuration already pins. Deriving it here
	// avoids adding a field to a generated configuration that rejects unknown
	// keys, and keeps the edge executable identity out of operator hands.
	installRoot := filepath.Dir(filepath.Dir(config.ManifestPath))
	adminPipe, adminPipeServer := mcpadmin.AdminPipeForInstallation(string(config.Environment), installRoot)
	adminClient, err := mcpadmin.NewClient(mcpadmin.Config{
		ControlURL:      config.ControlURL,
		Timeout:         config.OperationTimeout(),
		AdminPipe:       adminPipe,
		AdminPipeServer: adminPipeServer,
		TokenProvider:   mcpadmin.TokenProviderFunc(readAdmin),
	}, appVersion)
	if err != nil {
		return nil, err
	}
	server, err := newServerControl(string(config.Environment), installRoot)
	if err != nil {
		return nil, err
	}

	controller := &appController{
		config:         config,
		catalog:        catalog,
		selection:      selection,
		statusClient:   statusClient,
		adminClient:    adminClient,
		launcher:       launcher,
		server:         server,
		selected:       selected,
		selectionNote:  selectionNote,
		readCredential: credential.Read,
		probeGateway:   claudedesktop.ProbeGateway,
		now:            time.Now,
	}
	controller.claude, controller.claudeDetail = discoverClaudeDesktop(filepath.Join(filepath.Dir(config.SelectionPath), "claude-desktop"))
	return controller, nil
}

// loadSelection reads the saved model choice. A saved model that the
// deployment no longer serves, or an unreadable file, must not keep the tray
// from starting: it falls back to the public model in memory, leaves the file
// as it is, and says so. Only a catalog without a usable public model fails.
func loadSelection(store *panel.SelectionStore) (string, string, error) {
	selection, err := store.Load()
	if err == nil {
		return selection.Model, "", nil
	}
	fallback, fallbackErr := store.Fallback()
	if fallbackErr != nil {
		return "", "", fmt.Errorf("%w; %v", err, fallbackErr)
	}
	return fallback.Model, "O modelo salvo não está mais disponível; o modelo padrão foi selecionado.", nil
}

func (c *appController) Snapshot(ctx context.Context) (trayui.Snapshot, error) {
	c.mu.RLock()
	selected, selectionNote := c.selected, c.selectionNote
	c.mu.RUnlock()

	snapshot := trayui.Snapshot{
		Environment:   string(c.config.Environment),
		SelectedModel: selected,
		SelectionNote: selectionNote,
		UpdatedAt:     c.now().UTC(),
	}
	if c.claude != nil {
		snapshot.ClaudeAvailable = true
		if mode, modeErr := c.claude.CurrentMode(); modeErr == nil {
			snapshot.ClaudeMode = trayui.ClaudeMode(mode)
		}
	}
	snapshot.ClaudeDetail = c.claudeDetail
	for _, model := range c.catalog.AvailableModels() {
		snapshot.Models = append(snapshot.Models, trayui.Model{
			ID:          model.ID,
			DisplayName: model.DisplayName,
			Available:   true,
			Codex:       model.CanLaunchCodex(),
			OpenCode:    model.CanLaunchOpenCode(),
		})
	}

	status, statusErr := c.statusClient.Status(ctx)
	if statusErr == nil {
		snapshot.EdgeReachable = true
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
		if !status.Ready && status.Upstream.Reachable && !status.Capacity.Available {
			snapshot.ReadyNote = snapshot.CapacityNote
		}
		published := make(map[string]struct{}, len(status.Models))
		for _, model := range status.Models {
			published[model.ID] = struct{}{}
		}
		for index := range snapshot.Models {
			if _, ok := published[snapshot.Models[index].ID]; !ok {
				snapshot.Models[index].Available = false
				snapshot.Models[index].Codex = false
				snapshot.Models[index].OpenCode = false
			}
		}
		for _, item := range status.ModelStatuses {
			if item.ID == selected {
				snapshot.CapacityOK = item.Available
				snapshot.CapacityNote = capacityReason(item.Reason)
			}
		}
		if snapshot.ClaudeAvailable && status.Ready && status.Upstream.Reachable {
			ok, note := c.claudeGateway(ctx)
			snapshot.ClaudeGatewayOK = ok
			if note != "" {
				snapshot.ClaudeDetail = note
			}
		}
		return snapshot, nil
	}

	// Readiness is intentionally public and side-effect-free. It preserves a
	// useful offline/degraded indication even when the status route fails.
	if ready, readyErr := c.statusClient.Readiness(ctx); readyErr == nil {
		snapshot.EdgeReachable = true
		snapshot.ProviderReady = ready.Status == "ready"
		if ready.UpstreamReachable != nil {
			snapshot.UpstreamReady = *ready.UpstreamReachable
		}
	}
	return snapshot, statusErr
}

// claudeGateway reports whether Claude Desktop could use the local gateway,
// asking the gateway at most once per gatewayProbeInterval.
func (c *appController) claudeGateway(ctx context.Context) (bool, string) {
	c.mu.RLock()
	if !c.gatewayAt.IsZero() && c.now().Sub(c.gatewayAt) < gatewayProbeInterval {
		ok, note := c.gatewayOK, c.gatewayNote
		c.mu.RUnlock()
		return ok, note
	}
	c.mu.RUnlock()

	ok, note := true, ""
	if token, err := c.readCredential("claude-gateway"); err != nil {
		ok, note = false, "Gateway Claude indisponível: "+sanitizeClaudeDetail(err)
	} else if err := c.probeGateway(ctx, claudedesktop.Gateway{BaseURL: c.config.DataURL, APIKey: token}); err != nil {
		ok, note = false, "Gateway Claude indisponível: "+sanitizeClaudeDetail(err)
	}
	c.mu.Lock()
	c.gatewayOK, c.gatewayNote, c.gatewayAt = ok, note, c.now()
	c.mu.Unlock()
	return ok, note
}

func (c *appController) forgetGateway() {
	c.mu.Lock()
	c.gatewayAt = time.Time{}
	c.mu.Unlock()
}

func (c *appController) SetClaudeMode(ctx context.Context, mode trayui.ClaudeMode) error {
	if c.claude == nil {
		return errors.New("claude Desktop não está disponível para configuração")
	}
	defer c.forgetGateway()
	var gateway claudedesktop.Gateway
	switch mode {
	case trayui.ClaudeModeAnthropic:
		return c.claude.Apply(ctx, claudedesktop.ModeAnthropic, gateway)
	case trayui.ClaudeModeLocal:
		token, err := c.readCredential("claude-gateway")
		if err != nil {
			return fmt.Errorf("ler credencial exclusiva do gateway Claude: %w", err)
		}
		gateway = claudedesktop.Gateway{BaseURL: c.config.DataURL, APIKey: token}
		if err := c.probeGateway(ctx, gateway); err != nil {
			return fmt.Errorf("precheck do gateway Claude: %w", err)
		}
		return c.claude.Apply(ctx, claudedesktop.ModeLocal, gateway)
	default:
		return errors.New("modo Claude Desktop inválido")
	}
}

func (c *appController) LaunchClaudeDesktop(ctx context.Context) error {
	if c.claude == nil {
		return errors.New("o Claude Desktop não está disponível para abertura")
	}
	return c.claude.Launch(ctx)
}

func sanitizeClaudeDetail(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 180 {
		return text[:180]
	}
	return text
}

func (c *appController) SelectModel(_ context.Context, modelID string) error {
	selection, err := c.selection.Save(modelID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.selected = selection.Model
	c.selectionNote = ""
	c.mu.Unlock()
	return nil
}

func (c *appController) LoadSelected(ctx context.Context) error {
	_, err := c.adminClient.Load(ctx, c.selectedModel())
	return err
}

func (c *appController) SwitchSelected(ctx context.Context) error {
	_, err := c.adminClient.Switch(ctx, c.selectedModel())
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

func (c *appController) StartServer(ctx context.Context) error { return c.server.Start(ctx) }

func (c *appController) StopServer(ctx context.Context) error { return c.server.Stop(ctx) }

func (c *appController) OpenPanel(ctx context.Context) error { return c.server.OpenPanel(ctx) }

func (c *appController) selectedModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.selected
}

func capacityReason(reason string) string {
	switch reason {
	case "model_already_running":
		return "modelo já carregado"
	case "commit_headroom_available":
		return "reserva de memória disponível"
	case "insufficient_commit_headroom":
		return "reserva de memória insuficiente"
	case "insufficient_physical_memory":
		return "memória física insuficiente"
	case "insufficient_vram_budget":
		return "VRAM insuficiente"
	case "resource_profile_incomplete":
		return "perfil de recursos incompleto"
	case "canary_resource_measurement_pending":
		return "medição de recursos pendente no canário"
	case "resource_measurement_required":
		return "medição de recursos obrigatória"
	default:
		return reason
	}
}
