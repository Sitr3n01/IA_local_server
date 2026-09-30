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
	wmDestroy        = 0x0002
	wmActivate       = 0x0006
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmQueryEndSess   = 0x0011
	wmEraseBkgnd     = 0x0014
	wmEndSession     = 0x0016
	wmSettingChange  = 0x001A
	wmSetCursor      = 0x0020
	wmContextMenu    = 0x007B
	wmKeyDown        = 0x0100
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmMouseWheel     = 0x020A
	wmMouseLeave     = 0x02A3
	wmDPIChanged     = 0x02E0
	wmUser           = 0x0400
	wmApp            = 0x8000
	wmTray           = wmApp + 1
	wmResult         = wmApp + 2
	ninSelect        = wmUser + 0
	ninKeySelect     = wmUser + 1
	ninBalloonUserCl = wmUser + 5

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002
	nimSetVer = 0x00000004

	nifMessage  = 0x00000001
	nifIcon     = 0x00000002
	nifTip      = 0x00000004
	nifInfo     = 0x00000010
	nifShowTip  = 0x00000080
	niifInfo    = 0x00000001
	niifError   = 0x00000003
	niifNoSound = 0x00000010
	niifQuiet   = 0x00000080

	notifyIconVersion4 = 4

	wsPopup         = 0x80000000
	wsExToolWindow  = 0x00000080
	wsExTopmost     = 0x00000008
	csDropShadow    = 0x00020000
	swHide          = 0
	asfwAny         = ^uintptr(0)
	timerRefresh    = 1
	fastRefresh     = 2 * time.Second
	startingTimeout = 2 * time.Minute
)

type point struct {
	X int32
	Y int32
}

type rect struct{ Left, Top, Right, Bottom int32 }

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

// actionResult reports one finished background action to the UI thread.
type actionResult struct {
	title   string // the notification title when the flyout is closed
	done    string // the notification text on success; empty keeps success quiet
	err     error
	refresh bool // a snapshot refresh, not an action
	exit    bool // the tray exits after a successful action
	launch  bool // a launch never held the busy flag
	started bool // the server was asked to start
}

type app struct {
	controller Controller
	options    Options
	window     windows.Handle
	flyout     *flyout
	icons      map[Tone]windows.Handle
	iconSize   int32
	iconAdded  bool
	taskbarMsg uint32
	showMsg    uint32
	interval   time.Duration
	ctx        context.Context
	cancel     context.CancelFunc

	mu        sync.RWMutex
	windowMu  sync.RWMutex
	workers   sync.WaitGroup
	closed    bool
	quitting  bool
	snapshot  Snapshot
	loaded    bool // the first snapshot arrived
	lastErr   error
	activity  string
	actionErr error
	startedAt time.Time
	results   chan actionResult
	busy      atomic.Bool
	refresh   atomic.Bool
	autoStart atomic.Bool
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procDestroyIcon           = user32.NewProc("DestroyIcon")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procAllowSetForegroundWnd = user32.NewProc("AllowSetForegroundWindow")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procPostMessageW          = user32.NewProc("PostMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procRegisterWindowMsgW    = user32.NewProc("RegisterWindowMessageW")
	procSetTimer              = user32.NewProc("SetTimer")
	procKillTimer             = user32.NewProc("KillTimer")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
	procSetProcessDPICtx      = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetSystemMetricsDPI   = user32.NewProc("GetSystemMetricsForDpi")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procCreateIconIndirect    = user32.NewProc("CreateIconIndirect")
	procShellNotifyIconW      = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW          = kernel32.NewProc("CreateMutexW")

	apps sync.Map
)

// dpiAwarenessPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2.
const dpiAwarenessPerMonitorV2 = ^uintptr(3)

// Run owns the Win32 message loop on the calling goroutine. Network, model and
// process operations always run on workers and report completion through a
// private window message, so a cold model load cannot freeze Explorer's tray.
func Run(ctx context.Context, controller Controller, options Options) error {
	if controller == nil {
		return errors.New("tray controller is required")
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
	if len(options.InstanceID) > 100 || strings.ContainsAny(options.InstanceID, "\\/\x00\r\n") {
		return errors.New("tray instance ID is invalid")
	}
	className := "CIA.LocalAI.Tray." + options.InstanceID
	showMsg, err := registerMessage("CIA.LocalAI.Tray.Show")
	if err != nil {
		return err
	}
	instanceMutex, err := acquireInstanceMutex(options.InstanceID)
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			showRunningInstance(className, showMsg)
		}
		return err
	}
	defer windows.CloseHandle(instanceMutex)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// DPI awareness is also declared in the executable's manifest; the call
	// covers builds without it and fails harmlessly when the manifest won.
	if result, _, _ := procSetProcessDPICtx.Call(dpiAwarenessPerMonitorV2); result == 0 {
		_, _, _ = procSetProcessDPIAware.Call()
	}
	if err := startGDIPlus(); err != nil {
		return err
	}

	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := &app{
		controller: controller,
		options:    options,
		showMsg:    showMsg,
		icons:      make(map[Tone]windows.Handle),
		results:    make(chan actionResult, 16),
		ctx:        appCtx,
		cancel:     cancel,
	}
	a.snapshot.Environment = options.Environment
	if err := a.createWindow(className); err != nil {
		return err
	}
	defer a.destroy()
	defer a.stopWorkers()
	apps.Store(a.window, a)
	defer apps.Delete(a.window)

	flyout, err := newFlyout(a)
	if err != nil {
		return err
	}
	a.flyout = flyout
	defer flyout.destroy()

	taskbarCreated, err := registerMessage("TaskbarCreated")
	if err != nil {
		return err
	}
	a.taskbarMsg = taskbarCreated
	if err := a.registerIcon(); err != nil {
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
	}
	a.setRefreshInterval(fastRefresh)

	// Opening the tray means running the system: the first snapshot decides
	// whether the server has to be started.
	a.autoStart.Store(true)
	a.startRefresh()

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

func registerMessage(name string) (uint32, error) {
	value, _ := windows.UTF16PtrFromString(name)
	id, _, err := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(value)))
	if id == 0 {
		return 0, fmt.Errorf("register window message %s: %w", name, err)
	}
	return uint32(id), nil
}

func snapshotWithTimeout(parent context.Context, controller Controller, timeout time.Duration) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return controller.Snapshot(ctx)
}

func acquireInstanceMutex(instanceID string) (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("Local\\CIA.LocalAI.Tray." + instanceID)
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, fmt.Errorf("create tray instance mutex: %w", callErr)
	}
	if errors.Is(callErr, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(windows.Handle(handle))
		return 0, fmt.Errorf("%w: IA Local %q", ErrAlreadyRunning, instanceID)
	}
	return windows.Handle(handle), nil
}

// showRunningInstance asks the tray that already runs to open its flyout, so
// starting IA Local a second time answers instead of doing nothing. This
// process was started by the user and may hand its foreground right over.
func showRunningInstance(className string, showMsg uint32) {
	name, _ := windows.UTF16PtrFromString(className)
	window, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(name)), 0)
	if window == 0 {
		return
	}
	_, _, _ = procAllowSetForegroundWnd.Call(asfwAny)
	_, _, _ = procPostMessageW.Call(window, uintptr(showMsg), 0, 0)
}

// createWindow creates the hidden top-level window that owns the icon. It is
// top-level, not message-only, because Explorer's TaskbarCreated broadcast
// reaches only top-level windows.
func (a *app) createWindow(className string) error {
	instance, _, callErr := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return fmt.Errorf("get process module: %w", callErr)
	}
	name, _ := windows.UTF16PtrFromString(className)
	title, _ := windows.UTF16PtrFromString("IA Local")
	class := windowClass{
		Size:       uint32(unsafe.Sizeof(windowClass{})),
		WindowProc: windows.NewCallback(windowProc),
		Instance:   windows.Handle(instance),
		ClassName:  name,
	}
	if atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return fmt.Errorf("register tray window class: %w", registerErr)
	}
	window, _, createErr := procCreateWindowExW.Call(wsExToolWindow, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
		wsPopup, 0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		return fmt.Errorf("create tray window: %w", createErr)
	}
	a.window = windows.Handle(window)
	return nil
}

// trayIcon returns the icon for a tone at the notification area's current
// size, drawing it on first use.
func (a *app) trayIcon(tone Tone) (windows.Handle, error) {
	size := smallIconSize()
	if size != a.iconSize {
		a.releaseIcons()
		a.iconSize = size
	}
	if icon, ok := a.icons[tone]; ok {
		return icon, nil
	}
	pixels, err := RenderMark(int(size), IconColor(tone), 0xffffffff)
	if err != nil {
		return 0, err
	}
	icon, err := iconFromPixels(pixels, int(size))
	if err != nil {
		return 0, err
	}
	a.icons[tone] = icon
	return icon, nil
}

func (a *app) releaseIcons() {
	for tone, icon := range a.icons {
		_, _, _ = procDestroyIcon.Call(uintptr(icon))
		delete(a.icons, tone)
	}
}

func smallIconSize() int32 {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	const smCxSmIcon = 49
	if size, _, _ := procGetSystemMetricsDPI.Call(smCxSmIcon, dpi); size != 0 {
		return int32(size)
	}
	return int32(16 * dpi / 96)
}

func (a *app) registerIcon() error {
	if a.window == 0 {
		return errors.New("notification-area window is not initialized")
	}
	data, err := a.iconData()
	if err != nil {
		return err
	}
	if result, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data))); result == 0 {
		return fmt.Errorf("add notification-area icon: %w", callErr)
	}
	a.iconAdded = true
	data.Version = notifyIconVersion4
	if result, _, callErr := procShellNotifyIconW.Call(nimSetVer, uintptr(unsafe.Pointer(&data))); result == 0 {
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
		a.iconAdded = false
		return fmt.Errorf("set notification-area icon version: %w", callErr)
	}
	return nil
}

func (a *app) iconData() (notifyIconData, error) {
	view := BuildView(a.viewState())
	icon, err := a.trayIcon(view.IconTone)
	if err != nil {
		return notifyIconData{}, err
	}
	data := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          a.window,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip | nifShowTip,
		CallbackMessage: wmTray,
		Icon:            icon,
	}
	copyUTF16(data.Tip[:], view.Tooltip)
	return data, nil
}

// updateIcon refreshes the icon's colour and tooltip after a state change.
func (a *app) updateIcon() {
	if a.window == 0 || !a.iconAdded {
		return
	}
	data, err := a.iconData()
	if err != nil {
		return
	}
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

// notify shows a Windows notification from the icon. The tray uses it only
// when the flyout is closed, to report how an action it started ended.
func (a *app) notify(title, text string, isError bool) {
	if !a.iconAdded || a.window == 0 {
		return
	}
	data := notifyIconData{
		Size:      uint32(unsafe.Sizeof(notifyIconData{})),
		Window:    a.window,
		ID:        1,
		Flags:     nifInfo,
		InfoFlags: niifInfo | niifNoSound | niifQuiet,
	}
	if isError {
		data.InfoFlags = niifError | niifQuiet
	}
	copyUTF16(data.InfoTitle[:], title)
	copyUTF16(data.Info[:], text)
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func (a *app) destroy() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	if a.window != 0 {
		a.removeIcon()
		_, _, _ = procDestroyWindow.Call(uintptr(a.window))
		a.window = 0
	}
	a.releaseIcons()
}

func (a *app) removeIcon() {
	if !a.iconAdded {
		return
	}
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Window: a.window, ID: 1}
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	a.iconAdded = false
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

func (a *app) requestExit() {
	a.windowMu.Lock()
	a.quitting = true
	a.windowMu.Unlock()
	a.postMessage(wmClose)
}

func (a *app) setRefreshInterval(interval time.Duration) {
	if interval == a.interval || a.window == 0 {
		return
	}
	if result, _, _ := procSetTimer.Call(uintptr(a.window), timerRefresh, uintptr(interval/time.Millisecond), 0); result != 0 {
		a.interval = interval
	}
}

// adjustRefresh polls fast while someone is looking or something is moving,
// and at the configured pace otherwise.
func (a *app) adjustRefresh() {
	a.mu.RLock()
	starting := !a.startedAt.IsZero()
	a.mu.RUnlock()
	if starting || a.busy.Load() || (a.flyout != nil && a.flyout.visible) {
		a.setRefreshInterval(fastRefresh)
		return
	}
	a.setRefreshInterval(a.options.RefreshInterval)
}

func windowProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, ok := apps.Load(windows.Handle(window))
	if !ok {
		result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
		return result
	}
	a := value.(*app)
	// Registered message IDs are compared only once they exist: before that
	// they are zero, which is WM_NULL.
	if a.taskbarMsg != 0 && message == a.taskbarMsg {
		// Explorer restarted and forgot every icon.
		a.iconAdded = false
		if err := a.registerIcon(); err != nil {
			a.mu.Lock()
			a.lastErr = err
			a.mu.Unlock()
		}
		return 0
	}
	if a.showMsg != 0 && message == a.showMsg {
		if a.flyout != nil {
			a.flyout.show()
		}
		return 0
	}
	switch message {
	case wmTray:
		if a.flyout == nil {
			return 0
		}
		switch uint32(lParam) & 0xffff {
		case ninSelect, ninKeySelect, wmContextMenu:
			a.flyout.toggle()
		case ninBalloonUserCl:
			a.flyout.show()
		}
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
	case wmSettingChange:
		// The notification area's icon size follows the system DPI.
		if smallIconSize() != a.iconSize {
			a.updateIcon()
		}
	case wmQueryEndSess:
		return 1
	case wmEndSession:
		if wParam != 0 {
			a.removeIcon()
		}
		return 0
	case wmClose:
		a.windowMu.RLock()
		quitting := a.quitting
		a.windowMu.RUnlock()
		if quitting {
			a.beginClose(window)
		}
		return 0
	case wmDestroy:
		a.removeIcon()
		a.windowMu.Lock()
		if a.window == windows.Handle(window) {
			a.window = 0
		}
		a.windowMu.Unlock()
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return result
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
	if a.flyout != nil {
		a.flyout.hide()
	}
	_, _, _ = procDestroyWindow.Call(window)
}

// viewState collects what BuildView needs under the state lock.
func (a *app) viewState() ViewState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return ViewState{
		Snapshot:  a.snapshot,
		Loaded:    a.loaded,
		Activity:  a.activity,
		Starting:  !a.startedAt.IsZero(),
		ActionErr: a.actionErr,
	}
}

// startAction runs one lifecycle action. Only one runs at a time; activity is
// what the flyout shows while it runs.
func (a *app) startAction(activity, title, done string, action func(context.Context) error, exit bool) {
	if a.ctx.Err() != nil || !a.busy.CompareAndSwap(false, true) {
		return
	}
	a.mu.Lock()
	a.activity = activity
	a.actionErr = nil
	a.mu.Unlock()
	a.changed()
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
		defer cancel()
		err := action(ctx)
		if a.ctx.Err() != nil {
			return
		}
		a.results <- actionResult{title: title, done: done, err: err, exit: exit}
		a.postMessage(wmResult)
	}()
}

// startLaunch opens another program. Launches do not hold the busy flag: they
// return as soon as the program starts and change nothing in the server.
func (a *app) startLaunch(title string, action func(context.Context) error) {
	if a.ctx.Err() != nil {
		return
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
		defer cancel()
		err := action(ctx)
		if a.ctx.Err() != nil {
			return
		}
		a.results <- actionResult{title: title, err: err, launch: true}
		a.postMessage(wmResult)
	}()
}

// startServer asks the controller to start the router and edge; the ordinary
// refreshes then follow the edge until it answers.
func (a *app) startServer() {
	if a.ctx.Err() != nil || !a.busy.CompareAndSwap(false, true) {
		return
	}
	a.mu.Lock()
	a.activity = "Iniciando o servidor"
	a.actionErr = nil
	a.mu.Unlock()
	a.changed()
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
		defer cancel()
		err := a.controller.StartServer(ctx)
		if a.ctx.Err() != nil {
			return
		}
		a.results <- actionResult{title: "Iniciar o servidor", err: err, started: err == nil}
		a.postMessage(wmResult)
	}()
}

func (a *app) startRefresh() {
	if a.ctx.Err() != nil || !a.refresh.CompareAndSwap(false, true) {
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
		if a.snapshot.Environment == "" {
			a.snapshot.Environment = a.options.Environment
		}
		a.lastErr = err
		a.loaded = true
		if !a.startedAt.IsZero() && (a.snapshot.EdgeReachable || time.Since(a.startedAt) > startingTimeout) {
			a.startedAt = time.Time{}
		}
		a.mu.Unlock()
		a.results <- actionResult{refresh: true}
		a.postMessage(wmResult)
	}()
}

func (a *app) handleResults() {
	for {
		select {
		case result := <-a.results:
			a.handleResult(result)
		default:
			a.changed()
			return
		}
	}
}

func (a *app) handleResult(result actionResult) {
	if result.refresh {
		if a.autoStart.CompareAndSwap(true, false) {
			a.mu.RLock()
			reachable := a.snapshot.EdgeReachable
			a.mu.RUnlock()
			if !reachable {
				a.startServer()
			}
		}
		return
	}
	a.mu.Lock()
	if !result.launch {
		a.busy.Store(false)
		a.activity = ""
		a.actionErr = result.err
		if result.started {
			a.startedAt = time.Now()
		}
	} else if result.err != nil {
		a.actionErr = result.err
	}
	a.mu.Unlock()
	if result.err == nil && result.exit {
		a.requestExit()
		return
	}
	if !a.flyout.visible {
		switch {
		case result.err != nil:
			a.notify(result.title+" falhou", FriendlyError(result.err), true)
		case result.done != "":
			a.notify("IA Local", result.done, false)
		}
	}
	a.startRefresh()
}

// changed pushes the current state to the icon and, if open, the flyout.
func (a *app) changed() {
	a.updateIcon()
	a.adjustRefresh()
	if a.flyout != nil {
		a.flyout.refresh()
	}
}

// activate runs the control the operator clicked. The policy is evaluated
// again from the current state, so a control that was live when the flyout
// was painted cannot act on a state that changed since.
func (a *app) activate(id zoneID) {
	view := BuildView(a.viewState())
	policy := view.Policy
	a.mu.RLock()
	selected := a.snapshot.SelectedModel
	a.mu.RUnlock()

	if id >= zoneModelBase {
		// The row is identified by what was painted under the pointer, then
		// checked against the current list, which may have changed since.
		painted := a.flyout.view.Models
		index := int(id - zoneModelBase)
		if !policy.Select || index >= len(painted) {
			return
		}
		for _, row := range view.Models {
			if row.ID == painted[index].ID && !row.Selected {
				a.selectModel(row.ID)
				return
			}
		}
		return
	}
	switch id {
	case zoneStartServer:
		if policy.StartServer {
			a.startServer()
		}
	case zoneModelAction:
		switch {
		case view.ModelAction == ModelActionLoad && policy.Load:
			a.startAction("Carregando o modelo", "Carregar o modelo", "Modelo carregado.", a.controller.LoadSelected, false)
		case view.ModelAction == ModelActionSwitch && policy.Switch:
			a.startAction("Trocando de modelo", "Trocar de modelo", "Modelo trocado.", a.controller.SwitchSelected, false)
		}
	case zoneUnload:
		if policy.Unload {
			a.startAction("Descarregando o modelo", "Descarregar o modelo", "Modelo descarregado.", a.controller.UnloadActive, false)
		}
	case zoneCodex:
		if policy.LaunchCodex {
			a.flyout.hide()
			a.startLaunch("Abrir o Codex", func(ctx context.Context) error { return a.controller.Launch(ctx, ClientCodex, selected) })
		}
	case zoneOpenCode:
		if policy.LaunchOpenCode {
			a.flyout.hide()
			a.startLaunch("Abrir o OpenCode", func(ctx context.Context) error { return a.controller.Launch(ctx, ClientOpenCode, selected) })
		}
	case zoneClaudeOpen:
		if policy.ClaudeOpen {
			a.flyout.hide()
			a.startLaunch("Abrir o Claude Desktop", a.controller.LaunchClaudeDesktop)
		}
	case zoneClaudeAnthropic:
		if policy.ClaudeAnthropic {
			a.startAction("Mudando o Claude para a Anthropic", "Mudar o Claude Desktop", "Claude Desktop usa a Anthropic.", func(ctx context.Context) error {
				return a.controller.SetClaudeMode(ctx, ClaudeModeAnthropic)
			}, false)
		}
	case zoneClaudeLocal:
		if policy.ClaudeLocal {
			a.startAction("Mudando o Claude para este servidor", "Mudar o Claude Desktop", "Claude Desktop usa este servidor.", func(ctx context.Context) error {
				return a.controller.SetClaudeMode(ctx, ClaudeModeLocal)
			}, false)
		}
	case zonePanel:
		a.flyout.hide()
		a.startLaunch("Abrir o painel", a.controller.OpenPanel)
	case zoneShutdownConfirm:
		if policy.Shutdown {
			a.startAction("Encerrando o IA Local", "Encerrar o IA Local", "", a.controller.StopServer, true)
		}
	}
}

// selectModel saves the choice at once: it is a small file write, and the
// radio should move under the pointer rather than after a background round.
func (a *app) selectModel(modelID string) {
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	err := a.controller.SelectModel(ctx, modelID)
	a.mu.Lock()
	a.actionErr = err
	if err == nil {
		a.snapshot.SelectedModel = modelID
		a.snapshot.SelectionNote = ""
	}
	a.mu.Unlock()
	a.changed()
	a.startRefresh()
}

func copyUTF16(destination []uint16, value string) {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) >= len(destination) {
		encoded = encoded[:len(destination)-1]
	}
	copy(destination, encoded)
	destination[len(encoded)] = 0
}
