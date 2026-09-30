//go:build windows

package trayui

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The flyout is the tray's only window: a borderless popup drawn with the
// browser monitor's tokens and components instead of a native menu. It opens
// above the notification area and closes when it loses activation, as the
// system flyouts do.

type zoneID int

const (
	zoneNone zoneID = iota
	zoneStartServer
	zoneModelAction
	zoneUnload
	zoneCodex
	zoneOpenCode
	zoneClaudeOpen
	zoneClaudeAnthropic
	zoneClaudeLocal
	zonePanel
	zoneShutdown
	zoneShutdownConfirm
	zoneShutdownCancel
	zoneModelBase zoneID = 1000
)

// zone is one interactive rectangle of the last paint, in device pixels.
type zone struct {
	id      zoneID
	area    rect
	enabled bool
}

const (
	flyoutWidth  = 360 // logical pixels, as CSS pixels on the page
	flyoutPad    = 16
	flyoutMargin = 12 // gap to the taskbar and to the screen edges
	timerAnimate = 2
	reopenGuard  = 300 * time.Millisecond
	rowHeight    = 48

	// Segoe Fluent Icons code points; Segoe MDL2 Assets shares them.
	glyphPlay    = ''
	glyphStop    = ''
	glyphSwitch  = ''
	glyphConsole = ''
	glyphCode    = ''
	glyphChat    = ''
	glyphOpenNew = ''
	glyphPower   = ''
	glyphError   = ''
	glyphWarning = ''
	glyphInfo    = ''

	dtLeft       = 0x0000
	dtCenter     = 0x0001
	dtRight      = 0x0002
	dtVCenter    = 0x0004
	dtWordBreak  = 0x0010
	dtSingleLine = 0x0020
	dtCalcRect   = 0x0400
	dtNoPrefix   = 0x0800
	dtEditCtrl   = 0x2000
	dtEndEllipse = 0x8000

	vkTab    = 0x09
	vkReturn = 0x0D
	vkShift  = 0x10
	vkEscape = 0x1B
	vkSpace  = 0x20
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28

	htClient        = 1
	tmeLeave        = 0x00000002
	swpShowWindow   = 0x0040
	hwndTopmost     = ^uintptr(0)
	monitorNearest  = 2
	idcArrow        = 32512
	idcHand         = 32649
	srcCopy         = 0x00CC0020
	transparentMode = 1

	dwmwaUseImmersiveDarkMode = 20
	dwmwaWindowCorner         = 33
	dwmwaBorderColor          = 34
	dwmwcpRound               = 2
)

const (
	faceText = iota
	faceDisplay
	faceIcons
)

type fontSpec struct {
	size   float32
	weight int32
	face   int
}

type fontKey struct {
	height int32
	weight int32
	face   int
}

type buttonStyle int

const (
	buttonSecondary buttonStyle = iota
	buttonPrimary
	buttonDanger
	buttonDangerFill
)

var (
	textBody   = fontSpec{13, 400, faceText}
	textStrong = fontSpec{13, 600, faceText}
	textSmall  = fontSpec{12, 400, faceText}
	textLabel  = fontSpec{12, 600, faceText}
	textChip   = fontSpec{11, 700, faceText}
	textTitle  = fontSpec{20, 700, faceDisplay}
	textBrand  = fontSpec{16, 700, faceDisplay}
	iconButton = fontSpec{16, 400, faceIcons}
	iconSmall  = fontSpec{14, 400, faceIcons}
)

type flyout struct {
	app         *app
	window      windows.Handle
	visible     bool
	hiddenAt    time.Time
	scale       float32
	palette     Palette
	faces       [3]string
	fonts       map[fontKey]uintptr
	measureDC   uintptr
	view        View
	zones       []zone
	hover       zoneID
	pressed     zoneID
	focus       zoneID
	keyboard    bool
	tracking    bool
	confirm     bool
	scroll      float32 // logical pixels scrolled off the top
	contentH    float32 // logical height of everything
	viewH       float32 // logical height of the window
	animStart   time.Time
	animating   bool
	positioning bool
}

type paintStruct struct {
	HDC       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	Window    windows.Handle
	HoverTime uint32
}

type notifyIconIdentifier struct {
	Size     uint32
	Window   windows.Handle
	ID       uint32
	GUIDItem windows.GUID
}

type textSize struct{ CX, CY int32 }

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	Icon     int32
	XHotspot uint32
	YHotspot uint32
	Mask     windows.Handle
	Color    windows.Handle
}

var (
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")
	shcore = windows.NewLazySystemDLL("shcore.dll")

	procBeginPaint           = user32.NewProc("BeginPaint")
	procEndPaint             = user32.NewProc("EndPaint")
	procGetClientRect        = user32.NewProc("GetClientRect")
	procInvalidateRect       = user32.NewProc("InvalidateRect")
	procShowWindow           = user32.NewProc("ShowWindow")
	procSetWindowPos         = user32.NewProc("SetWindowPos")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procSetFocus             = user32.NewProc("SetFocus")
	procTrackMouseEvent      = user32.NewProc("TrackMouseEvent")
	procSetCapture           = user32.NewProc("SetCapture")
	procReleaseCapture       = user32.NewProc("ReleaseCapture")
	procLoadCursorW          = user32.NewProc("LoadCursorW")
	procSetCursor            = user32.NewProc("SetCursor")
	procGetCursorPos         = user32.NewProc("GetCursorPos")
	procScreenToClient       = user32.NewProc("ScreenToClient")
	procMonitorFromPoint     = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	procGetKeyState          = user32.NewProc("GetKeyState")
	procDrawTextW            = user32.NewProc("DrawTextW")
	procShellNotifyIconRect  = shell32.NewProc("Shell_NotifyIconGetRect")
	procCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection     = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap         = gdi32.NewProc("CreateBitmap")
	procSelectObject         = gdi32.NewProc("SelectObject")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
	procDeleteDC             = gdi32.NewProc("DeleteDC")
	procBitBlt               = gdi32.NewProc("BitBlt")
	procCreateFontW          = gdi32.NewProc("CreateFontW")
	procSetTextColor         = gdi32.NewProc("SetTextColor")
	procSetBkMode            = gdi32.NewProc("SetBkMode")
	procGetTextExtentPoint32 = gdi32.NewProc("GetTextExtentPoint32W")
	procGetTextFaceW         = gdi32.NewProc("GetTextFaceW")
	procDwmSetWindowAttr     = dwmapi.NewProc("DwmSetWindowAttribute")
	procGetDpiForMonitor     = shcore.NewProc("GetDpiForMonitor")

	flyouts sync.Map
)

func newFlyout(a *app) (*flyout, error) {
	instance, _, _ := procGetModuleHandleW.Call(0)
	name, _ := windows.UTF16PtrFromString(fmt.Sprintf("CIA.LocalAI.Flyout.%d", windows.GetCurrentProcessId()))
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	class := windowClass{
		Size:       uint32(unsafe.Sizeof(windowClass{})),
		Style:      csDropShadow,
		WindowProc: windows.NewCallback(flyoutProc),
		Instance:   windows.Handle(instance),
		Cursor:     windows.Handle(cursor),
		ClassName:  name,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return nil, fmt.Errorf("register flyout window class: %w", err)
	}
	title, _ := windows.UTF16PtrFromString("IA Local")
	window, _, err := procCreateWindowExW.Call(wsExToolWindow|wsExTopmost, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
		wsPopup, 0, 0, 0, 0, uintptr(a.window), 0, instance, 0)
	if window == 0 {
		return nil, fmt.Errorf("create flyout window: %w", err)
	}
	f := newRenderer()
	f.app = a
	f.window = windows.Handle(window)
	flyouts.Store(f.window, f)
	return f, nil
}

// newRenderer returns a flyout that can lay out and draw but has no window,
// which is all the offscreen rendering tests need.
func newRenderer() *flyout {
	measureDC, _, _ := procCreateCompatibleDC.Call(0)
	f := &flyout{
		scale:     1,
		palette:   LightPalette,
		fonts:     make(map[fontKey]uintptr),
		measureDC: measureDC,
	}
	f.faces[faceText] = resolveFace(measureDC, "Segoe UI Variable Text", "Segoe UI")
	f.faces[faceDisplay] = resolveFace(measureDC, "Segoe UI Variable Display", "Segoe UI")
	f.faces[faceIcons] = resolveFace(measureDC, "Segoe Fluent Icons", "Segoe MDL2 Assets")
	return f
}

func (f *flyout) destroy() {
	flyouts.Delete(f.window)
	if f.window != 0 {
		_, _, _ = procDestroyWindow.Call(uintptr(f.window))
		f.window = 0
	}
	f.releaseFonts()
	if f.measureDC != 0 {
		_, _, _ = procDeleteDC.Call(f.measureDC)
		f.measureDC = 0
	}
}

// resolveFace returns preferred when GDI knows the family and fallback when
// it would substitute another one: Segoe UI Variable and Fluent Icons ship
// with Windows 11, Segoe UI and MDL2 Assets with Windows 10.
func resolveFace(dc uintptr, preferred, fallback string) string {
	name, _ := windows.UTF16PtrFromString(preferred)
	font, _, _ := procCreateFontW.Call(^uintptr(15), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(name)))
	if font == 0 {
		return fallback
	}
	defer procDeleteObject.Call(font)
	old, _, _ := procSelectObject.Call(dc, font)
	var buffer [64]uint16
	_, _, _ = procGetTextFaceW.Call(dc, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	_, _, _ = procSelectObject.Call(dc, old)
	if strings.EqualFold(windows.UTF16ToString(buffer[:]), preferred) {
		return preferred
	}
	return fallback
}

func (f *flyout) releaseFonts() {
	for key, font := range f.fonts {
		_, _, _ = procDeleteObject.Call(font)
		delete(f.fonts, key)
	}
}

func (f *flyout) setScale(dpi uint32) {
	if dpi == 0 {
		dpi = 96
	}
	scale := float32(dpi) / 96
	if scale != f.scale {
		f.scale = scale
		f.releaseFonts()
	}
}

func (f *flyout) font(spec fontSpec) uintptr {
	key := fontKey{height: -int32(math.Round(float64(spec.size * f.scale))), weight: spec.weight, face: spec.face}
	if font, ok := f.fonts[key]; ok {
		return font
	}
	name, _ := windows.UTF16PtrFromString(f.faces[spec.face])
	const defaultCharset, clearTypeQuality = 1, 5
	font, _, _ := procCreateFontW.Call(uintptr(key.height), 0, 0, 0, uintptr(spec.weight), 0, 0, 0,
		defaultCharset, 0, 0, clearTypeQuality, 0, uintptr(unsafe.Pointer(name)))
	f.fonts[key] = font
	return font
}

// measure returns a single line's width in logical pixels.
func (f *flyout) measure(text string, spec fontSpec) float32 {
	if text == "" {
		return 0
	}
	encoded := windows.StringToUTF16(text)
	old, _, _ := procSelectObject.Call(f.measureDC, f.font(spec))
	var size textSize
	_, _, _ = procGetTextExtentPoint32.Call(f.measureDC, uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)-1), uintptr(unsafe.Pointer(&size)))
	_, _, _ = procSelectObject.Call(f.measureDC, old)
	return float32(math.Ceil(float64(float32(size.CX) / f.scale)))
}

// measureWrapped returns the logical height of text wrapped to width, at
// most maxLines lines tall.
func (f *flyout) measureWrapped(text string, spec fontSpec, width float32, maxLines int) float32 {
	line := f.lineHeight(spec)
	if text == "" {
		return 0
	}
	encoded, _ := windows.UTF16PtrFromString(text)
	old, _, _ := procSelectObject.Call(f.measureDC, f.font(spec))
	area := rect{Right: int32(width * f.scale)}
	_, _, _ = procDrawTextW.Call(f.measureDC, uintptr(unsafe.Pointer(encoded)), ^uintptr(0), uintptr(unsafe.Pointer(&area)),
		dtCalcRect|dtWordBreak|dtNoPrefix|dtEditCtrl)
	_, _, _ = procSelectObject.Call(f.measureDC, old)
	height := float32(math.Ceil(float64(float32(area.Bottom) / f.scale)))
	if limit := line * float32(maxLines); height > limit {
		return limit
	}
	return height
}

func (f *flyout) lineHeight(spec fontSpec) float32 {
	encoded := windows.StringToUTF16("Ág")
	old, _, _ := procSelectObject.Call(f.measureDC, f.font(spec))
	var size textSize
	_, _, _ = procGetTextExtentPoint32.Call(f.measureDC, uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)-1), uintptr(unsafe.Pointer(&size)))
	_, _, _ = procSelectObject.Call(f.measureDC, old)
	return float32(math.Ceil(float64(float32(size.CY) / f.scale)))
}

func (f *flyout) applyTheme() {
	dark := systemUsesDarkTheme()
	f.palette = LightPalette
	if dark {
		f.palette = DarkPalette
	}
	setWindowAttribute := func(attribute uint32, value uint32) {
		_, _, _ = procDwmSetWindowAttr.Call(uintptr(f.window), uintptr(attribute), uintptr(unsafe.Pointer(&value)), 4)
	}
	darkValue := uint32(0)
	if dark {
		darkValue = 1
	}
	setWindowAttribute(dwmwaUseImmersiveDarkMode, darkValue)
	setWindowAttribute(dwmwaWindowCorner, dwmwcpRound)
	setWindowAttribute(dwmwaBorderColor, f.palette.Line.COLORREF())
}

// systemUsesDarkTheme reads the "app mode" choice in Settings > Personalization
// > Colors, which is what the page's prefers-color-scheme follows too.
func systemUsesDarkTheme() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("AppsUseLightTheme")
	return err == nil && value == 0
}

func (f *flyout) toggle() {
	if f.visible {
		f.hide()
		return
	}
	// A click on the icon while the flyout is open first deactivates it, and
	// then arrives here: without the guard it would reopen at once.
	if time.Since(f.hiddenAt) < reopenGuard {
		return
	}
	f.show()
}

func (f *flyout) show() {
	f.confirm = false
	f.scroll = 0
	f.hover, f.pressed, f.focus = zoneNone, zoneNone, zoneNone
	f.keyboard = false
	f.view = BuildView(f.app.viewState())
	f.applyTheme()
	f.position()
	f.visible = true
	_, _, _ = procSetForegroundWindow.Call(uintptr(f.window))
	_, _, _ = procSetFocus.Call(uintptr(f.window))
	f.updateAnimation()
	f.app.adjustRefresh()
	f.app.startRefresh()
}

func (f *flyout) hide() {
	if !f.visible {
		return
	}
	f.visible = false
	f.hiddenAt = time.Now()
	f.confirm = false
	_, _, _ = procShowWindow.Call(uintptr(f.window), swHide)
	f.updateAnimation()
	f.app.adjustRefresh()
}

// refresh repaints after a state change, resizing when the content grew or
// shrank.
func (f *flyout) refresh() {
	if !f.visible {
		return
	}
	f.view = BuildView(f.app.viewState())
	if height := f.measureHeight(); height != f.contentH {
		f.position()
	} else {
		f.invalidate()
	}
	f.updateAnimation()
}

func (f *flyout) invalidate() {
	_, _, _ = procInvalidateRect.Call(uintptr(f.window), 0, 0)
}

func (f *flyout) updateAnimation() {
	want := f.visible && f.view.Progress
	switch {
	case want && !f.animating:
		f.animating = true
		f.animStart = time.Now()
		_, _, _ = procSetTimer.Call(uintptr(f.window), timerAnimate, 33, 0)
	case !want && f.animating:
		f.animating = false
		_, _, _ = procKillTimer.Call(uintptr(f.window), timerAnimate)
	}
}

func (f *flyout) measureHeight() float32 {
	scroll := f.scroll
	f.scroll = 0
	height := f.layout(&painter{f: f})
	f.scroll = scroll
	return height
}

// position sizes the flyout for the monitor that shows the icon and places it
// beside the taskbar, wherever the taskbar is.
func (f *flyout) position() {
	anchor, ok := f.app.iconRect()
	if !ok {
		var cursor point
		_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
		anchor = rect{cursor.X, cursor.Y, cursor.X, cursor.Y}
	}
	centerX, centerY := (anchor.Left+anchor.Right)/2, (anchor.Top+anchor.Bottom)/2
	monitor, _, _ := procMonitorFromPoint.Call(uintptr(uint32(centerX))|uintptr(uint32(centerY))<<32, monitorNearest)
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	_, _, _ = procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	var dpiX, dpiY uint32
	if result, _, _ := procGetDpiForMonitor.Call(monitor, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))); result != 0 {
		system, _, _ := procGetDpiForSystem.Call()
		dpiX = uint32(system)
	}
	f.setScale(dpiX)

	f.contentH = f.measureHeight()
	work, screen := info.Work, info.Monitor
	margin := int32(math.Round(float64(flyoutMargin * f.scale)))
	width := int32(math.Round(float64(flyoutWidth * f.scale)))
	height := int32(math.Ceil(float64(f.contentH * f.scale)))
	if limit := work.Bottom - work.Top - 2*margin; height > limit {
		height = limit
	}
	f.viewH = float32(height) / f.scale
	f.clampScroll()

	x := centerX - width/2
	y := work.Bottom - margin - height
	switch {
	case work.Top > screen.Top:
		y = work.Top + margin
	case work.Left > screen.Left:
		x, y = work.Left+margin, centerY-height/2
	case work.Right < screen.Right:
		x, y = work.Right-margin-width, centerY-height/2
	case work.Bottom == screen.Bottom && anchor.Top < (screen.Top+screen.Bottom)/2:
		// An auto-hidden taskbar leaves the work area whole; follow the icon.
		y = anchor.Bottom + margin
	case work.Bottom == screen.Bottom:
		y = anchor.Top - margin - height
	}
	x = clamp32(x, work.Left+margin, work.Right-margin-width)
	y = clamp32(y, work.Top+margin, work.Bottom-margin-height)

	f.positioning = true
	_, _, _ = procSetWindowPos.Call(uintptr(f.window), hwndTopmost, uintptr(x), uintptr(y), uintptr(width), uintptr(height), swpShowWindow)
	f.positioning = false
	f.invalidate()
}

func clamp32(value, low, high int32) int32 {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func (f *flyout) clampScroll() {
	limit := f.contentH - f.viewH
	if limit < 0 {
		limit = 0
	}
	if f.scroll > limit {
		f.scroll = limit
	}
	if f.scroll < 0 {
		f.scroll = 0
	}
}

func (a *app) iconRect() (rect, bool) {
	identifier := notifyIconIdentifier{Size: uint32(unsafe.Sizeof(notifyIconIdentifier{})), Window: a.window, ID: 1}
	var area rect
	result, _, _ := procShellNotifyIconRect.Call(uintptr(unsafe.Pointer(&identifier)), uintptr(unsafe.Pointer(&area)))
	return area, result == 0 && area.Right > area.Left
}

func flyoutProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, ok := flyouts.Load(windows.Handle(window))
	if !ok {
		result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
		return result
	}
	f := value.(*flyout)
	x, y := int32(int16(lParam&0xffff)), int32(int16((lParam>>16)&0xffff))
	switch message {
	case wmPaint:
		f.paint()
		return 0
	case wmEraseBkgnd:
		return 1
	case wmActivate:
		if wParam&0xffff == 0 {
			f.hide()
		}
		return 0
	case wmMouseMove:
		if !f.tracking {
			event := trackMouseEvent{Size: uint32(unsafe.Sizeof(trackMouseEvent{})), Flags: tmeLeave, Window: f.window}
			_, _, _ = procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&event)))
			f.tracking = true
		}
		f.setHover(f.enabledAt(x, y))
		return 0
	case wmMouseLeave:
		f.tracking = false
		f.setHover(zoneNone)
		return 0
	case wmLButtonDown:
		f.keyboard = false
		if id := f.enabledAt(x, y); id != zoneNone {
			f.pressed = id
			_, _, _ = procSetCapture.Call(window)
		}
		f.invalidate()
		return 0
	case wmLButtonUp:
		_, _, _ = procReleaseCapture.Call()
		pressed := f.pressed
		f.pressed = zoneNone
		f.invalidate()
		if pressed != zoneNone && f.enabledAt(x, y) == pressed {
			f.activate(pressed)
		}
		return 0
	case wmSetCursor:
		if lParam&0xffff == htClient {
			var cursor point
			_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
			_, _, _ = procScreenToClient.Call(window, uintptr(unsafe.Pointer(&cursor)))
			shape := uintptr(idcArrow)
			if f.enabledAt(cursor.X, cursor.Y) != zoneNone {
				shape = idcHand
			}
			handle, _, _ := procLoadCursorW.Call(0, shape)
			_, _, _ = procSetCursor.Call(handle)
			return 1
		}
	case wmMouseWheel:
		delta := float32(int16((wParam >> 16) & 0xffff))
		f.scroll -= delta / 120 * rowHeight
		f.clampScroll()
		f.invalidate()
		return 0
	case wmKeyDown:
		f.key(wParam)
		return 0
	case wmTimer:
		if wParam == timerAnimate {
			f.invalidate()
		}
		return 0
	case wmDPIChanged:
		// The flyout was sized for the monitor it opens on; follow a move.
		if f.visible && !f.positioning {
			f.position()
		}
		return 0
	case wmSettingChange:
		if f.visible {
			f.applyTheme()
			f.invalidate()
		}
		return 0
	case wmClose:
		f.hide()
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return result
}

func (f *flyout) enabledAt(x, y int32) zoneID {
	for index := len(f.zones) - 1; index >= 0; index-- {
		z := f.zones[index]
		if x >= z.area.Left && x < z.area.Right && y >= z.area.Top && y < z.area.Bottom {
			if z.enabled {
				return z.id
			}
			return zoneNone
		}
	}
	return zoneNone
}

func (f *flyout) setHover(id zoneID) {
	if id != f.hover {
		f.hover = id
		f.invalidate()
	}
}

func (f *flyout) key(virtualKey uintptr) {
	switch virtualKey {
	case vkEscape:
		if f.confirm {
			f.confirm = false
			f.focus = zoneShutdown
			f.refresh()
			return
		}
		f.hide()
	case vkTab:
		shift, _, _ := procGetKeyState.Call(vkShift)
		direction := 1
		if int16(shift) < 0 {
			direction = -1
		}
		f.moveFocus(direction)
	case vkDown, vkRight:
		f.moveFocus(1)
	case vkUp, vkLeft:
		f.moveFocus(-1)
	case vkReturn, vkSpace:
		for _, z := range f.zones {
			if z.id == f.focus && z.enabled {
				f.activate(z.id)
				return
			}
		}
	}
}

// moveFocus walks the live controls in reading order, the order they were
// painted in, and scrolls the focused one into view.
func (f *flyout) moveFocus(direction int) {
	f.keyboard = true
	live := make([]zone, 0, len(f.zones))
	for _, z := range f.zones {
		if z.enabled {
			live = append(live, z)
		}
	}
	if len(live) == 0 {
		f.invalidate()
		return
	}
	next := 0
	if direction < 0 {
		next = len(live) - 1
	}
	for index, z := range live {
		if z.id == f.focus {
			next = (index + direction + len(live)) % len(live)
			break
		}
	}
	f.focus = live[next].id
	area := live[next].area
	var client rect
	_, _, _ = procGetClientRect.Call(uintptr(f.window), uintptr(unsafe.Pointer(&client)))
	if area.Top < 0 {
		f.scroll += float32(area.Top)/f.scale - flyoutPad
	} else if area.Bottom > client.Bottom {
		f.scroll += float32(area.Bottom-client.Bottom)/f.scale + flyoutPad
	}
	f.clampScroll()
	f.invalidate()
}

func (f *flyout) activate(id zoneID) {
	switch id {
	case zoneShutdown:
		f.confirm = true
		// The safe answer takes the focus, as Cancel does in the monitor's
		// native dialogs.
		f.focus = zoneShutdownCancel
		f.refresh()
		return
	case zoneShutdownCancel:
		f.confirm = false
		f.focus = zoneShutdown
		f.refresh()
		return
	}
	f.app.activate(id)
	if id == zoneShutdownConfirm {
		f.confirm = false
	}
}

func (f *flyout) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(f.window), uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(uintptr(f.window), uintptr(unsafe.Pointer(&ps)))
	var client rect
	_, _, _ = procGetClientRect.Call(uintptr(f.window), uintptr(unsafe.Pointer(&client)))
	width, height := client.Right-client.Left, client.Bottom-client.Top
	if hdc == 0 || width <= 0 || height <= 0 {
		return
	}
	memory, _, _ := procCreateCompatibleDC.Call(hdc)
	bitmap, _, _ := procCreateCompatibleBmp.Call(hdc, uintptr(width), uintptr(height))
	old, _, _ := procSelectObject.Call(memory, bitmap)
	defer func() {
		_, _, _ = procSelectObject.Call(memory, old)
		_, _, _ = procDeleteObject.Call(bitmap)
		_, _, _ = procDeleteDC.Call(memory)
	}()
	f.draw(memory, width, height)
	_, _, _ = procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(height), memory, 0, 0, srcCopy)
}

// draw renders the whole flyout into a device context of the window's size.
func (f *flyout) draw(dc uintptr, width, height int32) {
	c, err := canvasFromHDC(dc)
	if err != nil {
		return
	}
	defer c.close()
	c.clear(f.palette.Bg)
	p := &painter{f: f, c: c}
	f.contentH = f.layout(p)
	f.zones = p.zones
	if f.contentH > f.viewH+0.5 {
		// A thin indicator, like an overlay scrollbar, when the screen is too
		// short for the whole flyout.
		track := float32(height)
		thumb := track * f.viewH / f.contentH
		top := (track - thumb) * f.scroll / (f.contentH - f.viewH)
		c.fillRound(float32(width)-5*f.scale, top+2*f.scale, 3*f.scale, thumb-4*f.scale, 2*f.scale, f.palette.InkMuted.WithAlpha(110))
	}
}

// painter takes logical coordinates, scales and scrolls them, and records the
// interactive zones. Without a canvas it only lays out, which is how the
// flyout measures its height before it is shown.
type painter struct {
	f     *flyout
	c     *canvas
	zones []zone
}

func (p *painter) box(x, y, w, h float32) (float32, float32, float32, float32) {
	s := p.f.scale
	left := float32(math.Round(float64(x * s)))
	top := float32(math.Round(float64((y - p.f.scroll) * s)))
	right := float32(math.Round(float64((x + w) * s)))
	bottom := float32(math.Round(float64((y + h - p.f.scroll) * s)))
	return left, top, right - left, bottom - top
}

func (p *painter) hairline() float32 {
	return float32(math.Max(1, math.Floor(float64(p.f.scale))))
}

func (p *painter) fill(x, y, w, h, r float32, color Color) {
	if p.c == nil {
		return
	}
	left, top, width, height := p.box(x, y, w, h)
	p.c.fillRound(left, top, width, height, r*p.f.scale, color)
}

func (p *painter) stroke(x, y, w, h, r float32, color Color) {
	p.strokeWidth(x, y, w, h, r, 0, color)
}

// strokeWidth draws a border inside the box; a zero width is one device
// pixel, as a 1px CSS border is.
func (p *painter) strokeWidth(x, y, w, h, r, width float32, color Color) {
	if p.c == nil {
		return
	}
	left, top, boxW, boxH := p.box(x, y, w, h)
	stroke := p.hairline()
	if width > 0 {
		stroke = float32(math.Round(float64(width * p.f.scale)))
	}
	p.c.strokeRound(left, top, boxW, boxH, r*p.f.scale, stroke, color)
}

func (p *painter) circle(cx, cy, r float32, color Color) {
	if p.c == nil {
		return
	}
	s := p.f.scale
	p.c.fillCircle(cx*s, (cy-p.f.scroll)*s, r*s, color)
}

func (p *painter) ring(cx, cy, r, width float32, color Color) {
	if p.c == nil {
		return
	}
	s := p.f.scale
	p.c.strokeCircle(cx*s, (cy-p.f.scroll)*s, r*s, float32(math.Max(1, float64(width*s))), color)
}

// rule is a horizontal hairline.
func (p *painter) rule(x, y, w float32, color Color) {
	if p.c == nil {
		return
	}
	left, top, width, _ := p.box(x, y, w, 0)
	p.c.fillRect(left, top, width, p.hairline(), color)
}

func (p *painter) mark(x, y, size float32, tile, glyph Color) {
	if p.c == nil {
		return
	}
	left, top, width, _ := p.box(x, y, size, size)
	p.c.drawMark(left, top, width, tile, glyph)
}

func (p *painter) text(value string, x, y, w, h float32, spec fontSpec, color Color, flags uint32) {
	if p.c == nil || value == "" || w <= 0 {
		return
	}
	left, top, width, height := p.box(x, y, w, h)
	area := rect{int32(left), int32(top), int32(left + width), int32(top + height)}
	font := p.f.font(spec)
	encoded, _ := windows.UTF16PtrFromString(value)
	p.c.withDC(func(hdc uintptr) {
		old, _, _ := procSelectObject.Call(hdc, font)
		_, _, _ = procSetBkMode.Call(hdc, transparentMode)
		_, _, _ = procSetTextColor.Call(hdc, uintptr(color.COLORREF()))
		_, _, _ = procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(encoded)), ^uintptr(0), uintptr(unsafe.Pointer(&area)), uintptr(flags|dtNoPrefix))
		_, _, _ = procSelectObject.Call(hdc, old)
	})
}

func (p *painter) glyph(symbol rune, x, y, w, h float32, spec fontSpec, color Color) {
	p.text(string(symbol), x, y, w, h, spec, color, dtCenter|dtVCenter|dtSingleLine)
}

func (p *painter) clip(x, y, w, h, r float32) {
	if p.c == nil {
		return
	}
	left, top, width, height := p.box(x, y, w, h)
	p.c.clipRound(left, top, width, height, r*p.f.scale)
}

func (p *painter) unclip() {
	if p.c != nil {
		p.c.resetClip()
	}
}

func (p *painter) zone(id zoneID, x, y, w, h float32, enabled bool) {
	left, top, width, height := p.box(x, y, w, h)
	p.zones = append(p.zones, zone{id: id, area: rect{int32(left), int32(top), int32(left + width), int32(top + height)}, enabled: enabled})
}

// focusRing draws the keyboard focus ring the page draws with box-shadow.
func (f *flyout) focusRing(p *painter, id zoneID, x, y, w, h, r float32) {
	if f.keyboard && f.focus == id {
		p.strokeWidth(x-3, y-3, w+6, h+6, r+3, 3, f.palette.Focus)
	}
}

func (f *flyout) card(p *painter, x, y, w, h float32) {
	p.fill(x, y, w, h, 14, f.palette.Surface)
	p.stroke(x, y, w, h, 14, f.palette.Line)
}

// fade is the page's disabled look (opacity .45) over a known background.
func fade(background, color Color) Color {
	return Blend(background, color.WithAlpha(115))
}

func (f *flyout) button(p *painter, id zoneID, x, y, w, h float32, label string, symbol rune, style buttonStyle, enabled bool, background Color) {
	pal := f.palette
	var fill, border, ink Color
	switch style {
	case buttonPrimary:
		fill, border, ink = pal.AccentFill, pal.AccentFill, pal.OnAccentFill
	case buttonDanger:
		fill, border, ink = pal.Surface, Blend(pal.Line, pal.Danger.Solid.WithAlpha(140)), pal.Danger.Ink
	case buttonDangerFill:
		fill, border, ink = pal.Danger.Solid, pal.Danger.Solid, 0xffffffff
	default:
		fill, border, ink = pal.Surface, pal.Line, pal.Ink
	}
	if enabled {
		hot, down := f.hover == id, f.pressed == id && f.hover == id
		switch style {
		case buttonPrimary, buttonDangerFill:
			if hot {
				fill = Blend(fill, pal.Ink.WithAlpha(30))
			}
			if down {
				fill = Blend(fill, pal.Ink.WithAlpha(30))
			}
			border = fill
		case buttonDanger:
			if hot {
				fill = Blend(pal.Surface, pal.Danger.Tint)
			}
			if down {
				fill = Blend(fill, pal.Danger.Tint)
			}
		default:
			if hot {
				fill = pal.Surface2
			}
			if down {
				fill = Blend(pal.Surface2, pal.Ink.WithAlpha(14))
			}
		}
	} else {
		fill, border, ink = fade(background, fill), fade(background, border), fade(background, ink)
	}
	p.fill(x, y, w, h, 10, fill)
	if border != fill {
		p.stroke(x, y, w, h, 10, border)
	}
	symbolW := float32(0)
	if symbol != 0 {
		symbolW = 16 + 6
	}
	labelW := f.measure(label, textStrong)
	if room := w - 16 - symbolW; labelW > room {
		labelW = room
	}
	left := x + (w-labelW-symbolW)/2
	if symbol != 0 {
		p.glyph(symbol, left, y, 16, h, iconButton, ink)
		left += symbolW
	}
	p.text(label, left, y, labelW+1, h, textStrong, ink, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
	f.focusRing(p, id, x, y, w, h, 10)
	p.zone(id, x, y, w, h, enabled)
}

func (f *flyout) noticeHeight(notice Notice, width float32) float32 {
	textH := f.measureWrapped(notice.Text, textSmall, width-48, 3)
	return float32(math.Max(float64(textH), 16)) + 16
}

func (f *flyout) notice(p *painter, notice Notice, x, y, w float32) float32 {
	pal := f.palette
	tone := pal.Tone(notice.Tone)
	h := f.noticeHeight(notice, w)
	p.fill(x, y, w, h, 10, Blend(pal.Surface, tone.Tint))
	symbol := rune(glyphInfo)
	switch notice.Tone {
	case ToneDanger:
		symbol = glyphError
	case ToneWarn:
		symbol = glyphWarning
	}
	p.glyph(symbol, x+10, y+8, 16, 16, iconSmall, tone.Ink)
	p.text(notice.Text, x+34, y+8, w-48, h-16, textSmall, tone.Ink, dtLeft|dtWordBreak|dtEditCtrl|dtEndEllipse)
	return h
}

// layout lays out and, with a canvas, draws the whole flyout. It returns the
// content height in logical pixels.
func (f *flyout) layout(p *painter) float32 {
	v := f.view
	pal := f.palette
	x0 := float32(flyoutPad)
	iw := float32(flyoutWidth - 2*flyoutPad)
	y := float32(flyoutPad)

	// Header: the brand as the page shows it, and the status pill.
	p.mark(x0, y+2, 28, Blend(pal.Bg, pal.Accent.Tint), pal.Accent.Ink)
	nameX := x0 + 28 + 10
	nameW := f.measure("IA Local", textBrand)
	p.text("IA Local", nameX, y, nameW+2, 32, textBrand, pal.Ink, dtLeft|dtVCenter|dtSingleLine)
	tone := pal.Tone(v.Tone)
	pillW := f.measure(v.Pill, textLabel) + 40
	pillX := x0 + iw - pillW
	if v.Environment != "" {
		chipW := f.measure(v.Environment, textChip) + 16
		chipX := nameX + nameW + 8
		if chipX+chipW < pillX-8 {
			p.stroke(chipX, y+6, chipW, 20, 10, pal.Line)
			p.text(v.Environment, chipX, y+6, chipW, 20, textChip, pal.InkMuted, dtCenter|dtVCenter|dtSingleLine)
		}
	}
	p.fill(pillX, y+2, pillW, 28, 14, pal.Surface)
	p.stroke(pillX, y+2, pillW, 28, 14, pal.Line)
	p.circle(pillX+16, y+16, 8, Blend(pal.Surface, tone.Tint))
	p.circle(pillX+16, y+16, 4, tone.Solid)
	p.text(v.Pill, pillX+28, y+2, pillW-40, 28, textLabel, tone.Ink, dtLeft|dtVCenter|dtSingleLine)
	y += 32 + 12

	// State card.
	innerX, innerW := x0+16, iw-32
	subH := f.measureWrapped(v.Subtitle, textBody, innerW, 2)
	cardH := 16 + 28 + 2 + subH
	if len(v.Chips) > 0 {
		cardH += 10 + 24
	}
	if v.Progress {
		cardH += 14 + 6
	}
	for _, notice := range v.Notices {
		cardH += 10 + f.noticeHeight(notice, innerW)
	}
	if v.Policy.StartServer {
		cardH += 14 + 36
	}
	cardH += 16
	f.card(p, x0, y, iw, cardH)
	cy := y + 16
	titleColor := pal.Ink
	if v.Tone == ToneDanger {
		titleColor = pal.Danger.Ink
	}
	p.text(v.Title, innerX, cy, innerW, 28, textTitle, titleColor, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
	cy += 28 + 2
	p.text(v.Subtitle, innerX, cy, innerW, subH, textBody, pal.InkMuted, dtLeft|dtWordBreak|dtEditCtrl|dtEndEllipse)
	cy += subH
	if len(v.Chips) > 0 {
		cy += 10
		chipX := innerX
		for _, chip := range v.Chips {
			chipTone := pal.Tone(chip.Tone)
			chipW := f.measure(chip.Text, textLabel) + 20
			p.fill(chipX, cy, chipW, 24, 12, Blend(pal.Surface, chipTone.Tint))
			p.text(chip.Text, chipX, cy, chipW, 24, textLabel, chipTone.Ink, dtCenter|dtVCenter|dtSingleLine)
			chipX += chipW + 6
		}
		cy += 24
	}
	if v.Progress {
		cy += 14
		p.fill(innerX, cy, innerW, 6, 3, pal.Surface2)
		p.clip(innerX, cy, innerW, 6, 3)
		phase := float32(time.Since(f.animStart)%(1400*time.Millisecond)) / float32(1400*time.Millisecond)
		eased := 1 - float32(math.Pow(float64(1-phase), 2))
		barW := innerW * 0.38
		p.fill(innerX-barW+eased*(innerW+barW), cy, barW, 6, 3, pal.Info.Solid)
		p.unclip()
		cy += 6
	}
	for _, notice := range v.Notices {
		cy += 10
		cy += f.notice(p, notice, innerX, cy, innerW)
	}
	if v.Policy.StartServer {
		cy += 14
		f.button(p, zoneStartServer, innerX, cy, innerW, 36, "Iniciar o servidor", glyphPlay, buttonPrimary, true, pal.Surface)
	}
	y += cardH + 20

	// Models.
	p.text("Modelos", x0, y, iw/2, 18, textStrong, pal.InkSoft, dtLeft|dtVCenter|dtSingleLine)
	count := "nenhum publicado"
	if len(v.Models) == 1 {
		count = "1 disponível"
	} else if len(v.Models) > 1 {
		count = fmt.Sprintf("%d disponíveis", len(v.Models))
	}
	p.text(count, x0+iw/2, y, iw/2, 18, textSmall, pal.InkMuted, dtRight|dtVCenter|dtSingleLine)
	y += 18 + 8
	listH := float32(rowHeight * len(v.Models))
	if len(v.Models) == 0 {
		listH = rowHeight
	}
	f.card(p, x0, y, iw, listH)
	if len(v.Models) == 0 {
		p.text("O servidor não publica nenhum modelo agora.", x0, y, iw, listH, textBody, pal.InkMuted, dtCenter|dtVCenter|dtSingleLine)
	}
	for index, row := range v.Models {
		id := zoneModelBase + zoneID(index)
		rowY := y + float32(index*rowHeight)
		enabled := v.Policy.Select && !row.Selected
		if index > 0 {
			p.rule(x0+14, rowY, iw-28, pal.LineSoft)
		}
		if enabled && f.hover == id {
			p.fill(x0+4, rowY+4, iw-8, rowHeight-8, 10, pal.Surface2)
		}
		radioX, radioY := x0+14+8, rowY+rowHeight/2
		if row.Selected {
			p.circle(radioX, radioY, 8, pal.AccentFill)
			p.circle(radioX, radioY, 3, pal.OnAccentFill)
		} else {
			p.ring(radioX, radioY, 8, 1.5, Blend(pal.Surface, pal.InkMuted.WithAlpha(150)))
		}
		textX := x0 + 14 + 16 + 12
		badgeW := float32(0)
		if row.Loaded {
			badgeW = f.measure("carregado", textLabel) + 16
			badgeX := x0 + iw - 14 - badgeW
			p.fill(badgeX, radioY-11, badgeW, 22, 11, Blend(pal.Surface, pal.Accent.Tint))
			p.text("carregado", badgeX, radioY-11, badgeW, 22, textLabel, pal.Accent.Ink, dtCenter|dtVCenter|dtSingleLine)
			badgeW += 8
		}
		textW := iw - (textX - x0) - 14 - badgeW
		if row.Detail == "" {
			p.text(row.Name, textX, rowY, textW, rowHeight, textStrong, pal.Ink, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
		} else {
			p.text(row.Name, textX, rowY+6, textW, 19, textStrong, pal.Ink, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
			p.text(row.Detail, textX, rowY+25, textW, 17, textSmall, pal.InkMuted, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
		}
		f.focusRing(p, id, x0+4, rowY+4, iw-8, rowHeight-8, 10)
		p.zone(id, x0, rowY, iw, rowHeight, enabled)
	}
	y += listH + 12
	var actions []rowButton
	switch v.ModelAction {
	case ModelActionLoad:
		actions = append(actions, rowButton{zoneModelAction, "Carregar", glyphPlay, buttonPrimary, v.Policy.Load})
	case ModelActionSwitch:
		actions = append(actions, rowButton{zoneModelAction, "Trocar de modelo", glyphSwitch, buttonPrimary, v.Policy.Switch})
	}
	if v.ShowUnload {
		actions = append(actions, rowButton{zoneUnload, "Descarregar", glyphStop, buttonDanger, v.Policy.Unload})
	}
	if len(actions) > 0 {
		f.buttonRow(p, x0, y, iw, actions)
		y += 36
	}
	y += 20

	// Clients.
	p.text("Clientes", x0, y, iw/2, 18, textStrong, pal.InkSoft, dtLeft|dtVCenter|dtSingleLine)
	p.text("com o modelo selecionado", x0+iw/3, y, iw*2/3, 18, textSmall, pal.InkMuted, dtRight|dtVCenter|dtSingleLine)
	y += 18 + 8
	f.buttonRow(p, x0, y, iw, []rowButton{
		{zoneCodex, "Codex", glyphConsole, buttonSecondary, v.Policy.LaunchCodex},
		{zoneOpenCode, "OpenCode", glyphCode, buttonSecondary, v.Policy.LaunchOpenCode},
		{zoneClaudeOpen, "Claude", glyphChat, buttonSecondary, v.Policy.ClaudeOpen},
	})
	y += 36 + 12

	segmentW := float32(176)
	p.text("Claude Desktop", x0, y, iw-segmentW-12, 36, textStrong, pal.Ink, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
	f.segmented(p, x0+iw-segmentW, y, segmentW, v)
	y += 36 + 4
	p.text(v.ClaudeNote, x0, y, iw, 17, textSmall, pal.InkMuted, dtLeft|dtVCenter|dtSingleLine|dtEndEllipse)
	y += 17 + 16

	// Footer.
	p.rule(0, y, flyoutWidth, pal.LineSoft)
	y += 12
	if !f.confirm {
		shutdownW := float32(118)
		f.button(p, zonePanel, x0, y, iw-shutdownW-8, 36, "Abrir painel", glyphOpenNew, buttonSecondary, v.Policy.OpenPanel, pal.Bg)
		f.button(p, zoneShutdown, x0+iw-shutdownW, y, shutdownW, 36, "Encerrar", glyphPower, buttonDanger, v.Policy.Shutdown, pal.Bg)
		y += 36
	} else {
		p.text("Encerrar o IA Local?", x0, y, iw, 20, textStrong, pal.Ink, dtLeft|dtVCenter|dtSingleLine)
		y += 20 + 2
		detail := "O servidor, o painel e este ícone fecham. Abra o IA Local de novo para voltar."
		detailColor := pal.InkMuted
		if v.InFlight > 0 {
			detail = plural(v.InFlight, "pedido em andamento será interrompido.", "pedidos em andamento serão interrompidos.") + " " + detail
			detailColor = pal.Danger.Ink
		}
		detailH := f.measureWrapped(detail, textSmall, iw, 3)
		p.text(detail, x0, y, iw, detailH, textSmall, detailColor, dtLeft|dtWordBreak|dtEditCtrl|dtEndEllipse)
		y += detailH + 12
		half := (iw - 8) / 2
		f.button(p, zoneShutdownCancel, x0, y, half, 36, "Cancelar", 0, buttonSecondary, true, pal.Bg)
		f.button(p, zoneShutdownConfirm, x0+half+8, y, half, 36, "Encerrar", glyphPower, buttonDangerFill, v.Policy.Shutdown, pal.Bg)
		y += 36
	}
	return y + flyoutPad
}

type rowButton struct {
	id      zoneID
	label   string
	symbol  rune
	style   buttonStyle
	enabled bool
}

// buttonRow lays buttons side by side across width. Each gets its content's
// width and an equal share of what is left, so the longest label is never the
// one an equal split would cut.
func (f *flyout) buttonRow(p *painter, x, y, width float32, buttons []rowButton) {
	gaps := 8 * float32(len(buttons)-1)
	widths := make([]float32, len(buttons))
	spare := width - gaps
	for index, button := range buttons {
		widths[index] = f.measure(button.label, textStrong) + 16
		if button.symbol != 0 {
			widths[index] += 16 + 6
		}
		spare -= widths[index]
	}
	for index, button := range buttons {
		buttonW := widths[index] + spare/float32(len(buttons))
		if spare < 0 {
			buttonW = (width - gaps) / float32(len(buttons))
		}
		f.button(p, button.id, x, y, buttonW, 36, button.label, button.symbol, button.style, button.enabled, f.palette.Bg)
		x += buttonW + 8
	}
}

// segmented draws the Claude Desktop mode switch as the page draws its tabs.
func (f *flyout) segmented(p *painter, x, y, w float32, v View) {
	pal := f.palette
	p.fill(x, y, w, 36, 12, pal.Surface2)
	p.stroke(x, y, w, 36, 12, pal.Line)
	half := (w - 8) / 2
	segments := []struct {
		id      zoneID
		label   string
		mode    ClaudeMode
		enabled bool
	}{
		{zoneClaudeAnthropic, "Anthropic", ClaudeModeAnthropic, v.Policy.ClaudeAnthropic},
		{zoneClaudeLocal, "Local", ClaudeModeLocal, v.Policy.ClaudeLocal},
	}
	for index, segment := range segments {
		segmentX := x + 4 + float32(index)*half
		active := v.ClaudeMode == segment.mode
		ink := pal.InkMuted
		switch {
		case active:
			p.fill(segmentX, y+4, half, 28, 9, pal.Surface)
			p.stroke(segmentX, y+4, half, 28, 9, pal.Line)
			ink = pal.Ink
		case segment.enabled && f.hover == segment.id:
			ink = pal.Ink
		case !segment.enabled:
			ink = fade(pal.Surface2, pal.InkMuted)
		}
		p.text(segment.label, segmentX, y+4, half, 28, textLabel, ink, dtCenter|dtVCenter|dtSingleLine)
		f.focusRing(p, segment.id, segmentX, y+4, half, 28, 9)
		p.zone(segment.id, segmentX, y+4, half, 28, segment.enabled)
	}
}

// iconFromPixels wraps straight-alpha BGRA pixels in an icon handle.
func iconFromPixels(pixels []byte, size int) (windows.Handle, error) {
	header := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(size),
		Height:   -int32(size), // top-down, the order RenderMark writes
		Planes:   1,
		BitCount: 32,
	}
	var bits unsafe.Pointer
	color, _, err := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0, fmt.Errorf("create icon bitmap: %w", err)
	}
	defer procDeleteObject.Call(color)
	copy(unsafe.Slice((*byte)(bits), len(pixels)), pixels)
	mask := make([]byte, ((size+15)/16)*2*size)
	maskBitmap, _, err := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	if maskBitmap == 0 {
		return 0, fmt.Errorf("create icon mask: %w", err)
	}
	defer procDeleteObject.Call(maskBitmap)
	info := iconInfo{Icon: 1, Mask: windows.Handle(maskBitmap), Color: windows.Handle(color)}
	icon, _, err := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&info)))
	if icon == 0 {
		return 0, fmt.Errorf("create icon: %w", err)
	}
	return windows.Handle(icon), nil
}
