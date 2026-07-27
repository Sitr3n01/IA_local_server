//go:build windows

package trayui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	wmApp           = 0x8000
	wmTray          = wmApp + 1
	wmResult        = wmApp + 2

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002
	nimSetVer = 0x00000004

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString    = 0x00000000
	mfGray      = 0x00000001
	mfDisabled  = 0x00000002
	mfChecked   = 0x00000008
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002
	tpmNonotify    = 0x0080
	tpmReturnCmd   = 0x0100

	mbOK               = 0x00000000
	mbIconError        = 0x00000010
	mbIconInformation  = 0x00000040
	notifyIconVersion4 = 4

	commandPanel   = 99
	commandRefresh = 100
	commandLoad    = 101
	commandSwitch  = 102
	commandUnload  = 103
	commandCodex   = 110
	commandOpen    = 111
	commandStatus  = 120
	commandExit    = 199
	commandModel   = 1000
)

type point struct {
	X int32
	Y int32
}

type message struct {
	Window  windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}

type windowClass struct {
	Size        uint32
	Style       uint32
	WindowProc  uintptr
	ClassExtra  int32
	WindowExtra int32
	Instance    windows.Handle
	Icon        windows.Handle
	Cursor      windows.Handle
	Background  windows.Handle
	MenuName    *uint16
	ClassName   *uint16
	IconSmall   windows.Handle
}

type notifyIconData struct {
	Size            uint32
	Window          windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

type actionResult struct {
	title   string
	message string
	err     error
	quiet   bool
}

type app struct {
	controller Controller
	options    Options
	window     windows.Handle
	icon       windows.Handle
	iconAdded  bool
	taskbarMsg uint32
	ctx        context.Context
	cancel     context.CancelFunc

	mu       sync.RWMutex
	windowMu sync.RWMutex
	workers  sync.WaitGroup
	closed   bool
	quitting bool
	snapshot Snapshot
	lastErr  error
	commands map[uint32]string
	results  chan actionResult
	busy     atomic.Bool
	refresh  atomic.Bool
	controls map[uint32]windows.Handle
	filtered []int
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procAppendMenuW        = user32.NewProc("AppendMenuW")
	procCreateIcon         = user32.NewProc("CreateIcon")
	procCreatePopupMenu    = user32.NewProc("CreatePopupMenu")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
	procDestroyMenu        = user32.NewProc("DestroyMenu")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procMessageBoxW        = user32.NewProc("MessageBoxW")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procRegisterWindowMsgW = user32.NewProc("RegisterWindowMessageW")
	procSetForegroundWind  = user32.NewProc("SetForegroundWindow")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procSetTimer           = user32.NewProc("SetTimer")
	procShellNotifyIconW   = shell32.NewProc("Shell_NotifyIconW")
	procTrackPopupMenu     = user32.NewProc("TrackPopupMenu")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")

	apps sync.Map
)

// Run owns the Win32 message loop on the calling goroutine. Network and model
// operations are always performed on workers and report completion through a
// private window message, so a cold model load cannot freeze Explorer's tray.
func Run(ctx context.Context, controller Controller, options Options) error {
	if controller == nil {
		return errors.New("tray controller is required")
	}
	if strings.TrimSpace(options.Title) == "" {
		options.Title = "CIA Local AI"
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = 10 * time.Second
	}
	if options.RefreshInterval < 2*time.Second || options.RefreshInterval > 5*time.Minute {
		return errors.New("tray refresh interval must be between 2 seconds and 5 minutes")
	}
	if strings.TrimSpace(options.InstanceID) == "" {
		options.InstanceID = "default"
	}
	instanceMutex, err := acquireInstanceMutex(options.InstanceID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(instanceMutex)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _, _ = procSetProcessDPIAware.Call()

	appCtx, cancel := context.WithCancel(ctx)
	a := &app{
		controller: controller,
		options:    options,
		commands:   make(map[uint32]string),
		results:    make(chan actionResult, 8),
		ctx:        appCtx,
		cancel:     cancel,
	}
	if snapshot, err := snapshotWithTimeout(ctx, controller, 4*time.Second); err == nil {
		a.snapshot = snapshot
	} else {
		if len(snapshot.Models) != 0 {
			a.snapshot = snapshot
		}
		a.lastErr = err
	}

	if err := a.createWindow(); err != nil {
		cancel()
		return err
	}
	// A VBS launcher supplies STARTF_USESHOWWINDOW=SW_HIDE. Windows applies
	// that startup state to the first ShowWindow call regardless of its
	// argument, so consume it here while the panel is intentionally hidden.
	// The first tray double-click can then show the dashboard immediately.
	_, _, _ = procShowWindow.Call(uintptr(a.window), swHide)
	defer a.destroy()
	defer a.stopWorkers()
	apps.Store(a.window, a)
	defer apps.Delete(a.window)
	if err := a.createDashboardControls(); err != nil {
		return err
	}

	if err := a.initializeTaskbarIntegration(); err != nil {
		return err
	}
	if err := a.registerIcon(); err != nil {
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
	}
	a.updateTooltip()
	intervalMS := uintptr(options.RefreshInterval / time.Millisecond)
	if result, _, callErr := procSetTimer.Call(uintptr(a.window), 1, intervalMS, 0); result == 0 {
		return fmt.Errorf("create tray refresh timer: %w", callErr)
	}

	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		<-appCtx.Done()
		a.requestExit()
	}()

	var msg message
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		signed := int32(result)
		if signed == -1 {
			return fmt.Errorf("read tray window message: %w", callErr)
		}
		if signed == 0 {
			return nil
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func snapshotWithTimeout(parent context.Context, controller Controller, timeout time.Duration) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return controller.Snapshot(ctx)
}

func acquireInstanceMutex(instanceID string) (windows.Handle, error) {
	if len(instanceID) > 100 || strings.ContainsAny(instanceID, "\\/\x00\r\n") {
		return 0, errors.New("tray instance ID is invalid")
	}
	name, _ := windows.UTF16PtrFromString("Local\\CIA.LocalAI.Tray." + instanceID)
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, fmt.Errorf("create tray instance mutex: %w", callErr)
	}
	if errors.Is(callErr, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(windows.Handle(handle))
		return 0, fmt.Errorf("%w: CIA Local AI panel %q", ErrAlreadyRunning, instanceID)
	}
	return windows.Handle(handle), nil
}

func (a *app) createWindow() error {
	instance, _, callErr := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return fmt.Errorf("get process module: %w", callErr)
	}
	className, _ := windows.UTF16PtrFromString(fmt.Sprintf("CIA.LocalAI.Tray.%d", windows.GetCurrentProcessId()))
	title, _ := windows.UTF16PtrFromString("")
	class := windowClass{
		Size:       uint32(unsafe.Sizeof(windowClass{})),
		WindowProc: windows.NewCallback(windowProc),
		Instance:   windows.Handle(instance),
		ClassName:  className,
	}
	atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("register tray window class: %w", registerErr)
	}
	window, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		100, 100, 1040, 720,
		0, 0, instance, 0,
	)
	if window == 0 {
		return fmt.Errorf("create tray message window: %w", createErr)
	}
	a.window = windows.Handle(window)
	return nil
}

func (a *app) initializeTaskbarIntegration() error {
	taskbarCreated, _ := windows.UTF16PtrFromString("TaskbarCreated")
	messageID, _, registerErr := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(taskbarCreated)))
	if messageID == 0 {
		return fmt.Errorf("register Explorer restart message: %w", registerErr)
	}
	a.taskbarMsg = uint32(messageID)

	icon, err := createProviderIcon()
	if err != nil {
		return err
	}
	a.icon = icon
	return nil
}

func (a *app) registerIcon() error {
	if a.window == 0 || a.icon == 0 {
		return errors.New("notification-area icon is not initialized")
	}
	data := a.iconData()
	result, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	if result == 0 {
		return fmt.Errorf("add notification-area icon: %w", callErr)
	}
	a.iconAdded = true
	data.Version = notifyIconVersion4
	result, _, callErr = procShellNotifyIconW.Call(nimSetVer, uintptr(unsafe.Pointer(&data)))
	if result == 0 {
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
		a.iconAdded = false
		return fmt.Errorf("set notification-area icon version: %w", callErr)
	}
	return nil
}

func (a *app) iconData() notifyIconData {
	data := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          a.window,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTray,
		Icon:            a.icon,
	}
	a.mu.RLock()
	tip := a.tooltipLocked()
	a.mu.RUnlock()
	copyUTF16(data.Tip[:], tip)
	return data
}

func (a *app) destroy() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	if a.window != 0 {
		data := a.iconData()
		if a.iconAdded {
			_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
			a.iconAdded = false
		}
		_, _, _ = procDestroyWindow.Call(uintptr(a.window))
		a.window = 0
	}
	if a.icon != 0 {
		_, _, _ = procDestroyIcon.Call(uintptr(a.icon))
		a.icon = 0
	}
}

func (a *app) stopWorkers() {
	if a.cancel != nil {
		a.cancel()
	}
	a.workers.Wait()
}

func (a *app) postMessage(message uint32) {
	a.windowMu.RLock()
	defer a.windowMu.RUnlock()
	if a.closed || a.window == 0 {
		return
	}
	_, _, _ = procPostMessageW.Call(uintptr(a.window), uintptr(message), 0, 0)
}

func (a *app) beginClose(window uintptr) {
	a.windowMu.Lock()
	if !a.closed {
		a.closed = true
		if a.cancel != nil {
			a.cancel()
		}
	}
	a.windowMu.Unlock()
	_, _, _ = procDestroyWindow.Call(window)
}

func windowProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, ok := apps.Load(windows.Handle(window))
	if !ok {
		result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
		return result
	}
	a := value.(*app)
	if message == a.taskbarMsg {
		a.iconAdded = false
		if err := a.registerIcon(); err != nil {
			a.mu.Lock()
			a.lastErr = err
			a.mu.Unlock()
		} else {
			a.updateTooltip()
		}
		return 0
	}
	switch message {
	case wmTray:
		event := uint32(lParam) & 0xffff
		if event == wmLButtonUp || event == wmLButtonDblClk {
			a.showDashboard()
			return 0
		}
		if event == wmRButtonUp || event == wmContextMenu {
			a.showMenu()
			return 0
		}
	case wmCommand:
		a.handleDashboardCommand(uint32(wParam&0xffff), uint32((wParam>>16)&0xffff))
		return 0
	case wmSize:
		a.layoutDashboard(int32(lParam&0xffff), int32((lParam>>16)&0xffff))
		return 0
	case wmTimer:
		if !a.iconAdded {
			if err := a.registerIcon(); err != nil {
				a.mu.Lock()
				a.lastErr = err
				a.mu.Unlock()
			}
		}
		a.startRefresh()
		return 0
	case wmResult:
		a.handleResults()
		return 0
	case wmClose:
		a.windowMu.RLock()
		quitting := a.quitting
		a.windowMu.RUnlock()
		if quitting {
			a.beginClose(window)
		} else {
			a.hideDashboard()
		}
		return 0
	case wmDestroy:
		data := a.iconData()
		if a.iconAdded {
			_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
			a.iconAdded = false
		}
		a.windowMu.Lock()
		if a.window == windows.Handle(window) {
			a.window = 0
		}
		a.windowMu.Unlock()
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return result
}

func (a *app) showMenu() {
	a.startRefresh()
	menu, _, err := procCreatePopupMenu.Call()
	if menu == 0 {
		a.showMessage("CIA Local AI", fmt.Sprintf("Não foi possível abrir o menu: %v", err), true)
		return
	}
	defer procDestroyMenu.Call(menu)

	a.mu.RLock()
	snapshot := a.snapshot
	lastErr := a.lastErr
	a.mu.RUnlock()
	busy := a.busy.Load()

	provider := "Servidor: offline"
	if snapshot.ProviderReady {
		provider = "Servidor: pronto"
	} else if snapshot.UpstreamReady {
		provider = "Servidor: degradado"
	}
	if lastErr != nil && !snapshot.ProviderReady {
		provider = "Servidor: indisponível"
	}
	a.append(menu, commandPanel, "Abrir painel", true, false)
	a.separator(menu)
	a.append(menu, 0, provider, false, false)
	active := snapshot.ActiveModel
	if active == "" {
		active = "nenhum (lazy load)"
	}
	a.append(menu, 0, "Carregado: "+active, false, false)
	a.append(menu, 0, fmt.Sprintf("Fila: %d/%d", snapshot.Queued, snapshot.MaxQueue), false, false)
	a.separator(menu)
	policy := EvaluateActions(snapshot, busy)
	a.append(menu, commandLoad, "Carregar modelo selecionado", policy.Load, false)
	if policy.AvailableModels > 1 {
		a.append(menu, commandSwitch, "Trocar para o modelo selecionado", policy.Switch, false)
	} else {
		a.append(menu, commandSwitch, "Troca indisponível — apenas um modelo qualificado", false, false)
	}
	a.append(menu, commandUnload, "Descarregar modelo ativo", policy.Unload, false)
	a.separator(menu)
	a.append(menu, commandOpen, "Abrir no OpenCode", policy.LaunchOpenCode, false)
	a.separator(menu)
	a.append(menu, commandRefresh, "Atualizar", !busy, false)
	a.append(menu, commandStatus, "Detalhes do status", true, false)
	a.append(menu, commandExit, "Fechar painel", policy.Exit, false)

	var cursor point
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		return
	}
	_, _, _ = procSetForegroundWind.Call(uintptr(a.window))
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmNonotify|tpmReturnCmd, uintptr(cursor.X), uintptr(cursor.Y), 0, uintptr(a.window), 0)
	if command != 0 {
		a.dispatch(uint32(command))
	}
}

func (a *app) append(menu uintptr, id uint32, label string, enabled, checked bool) {
	flags := uintptr(mfString)
	if !enabled {
		flags |= mfGray | mfDisabled
	}
	if checked {
		flags |= mfChecked
	}
	text, _ := windows.UTF16PtrFromString(label)
	_, _, _ = procAppendMenuW.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(text)))
}

func (a *app) separator(menu uintptr) {
	_, _, _ = procAppendMenuW.Call(menu, mfSeparator, 0, 0)
}

func (a *app) dispatch(command uint32) {
	if modelID, ok := a.commands[command]; ok {
		a.startAction("Selecionar modelo", true, func(ctx context.Context) error {
			return a.controller.SelectModel(ctx, modelID)
		})
		return
	}
	switch command {
	case commandPanel:
		a.showDashboard()
	case commandRefresh:
		a.startRefresh()
	case commandLoad:
		a.startAction("Carregar modelo", false, a.controller.LoadSelected)
	case commandSwitch:
		a.startAction("Trocar modelo", false, a.controller.SwitchSelected)
	case commandUnload:
		a.startAction("Descarregar modelo", false, a.controller.UnloadActive)
	case commandOpen:
		a.startAction("Abrir OpenCode", true, func(ctx context.Context) error {
			a.mu.RLock()
			modelID := a.snapshot.SelectedModel
			a.mu.RUnlock()
			return a.controller.Launch(ctx, ClientOpenCode, modelID)
		})
	case commandStatus:
		a.showStatus()
	case commandExit:
		if !a.busy.Load() {
			a.requestExit()
		}
	}
}

func (a *app) startAction(title string, quiet bool, action func(context.Context) error) {
	if a.ctx.Err() != nil {
		return
	}
	if !a.busy.CompareAndSwap(false, true) {
		return
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
		defer cancel()
		err := action(ctx)
		if a.ctx.Err() != nil {
			return
		}
		a.results <- actionResult{title: title, message: "Operação concluída.", err: err, quiet: quiet}
		a.postMessage(wmResult)
	}()
}

func (a *app) startRefresh() {
	if a.ctx.Err() != nil {
		return
	}
	if !a.refresh.CompareAndSwap(false, true) {
		return
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer a.refresh.Store(false)
		snapshot, err := snapshotWithTimeout(a.ctx, a.controller, 4*time.Second)
		if a.ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		if err == nil || len(snapshot.Models) != 0 {
			a.snapshot = snapshot
		}
		a.lastErr = err
		a.mu.Unlock()
		a.results <- actionResult{quiet: true}
		a.postMessage(wmResult)
	}()
}

func (a *app) handleResults() {
	for {
		select {
		case result := <-a.results:
			if result.title != "" {
				a.busy.Store(false)
				if result.err != nil {
					a.showMessage(result.title, friendlyError(result.err), true)
				} else if !result.quiet {
					a.showMessage(result.title, result.message, false)
				}
				a.startRefresh()
			}
		default:
			a.updateTooltip()
			a.refreshDashboard()
			return
		}
	}
}

func (a *app) updateTooltip() {
	if a.window == 0 || a.icon == 0 || !a.iconAdded {
		return
	}
	data := a.iconData()
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func (a *app) tooltipLocked() string {
	state := "offline"
	if a.snapshot.ProviderReady {
		state = "pronto"
	} else if a.snapshot.UpstreamReady {
		state = "degradado"
	}
	selected := a.snapshot.SelectedModel
	if selected == "" {
		selected = "nenhum"
	}
	active := a.snapshot.ActiveModel
	if active == "" {
		active = "lazy"
	}
	return fmt.Sprintf("CIA Local AI: %s | selecionado %s | carregado %s", state, selected, active)
}

func (a *app) showStatus() {
	a.mu.RLock()
	s := a.snapshot
	err := a.lastErr
	a.mu.RUnlock()
	provider := "não pronto"
	if s.ProviderReady {
		provider = "pronto"
	}
	active := s.ActiveModel
	if active == "" {
		active = "nenhum (carregamento sob demanda)"
	}
	capacity := "indisponível"
	if s.CapacityOK {
		capacity = "disponível"
	}
	lines := []string{
		"Ambiente: " + s.Environment,
		"Servidor: " + provider,
		"Modelo selecionado: " + s.SelectedModel,
		"Modelo carregado: " + active,
		fmt.Sprintf("Inferências: %d/%d", s.Active, s.MaxActive),
		fmt.Sprintf("Fila: %d/%d", s.Queued, s.MaxQueue),
		"Capacidade: " + capacity,
	}
	if !s.StatusAvailable {
		lines = append(lines, "Status operacional: indisponível; controles administrativos bloqueados")
	}
	if s.CapacityNote != "" {
		lines = append(lines, "Motivo: "+s.CapacityNote)
	}
	if err != nil {
		lines = append(lines, "Última atualização: "+friendlyError(err))
	}
	a.showMessage("Status — CIA Local AI", strings.Join(lines, "\r\n"), false)
}

func (a *app) showMessage(title, message string, isError bool) {
	flags := uintptr(mbOK | mbIconInformation)
	if isError {
		flags = mbOK | mbIconError
	}
	titlePtr, _ := windows.UTF16PtrFromString(title)
	messagePtr, _ := windows.UTF16PtrFromString(message)
	_, _, _ = procMessageBoxW.Call(uintptr(a.window), uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), flags)
}

func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "inference_busy"):
		return "Há uma inferência ativa ou aguardando. Tente novamente quando a fila estiver vazia."
	case strings.Contains(text, "insufficient_capacity"):
		return "O modelo não cabe com as margens de segurança atuais."
	case strings.Contains(text, "invalid_api_key"), strings.Contains(text, "credential"):
		return "A credencial administrativa está ausente ou inválida."
	case strings.Contains(text, "model_not_found"):
		return "O modelo não está autorizado neste ambiente."
	case strings.Contains(text, "connection refused"), strings.Contains(text, "unavailable"):
		return "O provedor local não está disponível."
	default:
		if len(text) > 400 {
			text = text[:400]
		}
		return text
	}
}

func copyUTF16(destination []uint16, value string) {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) >= len(destination) {
		encoded = encoded[:len(destination)-1]
	}
	copy(destination, encoded)
	destination[len(encoded)] = 0
}

// createProviderIcon builds an original icon in memory so the executable does
// not depend on the undocumented legacy tray artwork.
func createProviderIcon() (windows.Handle, error) {
	const size = 32
	andMask := make([]byte, size*size/8)
	color := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-15, y-15
			distance := dx*dx + dy*dy
			outside := distance > 15*15
			if outside {
				andMask[y*4+x/8] |= 1 << (7 - uint(x%8))
				continue
			}
			b, g, r := byte(32), byte(32), byte(32)
			if distance >= 12*12 {
				b, g, r = 0, 122, 255
			}
			if iconNode(x, y) {
				b, g, r = 245, 245, 245
			}
			// CreateIcon expects bottom-up BGRA scanlines for a 32-bit XOR mask.
			index := ((size-1-y)*size + x) * 4
			color[index], color[index+1], color[index+2], color[index+3] = b, g, r, 255
		}
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	result, _, callErr := procCreateIcon.Call(
		instance,
		size,
		size,
		1,
		32,
		uintptr(unsafe.Pointer(&andMask[0])),
		uintptr(unsafe.Pointer(&color[0])),
	)
	if result == 0 {
		return 0, fmt.Errorf("create provider tray icon: %w", callErr)
	}
	return windows.Handle(result), nil
}

func iconNode(x, y int) bool {
	nodes := [][2]int{{10, 11}, {21, 9}, {19, 21}}
	for _, node := range nodes {
		if abs(x-node[0]) <= 2 && abs(y-node[1]) <= 2 {
			return true
		}
	}
	// Two thin links make a stable network/provider glyph at 16-32 px.
	return (x >= 12 && x <= 19 && abs((20-x)/2+9-y) <= 1) ||
		(x >= 12 && x <= 18 && abs((x-12)*2/3+12-y) <= 1)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
