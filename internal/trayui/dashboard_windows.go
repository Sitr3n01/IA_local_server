//go:build windows

package trayui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	esMultiline        = 0x0004
	esAutoVScroll      = 0x0040
	esReadOnly         = 0x0800
	lbsNotify          = 0x0001
	bsPushButton       = 0x00000000
	ssLeft             = 0x00000000

	swHide = 0
	swShow = 5

	wmSetFont      = 0x0030
	lbAddString    = 0x0180
	lbResetContent = 0x0184
	lbSetCurSel    = 0x0186
	lbGetCurSel    = 0x0188
	lbErr          = ^uintptr(0)
	lbnSelChange   = 1
	enChange       = 0x0300
	emSetCueBanner = 0x1501

	idStatus     = 2001
	idSearch     = 2002
	idModels     = 2003
	idDetails    = 2004
	idEvents     = 2005
	idRoots      = 2006
	idSelect     = 2010
	idLoad       = 2011
	idUnload     = 2012
	idValidate   = 2013
	idCodex      = 2014
	idOpenCode   = 2015
	idRefresh    = 2016
	idAddRoot    = 2017
	idRemoveRoot = 2018

	defaultGUIFont      = 17
	bifReturnOnlyFSDirs = 0x0001
	bifNewDialogStyle   = 0x0040
)

type rect struct{ Left, Top, Right, Bottom int32 }

type browseInfo struct {
	Owner       windows.Handle
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

var (
	procMoveWindow           = user32.NewProc("MoveWindow")
	procSetWindowTextW       = user32.NewProc("SetWindowTextW")
	procSendMessageW         = user32.NewProc("SendMessageW")
	procEnableWindow         = user32.NewProc("EnableWindow")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetClientRect        = user32.NewProc("GetClientRect")
	procGetStockObject       = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetStockObject")
	procSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree        = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
)

func (a *app) createDashboardControls() error {
	a.controls = make(map[uint32]windows.Handle)
	controls := []struct {
		id          uint32
		class, text string
		style       uintptr
	}{
		{idStatus, "STATIC", "CIA Local AI", wsChild | wsVisible | ssLeft},
		{idSearch, "EDIT", "", wsChild | wsVisible | wsTabStop | wsBorder},
		{idModels, "LISTBOX", "", wsChild | wsVisible | wsTabStop | wsBorder | wsVScroll | lbsNotify},
		{idDetails, "EDIT", "", wsChild | wsVisible | wsBorder | wsVScroll | esMultiline | esAutoVScroll | esReadOnly},
		{idEvents, "EDIT", "", wsChild | wsVisible | wsBorder | wsVScroll | esMultiline | esAutoVScroll | esReadOnly},
		{idRoots, "LISTBOX", "", wsChild | wsVisible | wsTabStop | wsBorder | wsVScroll | lbsNotify},
		{idSelect, "BUTTON", "Selecionar", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idLoad, "BUTTON", "Carregar / Trocar", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idUnload, "BUTTON", "Descarregar", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idValidate, "BUTTON", "Validar", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idOpenCode, "BUTTON", "Abrir OpenCode", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idRefresh, "BUTTON", "Atualizar", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idAddRoot, "BUTTON", "Adicionar pasta", wsChild | wsVisible | wsTabStop | bsPushButton},
		{idRemoveRoot, "BUTTON", "Remover pasta", wsChild | wsVisible | wsTabStop | bsPushButton},
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	for _, control := range controls {
		class, _ := windows.UTF16PtrFromString(control.class)
		text, _ := windows.UTF16PtrFromString(control.text)
		handle, _, callErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(text)), control.style,
			0, 0, 100, 24, uintptr(a.window), uintptr(control.id), instance, 0)
		if handle == 0 {
			return fmt.Errorf("create dashboard control %d: %w", control.id, callErr)
		}
		a.controls[control.id] = windows.Handle(handle)
		_, _, _ = procSendMessageW.Call(handle, wmSetFont, font, 1)
	}
	cue, _ := windows.UTF16PtrFromString("Pesquisar modelos...")
	_, _, _ = procSendMessageW.Call(uintptr(a.controls[idSearch]), emSetCueBanner, 1, uintptr(unsafe.Pointer(cue)))
	var area rect
	_, _, _ = procGetClientRect.Call(uintptr(a.window), uintptr(unsafe.Pointer(&area)))
	a.layoutDashboard(area.Right-area.Left, area.Bottom-area.Top)
	a.refreshDashboard()
	return nil
}

func (a *app) layoutDashboard(width, height int32) {
	if len(a.controls) == 0 || width < 700 || height < 500 {
		return
	}
	left := int32(370)
	margin := int32(16)
	move := func(id uint32, x, y, w, h int32) {
		if handle := a.controls[id]; handle != 0 {
			_, _, _ = procMoveWindow.Call(uintptr(handle), uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
		}
	}
	move(idStatus, margin, 14, width-2*margin, 48)
	move(idSearch, margin, 70, left, 26)
	move(idModels, margin, 102, left, height-322)
	move(idRoots, margin, height-210, left, 92)
	move(idAddRoot, margin, height-110, 130, 28)
	move(idRemoveRoot, margin+138, height-110, 130, 28)
	rightX := margin + left + 16
	rightW := width - rightX - margin
	move(idDetails, rightX, 70, rightW, 285)
	move(idEvents, rightX, 365, rightW, height-485)
	buttonY := height - 110
	buttons := []uint32{idSelect, idLoad, idUnload, idValidate, idOpenCode, idRefresh}
	buttonW := (rightW - 12) / 4
	for index, id := range buttons {
		row := int32(index / 4)
		column := int32(index % 4)
		move(id, rightX+column*(buttonW+4), buttonY+row*34, buttonW, 28)
	}
}

func (a *app) showDashboard() {
	a.refreshDashboard()
	a.setDashboardTitle(a.options.Title)
	_, _, _ = procShowWindow.Call(uintptr(a.window), swShow)
	_, _, _ = procSetForegroundWind.Call(uintptr(a.window))
}

func (a *app) hideDashboard() {
	_, _, _ = procShowWindow.Call(uintptr(a.window), swHide)
	a.setDashboardTitle("")
}

func (a *app) setDashboardTitle(title string) {
	value, _ := windows.UTF16PtrFromString(title)
	_, _, _ = procSetWindowTextW.Call(uintptr(a.window), uintptr(unsafe.Pointer(value)))
}

func (a *app) requestExit() {
	a.windowMu.Lock()
	a.quitting = true
	a.windowMu.Unlock()
	a.postMessage(wmClose)
}

func (a *app) handleDashboardCommand(id, notification uint32) {
	if id == idModels && notification == lbnSelChange {
		a.updateDetails()
		return
	}
	if id == idSearch && notification == enChange {
		a.populateModels()
		return
	}
	switch id {
	case idSelect:
		if model, ok := a.dashboardModel(); ok {
			a.startAction("Selecionar modelo", true, func(ctx context.Context) error { return a.controller.SelectModel(ctx, model.ID) })
		}
	case idLoad:
		if model, ok := a.dashboardModel(); ok {
			a.startAction("Carregar modelo", false, func(ctx context.Context) error {
				a.mu.RLock()
				active := a.snapshot.ActiveModel
				a.mu.RUnlock()
				if active != "" && active != model.ID {
					return a.controller.SwitchSelected(ctx)
				}
				return a.controller.LoadSelected(ctx)
			})
		}
	case idUnload:
		a.startAction("Descarregar modelo", false, a.controller.UnloadActive)
	case idValidate:
		model, ok := a.dashboardModel()
		if ok {
			a.startAction("Validar modelo", false, func(ctx context.Context) error {
				return a.controller.ValidateModel(ctx, model.ID)
			})
		}
	case idOpenCode:
		if model, ok := a.dashboardModel(); ok {
			a.startAction("Abrir OpenCode", true, func(ctx context.Context) error { return a.controller.Launch(ctx, ClientOpenCode, model.ID) })
		}
	case idRefresh:
		a.startRefresh()
	case idAddRoot:
		if path, ok := a.chooseFolder(); ok {
			a.startAction("Adicionar pasta", false, func(ctx context.Context) error { return a.controller.AddModelRoot(ctx, path) })
		}
	case idRemoveRoot:
		if root, ok := a.selectedRoot(); ok {
			a.startAction("Remover pasta", false, func(ctx context.Context) error { return a.controller.RemoveModelRoot(ctx, root) })
		}
	}
}

func (a *app) refreshDashboard() {
	if len(a.controls) == 0 {
		return
	}
	a.mu.RLock()
	snapshot := a.snapshot
	lastErr := a.lastErr
	a.mu.RUnlock()
	server := "offline"
	if snapshot.ProviderReady {
		server = "pronto"
	} else if snapshot.UpstreamReady {
		server = "degradado"
	}
	active := snapshot.ActiveModel
	if active == "" {
		active = "nenhum (lazy load)"
	}
	status := fmt.Sprintf("Servidor: %s    Selecionado: %s    Carregado: %s\r\nFila: %d/%d    GPU: AMD Radeon RX 9070 XT    Capacidade: %s", server, snapshot.SelectedModel, active, snapshot.Queued, snapshot.MaxQueue, snapshot.CapacityNote)
	if lastErr != nil {
		status += "    Último erro: " + friendlyError(lastErr)
	}
	a.setText(idStatus, status)
	a.populateModels()
	a.populateRoots()
	a.populateEvents()
}

func (a *app) populateModels() {
	handle := a.controls[idModels]
	if handle == 0 {
		return
	}
	query := strings.ToLower(strings.TrimSpace(a.getText(idSearch)))
	a.mu.RLock()
	snapshot := a.snapshot
	a.mu.RUnlock()
	_, _, _ = procSendMessageW.Call(uintptr(handle), lbResetContent, 0, 0)
	a.filtered = a.filtered[:0]
	selectedRow := -1
	for index, model := range snapshot.Models {
		haystack := strings.ToLower(model.ID + " " + model.DisplayName + " " + model.ArtifactPath)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		badge := "indisponível"
		if model.Discovered {
			badge = "detectado"
		} else if model.Available {
			badge = "disponível"
		}
		if model.Validation == "validando" {
			badge = "validando"
		} else if model.Validation == "falhou" {
			badge = "indisponível"
		}
		if model.ID == snapshot.ActiveModel {
			badge = "ativo"
		}
		label := fmt.Sprintf("[%s] %s", badge, model.DisplayName)
		if model.ID == snapshot.SelectedModel {
			label = "✓ " + label
		}
		ptr, _ := windows.UTF16PtrFromString(label)
		_, _, _ = procSendMessageW.Call(uintptr(handle), lbAddString, 0, uintptr(unsafe.Pointer(ptr)))
		a.filtered = append(a.filtered, index)
		if model.ID == snapshot.SelectedModel {
			selectedRow = len(a.filtered) - 1
		}
	}
	if selectedRow < 0 && len(a.filtered) > 0 {
		selectedRow = 0
	}
	if selectedRow >= 0 {
		_, _, _ = procSendMessageW.Call(uintptr(handle), lbSetCurSel, uintptr(selectedRow), 0)
	}
	a.updateDetails()
}

func (a *app) updateDetails() {
	model, ok := a.dashboardModel()
	if !ok {
		a.setText(idDetails, "Nenhum modelo selecionado.")
		return
	}
	state := "indisponível"
	if model.Discovered {
		state = "detectado — aguardando validação"
	} else if model.Available {
		state = "disponível"
	}
	lines := []string{"Modelo: " + model.DisplayName, "ID: " + model.ID, "Estado: " + state, "Arquivo: " + model.ArtifactPath, "Tamanho: " + formatBytes(model.ArtifactBytes), "Runtime: " + model.Runtime, fmt.Sprintf("Contexto: %d tokens", model.ContextTokens), fmt.Sprintf("GPU layers: %d", model.GPULayers), "KV cache: " + model.Quantization, "Capacidades: " + model.Capabilities}
	if model.ArtifactSHA256 != "" {
		lines = append(lines, "SHA-256: "+model.ArtifactSHA256)
	}
	if model.Validation != "" {
		lines = append(lines, "Validação: "+model.Validation)
	}
	if model.Reason != "" {
		lines = append(lines, "Motivo: "+model.Reason)
	}
	a.setText(idDetails, strings.Join(lines, "\r\n"))
	a.mu.RLock()
	snapshot := a.snapshot
	a.mu.RUnlock()
	busy := a.busy.Load()
	selected := model.ID == snapshot.SelectedModel
	a.enable(idSelect, model.Available && !model.Discovered && !busy)
	a.enable(idLoad, selected && model.Available && !busy && snapshot.StatusAvailable && snapshot.Active == 0 && snapshot.Queued == 0)
	a.enable(idUnload, snapshot.ActiveModel != "" && !busy && snapshot.Active == 0 && snapshot.Queued == 0)
	a.enable(idValidate, (model.Discovered || model.Available) && !busy)
	a.enable(idOpenCode, model.OpenCode && snapshot.ProviderReady && !busy)
}

func (a *app) populateRoots() {
	handle := a.controls[idRoots]
	if handle == 0 {
		return
	}
	_, _, _ = procSendMessageW.Call(uintptr(handle), lbResetContent, 0, 0)
	a.mu.RLock()
	roots := append([]string(nil), a.snapshot.ModelRoots...)
	a.mu.RUnlock()
	for _, root := range roots {
		ptr, _ := windows.UTF16PtrFromString(root)
		_, _, _ = procSendMessageW.Call(uintptr(handle), lbAddString, 0, uintptr(unsafe.Pointer(ptr)))
	}
	if len(roots) > 0 {
		_, _, _ = procSendMessageW.Call(uintptr(handle), lbSetCurSel, 0, 0)
	}
}

func (a *app) populateEvents() {
	a.mu.RLock()
	events := append([]Event(nil), a.snapshot.RecentEvents...)
	a.mu.RUnlock()
	if len(events) > 20 {
		events = events[len(events)-20:]
	}
	lines := []string{"Atividade operacional recente (sem prompts ou credenciais):"}
	for _, event := range events {
		timestamp := event.Time
		if parsed, err := time.Parse(time.RFC3339Nano, event.Time); err == nil {
			timestamp = parsed.Local().Format("15:04:05")
		}
		lines = append(lines, fmt.Sprintf("%s  %s %s  %d  %d ms", timestamp, event.Method, event.Path, event.Status, event.DurationMS))
	}
	a.setText(idEvents, strings.Join(lines, "\r\n"))
}

func (a *app) dashboardModel() (Model, bool) {
	handle := a.controls[idModels]
	if handle == 0 {
		return Model{}, false
	}
	row, _, _ := procSendMessageW.Call(uintptr(handle), lbGetCurSel, 0, 0)
	if row == lbErr || int(row) >= len(a.filtered) {
		return Model{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	index := a.filtered[int(row)]
	if index < 0 || index >= len(a.snapshot.Models) {
		return Model{}, false
	}
	return a.snapshot.Models[index], true
}

func (a *app) selectedRoot() (string, bool) {
	handle := a.controls[idRoots]
	row, _, _ := procSendMessageW.Call(uintptr(handle), lbGetCurSel, 0, 0)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if row == lbErr || int(row) >= len(a.snapshot.ModelRoots) {
		return "", false
	}
	return a.snapshot.ModelRoots[int(row)], true
}

func (a *app) setText(id uint32, value string) {
	handle := a.controls[id]
	if handle == 0 {
		return
	}
	ptr, _ := windows.UTF16PtrFromString(value)
	_, _, _ = procSetWindowTextW.Call(uintptr(handle), uintptr(unsafe.Pointer(ptr)))
}
func (a *app) getText(id uint32) string {
	handle := a.controls[id]
	if handle == 0 {
		return ""
	}
	length, _, _ := procGetWindowTextLengthW.Call(uintptr(handle))
	buffer := make([]uint16, int(length)+1)
	_, _, _ = procGetWindowTextW.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), length+1)
	return string(utf16.Decode(buffer[:length]))
}
func (a *app) enable(id uint32, enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	if handle := a.controls[id]; handle != 0 {
		_, _, _ = procEnableWindow.Call(uintptr(handle), value)
	}
}

func (a *app) chooseFolder() (string, bool) {
	display := make([]uint16, windows.MAX_PATH)
	title, _ := windows.UTF16PtrFromString("Escolha uma pasta que contenha modelos GGUF")
	info := browseInfo{Owner: a.window, DisplayName: &display[0], Title: title, Flags: bifReturnOnlyFSDirs | bifNewDialogStyle}
	item, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&info)))
	if item == 0 {
		return "", false
	}
	defer procCoTaskMemFree.Call(item)
	path := make([]uint16, windows.MAX_PATH)
	ok, _, _ := procSHGetPathFromIDListW.Call(item, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return "", false
	}
	return filepath.Clean(windows.UTF16ToString(path)), true
}

func formatBytes(bytes int64) string {
	if bytes <= 0 {
		return "—"
	}
	const gib = 1024 * 1024 * 1024
	return fmt.Sprintf("%.2f GiB", float64(bytes)/gib)
}
