//go:build windows

package trayui

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

type renderCase struct {
	name    string
	state   ViewState
	confirm bool
}

func renderCases() []renderCase {
	idle := viewSnapshot()
	loaded := viewSnapshot()
	loaded.ActiveModel = "gemma"
	offline := viewSnapshot()
	offline.EdgeReachable, offline.StatusAvailable, offline.ProviderReady, offline.UpstreamReady = false, false, false, false
	pressured := viewSnapshot()
	pressured.ProviderReady, pressured.CapacityOK = false, false
	pressured.ReadyNote, pressured.CapacityNote = "memória física insuficiente", "memória física insuficiente"
	pressured.SelectionNote = "O modelo salvo não está mais disponível; o modelo padrão foi selecionado."
	working := viewSnapshot()
	working.ActiveModel, working.Active, working.Queued = "qwen", 1, 3
	working.ClaudeAvailable, working.ClaudeGatewayOK = true, true
	empty := viewSnapshot()
	empty.Models = nil
	crowded := viewSnapshot()
	crowded.CapacityOK, crowded.CapacityNote = false, "reserva de memória insuficiente"
	crowded.SelectionNote = "O modelo salvo não está mais disponível; o modelo padrão foi selecionado."
	for index := 0; index < 6; index++ {
		crowded.Models = append(crowded.Models, Model{
			ID: fmt.Sprintf("extra-%d", index), DisplayName: "Modelo com um nome de exibição bem comprido | Q4_K_M | 128k context", Available: true,
		})
	}
	return []renderCase{
		{name: "connecting", state: ViewState{}},
		{name: "idle", state: ViewState{Loaded: true, Snapshot: idle}},
		{name: "loaded", state: ViewState{Loaded: true, Snapshot: loaded}},
		{name: "offline", state: ViewState{Loaded: true, Snapshot: offline}},
		{name: "starting", state: ViewState{Loaded: true, Snapshot: offline, Starting: true}},
		{name: "busy", state: ViewState{Loaded: true, Snapshot: loaded, Activity: "Trocando de modelo"}},
		{name: "pressured", state: ViewState{Loaded: true, Snapshot: pressured}},
		{name: "working", state: ViewState{Loaded: true, Snapshot: working}},
		{name: "error", state: ViewState{Loaded: true, Snapshot: crowded, ActionErr: errors.New("model validation returned 503 Service Unavailable: the upstream router refused the request because another model is loading")}},
		{name: "no-models", state: ViewState{Loaded: true, Snapshot: empty}},
		{name: "confirm", state: ViewState{Loaded: true, Snapshot: working}, confirm: true},
	}
}

// renderPixels draws the flyout into a top-down 32-bit DIB and returns its
// BGRA pixels.
func renderPixels(t *testing.T, f *flyout, width, height int32) []byte {
	t.Helper()
	header := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: width, Height: -height, Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bitmap, _, err := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		t.Fatalf("create DIB: %v", err)
	}
	defer procDeleteObject.Call(bitmap)
	dc, _, _ := procCreateCompatibleDC.Call(0)
	defer procDeleteDC.Call(dc)
	old, _, _ := procSelectObject.Call(dc, bitmap)
	f.draw(dc, width, height)
	_, _, _ = procSelectObject.Call(dc, old)
	return append([]byte(nil), unsafe.Slice((*byte)(bits), int(width*height*4))...)
}

func TestFlyoutRendersEveryStateInsideItsBounds(t *testing.T) {
	if err := startGDIPlus(); err != nil {
		t.Fatal(err)
	}
	outDir := os.Getenv("CIA_TRAY_RENDER_DIR")
	for _, theme := range []struct {
		name    string
		palette Palette
	}{{"light", LightPalette}, {"dark", DarkPalette}} {
		for _, dpi := range []uint32{96, 144} {
			for _, test := range renderCases() {
				t.Run(fmt.Sprintf("%s/%d/%s", theme.name, dpi, test.name), func(t *testing.T) {
					f := newRenderer()
					defer f.destroy()
					f.palette = theme.palette
					f.setScale(dpi)
					f.view = BuildView(test.state)
					f.confirm = test.confirm
					f.contentH = f.measureHeight()
					f.viewH = f.contentH
					if f.contentH < 300 || f.contentH > 1100 {
						t.Fatalf("content height %.0f is implausible", f.contentH)
					}
					width := int32(math.Round(flyoutWidth * float64(f.scale)))
					height := int32(math.Ceil(float64(f.contentH * f.scale)))
					pixels := renderPixels(t, f, width, height)

					background := f.palette.Bg
					drawn := 0
					for index := 0; index < len(pixels); index += 4 {
						if pixels[index] != byte(background) || pixels[index+1] != byte(background>>8) || pixels[index+2] != byte(background>>16) {
							drawn++
						}
					}
					if drawn < len(pixels)/4/10 {
						t.Fatalf("only %d pixels differ from the background", drawn)
					}
					checkZones(t, f, width, height)
					if outDir != "" {
						writePNG(t, filepath.Join(outDir, fmt.Sprintf("%s-%d-%s.png", theme.name, dpi, test.name)), pixels, int(width), int(height))
					}
				})
			}
		}
	}
}

// checkZones asserts that every control lies inside the window, that live
// controls never overlap, and that every labelled button is wide enough for
// its label: the check that caught "OpenCo…".
func checkZones(t *testing.T, f *flyout, width, height int32) {
	t.Helper()
	labels := map[zoneID]string{
		zoneClaudeLocal: "Claude Local",
		zonePanel:       "Abrir painel", zoneShutdown: "Encerrar", zoneUnload: "Descarregar",
		zoneStartServer: "Iniciar o servidor", zoneShutdownConfirm: "Encerrar",
	}
	if f.view.ModelAction == ModelActionSwitch {
		labels[zoneModelAction] = "Trocar de modelo"
	} else {
		labels[zoneModelAction] = "Carregar"
	}
	for index, z := range f.zones {
		if z.area.Left < 0 || z.area.Top < 0 || z.area.Right > width || z.area.Bottom > height || z.area.Right <= z.area.Left {
			t.Errorf("zone %d outside the window: %+v", z.id, z.area)
		}
		if label, ok := labels[z.id]; ok {
			need := f.measure(label, textStrong) + 16 + 6 + 16
			if have := float32(z.area.Right-z.area.Left) / f.scale; have+1 < need {
				t.Errorf("button %q is %.0f px wide, its content needs %.0f", label, have, need)
			}
		}
		for _, other := range f.zones[index+1:] {
			if !z.enabled || !other.enabled {
				continue
			}
			if z.area.Left < other.area.Right && other.area.Left < z.area.Right && z.area.Top < other.area.Bottom && other.area.Top < z.area.Bottom {
				t.Errorf("live zones %d and %d overlap", z.id, other.id)
			}
		}
	}
}

func writePNG(t *testing.T, path string, pixels []byte, width, height int) {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for index := 0; index < len(pixels); index += 4 {
		picture.Pix[index], picture.Pix[index+1], picture.Pix[index+2], picture.Pix[index+3] = pixels[index+2], pixels[index+1], pixels[index], 255
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
}

func TestRenderMarkIsOpaqueInsideAndClearAtTheCorners(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32, 48} {
		pixels, err := RenderMark(size, Brand, 0xffffffff)
		if err != nil {
			t.Fatal(err)
		}
		alpha := func(x, y int) byte { return pixels[(y*size+x)*4+3] }
		if alpha(0, 0) > 40 || alpha(size-1, size-1) > 40 {
			t.Errorf("size %d: corners are not transparent (%d, %d)", size, alpha(0, 0), alpha(size-1, size-1))
		}
		if alpha(size/2, 2) != 255 || alpha(2, size/2) != 255 {
			t.Errorf("size %d: tile edge is not opaque", size)
		}
		white := 0
		for index := 0; index < len(pixels); index += 4 {
			if pixels[index] > 200 && pixels[index+1] > 200 && pixels[index+2] > 200 && pixels[index+3] > 200 {
				white++
			}
		}
		if white < size*size/20 {
			t.Errorf("size %d: the glyph is missing (%d white pixels)", size, white)
		}
	}
}
