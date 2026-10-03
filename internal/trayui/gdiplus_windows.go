//go:build windows

package trayui

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GDI+ draws the shapes GDI cannot antialias: the rounded cards, pills and
// the icon mark. Text stays with GDI, whose ClearType output matches the rest
// of the desktop. The flat API takes REAL (float32) arguments; the Go runtime
// mirrors the first four integer argument registers into XMM0-3 and passes
// the rest in 8-byte stack slots, which is what the x64 ABI expects for them.

var (
	gdiplus = windows.NewLazySystemDLL("gdiplus.dll")

	procGdiplusStartup            = gdiplus.NewProc("GdiplusStartup")
	procGdipCreateFromHDC         = gdiplus.NewProc("GdipCreateFromHDC")
	procGdipDeleteGraphics        = gdiplus.NewProc("GdipDeleteGraphics")
	procGdipSetSmoothingMode      = gdiplus.NewProc("GdipSetSmoothingMode")
	procGdipSetPixelOffsetMode    = gdiplus.NewProc("GdipSetPixelOffsetMode")
	procGdipGraphicsClear         = gdiplus.NewProc("GdipGraphicsClear")
	procGdipGetDC                 = gdiplus.NewProc("GdipGetDC")
	procGdipReleaseDC             = gdiplus.NewProc("GdipReleaseDC")
	procGdipCreateSolidFill       = gdiplus.NewProc("GdipCreateSolidFill")
	procGdipDeleteBrush           = gdiplus.NewProc("GdipDeleteBrush")
	procGdipCreatePen1            = gdiplus.NewProc("GdipCreatePen1")
	procGdipDeletePen             = gdiplus.NewProc("GdipDeletePen")
	procGdipSetPenStartCap        = gdiplus.NewProc("GdipSetPenStartCap")
	procGdipSetPenEndCap          = gdiplus.NewProc("GdipSetPenEndCap")
	procGdipSetPenLineJoin        = gdiplus.NewProc("GdipSetPenLineJoin")
	procGdipCreatePath            = gdiplus.NewProc("GdipCreatePath")
	procGdipDeletePath            = gdiplus.NewProc("GdipDeletePath")
	procGdipAddPathArc            = gdiplus.NewProc("GdipAddPathArc")
	procGdipAddPathLine           = gdiplus.NewProc("GdipAddPathLine")
	procGdipClosePathFigure       = gdiplus.NewProc("GdipClosePathFigure")
	procGdipFillPath              = gdiplus.NewProc("GdipFillPath")
	procGdipDrawPath              = gdiplus.NewProc("GdipDrawPath")
	procGdipFillEllipse           = gdiplus.NewProc("GdipFillEllipse")
	procGdipDrawEllipse           = gdiplus.NewProc("GdipDrawEllipse")
	procGdipFillRectangle         = gdiplus.NewProc("GdipFillRectangle")
	procGdipDrawLine              = gdiplus.NewProc("GdipDrawLine")
	procGdipSetClipPath           = gdiplus.NewProc("GdipSetClipPath")
	procGdipResetClip             = gdiplus.NewProc("GdipResetClip")
	procGdipCreateBitmapFromScan0 = gdiplus.NewProc("GdipCreateBitmapFromScan0")
	procGdipGetImageGraphicsCtx   = gdiplus.NewProc("GdipGetImageGraphicsContext")
	procGdipDisposeImage          = gdiplus.NewProc("GdipDisposeImage")

	gdiplusOnce sync.Once
	gdiplusErr  error
)

const (
	smoothingModeAntiAlias8x8 = 5
	pixelOffsetModeHalf       = 4
	lineCapRound              = 2
	lineJoinRound             = 2
	unitPixel                 = 2
	combineModeReplace        = 0
	pixelFormat32bppARGB      = 0x0026200A
)

type gdiplusStartupInput struct {
	Version                  uint32
	DebugEventCallback       uintptr
	SuppressBackgroundThread int32
	SuppressExternalCodecs   int32
}

func startGDIPlus() error {
	gdiplusOnce.Do(func() {
		input := gdiplusStartupInput{Version: 1}
		var token uintptr
		status, _, _ := procGdiplusStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0)
		if status != 0 {
			gdiplusErr = fmt.Errorf("start GDI+: status %d", status)
		}
	})
	return gdiplusErr
}

func real32(value float32) uintptr { return uintptr(math.Float32bits(value)) }

func gdipCall(proc *windows.LazyProc, args ...uintptr) error {
	status, _, _ := proc.Call(args...)
	if status != 0 {
		return fmt.Errorf("%s: GDI+ status %d", proc.Name, status)
	}
	return nil
}

// canvas is one GDI+ drawing surface, either a window's memory DC or an icon
// bitmap. Coordinates are device pixels.
type canvas struct {
	graphics uintptr
}

func newCanvas(graphics uintptr) (*canvas, error) {
	if err := gdipCall(procGdipSetSmoothingMode, graphics, smoothingModeAntiAlias8x8); err != nil {
		_, _, _ = procGdipDeleteGraphics.Call(graphics)
		return nil, err
	}
	// Half-pixel offset puts pixel centres on .5, so a fill between integer
	// coordinates covers whole pixels, as in the browser.
	_, _, _ = procGdipSetPixelOffsetMode.Call(graphics, pixelOffsetModeHalf)
	return &canvas{graphics: graphics}, nil
}

func canvasFromHDC(hdc uintptr) (*canvas, error) {
	var graphics uintptr
	if err := gdipCall(procGdipCreateFromHDC, hdc, uintptr(unsafe.Pointer(&graphics))); err != nil {
		return nil, err
	}
	return newCanvas(graphics)
}

func (c *canvas) close() {
	if c.graphics != 0 {
		_, _, _ = procGdipDeleteGraphics.Call(c.graphics)
		c.graphics = 0
	}
}

// withDC lends the canvas's device context to GDI for text; GDI+ requires its
// own drawing to be suspended meanwhile.
func (c *canvas) withDC(draw func(hdc uintptr)) {
	var hdc uintptr
	if status, _, _ := procGdipGetDC.Call(c.graphics, uintptr(unsafe.Pointer(&hdc))); status != 0 || hdc == 0 {
		return
	}
	draw(hdc)
	_, _, _ = procGdipReleaseDC.Call(c.graphics, hdc)
}

func (c *canvas) clear(color Color) {
	_, _, _ = procGdipGraphicsClear.Call(c.graphics, uintptr(color))
}

func (c *canvas) brush(color Color, use func(brush uintptr)) {
	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(color), uintptr(unsafe.Pointer(&brush))); status != 0 {
		return
	}
	use(brush)
	_, _, _ = procGdipDeleteBrush.Call(brush)
}

func (c *canvas) pen(color Color, width float32, use func(pen uintptr)) {
	var pen uintptr
	if status, _, _ := procGdipCreatePen1.Call(uintptr(color), real32(width), unitPixel, uintptr(unsafe.Pointer(&pen))); status != 0 {
		return
	}
	_, _, _ = procGdipSetPenStartCap.Call(pen, lineCapRound)
	_, _, _ = procGdipSetPenEndCap.Call(pen, lineCapRound)
	_, _, _ = procGdipSetPenLineJoin.Call(pen, lineJoinRound)
	use(pen)
	_, _, _ = procGdipDeletePen.Call(pen)
}

// roundedPath builds a rectangle with corner radius r; r is clamped to half
// the shorter side, which makes a pill of any height.
func roundedPath(x, y, w, h, r float32) (uintptr, error) {
	var path uintptr
	if err := gdipCall(procGdipCreatePath, 0, uintptr(unsafe.Pointer(&path))); err != nil {
		return 0, err
	}
	r = float32(math.Min(float64(r), math.Min(float64(w), float64(h))/2))
	if r <= 0 {
		for _, segment := range [][4]float32{{x, y, x + w, y}, {x + w, y, x + w, y + h}, {x + w, y + h, x, y + h}} {
			_, _, _ = procGdipAddPathLine.Call(path, real32(segment[0]), real32(segment[1]), real32(segment[2]), real32(segment[3]))
		}
	} else {
		d := 2 * r
		arc := func(ax, ay, start float32) {
			_, _, _ = procGdipAddPathArc.Call(path, real32(ax), real32(ay), real32(d), real32(d), real32(start), real32(90))
		}
		arc(x, y, 180)
		arc(x+w-d, y, 270)
		arc(x+w-d, y+h-d, 0)
		arc(x, y+h-d, 90)
	}
	_, _, _ = procGdipClosePathFigure.Call(path)
	return path, nil
}

func (c *canvas) fillRound(x, y, w, h, r float32, color Color) {
	if w <= 0 || h <= 0 || color>>24 == 0 {
		return
	}
	path, err := roundedPath(x, y, w, h, r)
	if err != nil {
		return
	}
	defer procGdipDeletePath.Call(path)
	c.brush(color, func(brush uintptr) { _, _, _ = procGdipFillPath.Call(c.graphics, brush, path) })
}

// strokeRound draws a border of the given width fully inside the rectangle,
// as CSS does with box-sizing: border-box.
func (c *canvas) strokeRound(x, y, w, h, r, width float32, color Color) {
	if w <= 0 || h <= 0 || color>>24 == 0 {
		return
	}
	inset := width / 2
	path, err := roundedPath(x+inset, y+inset, w-width, h-width, r-inset)
	if err != nil {
		return
	}
	defer procGdipDeletePath.Call(path)
	c.pen(color, width, func(pen uintptr) { _, _, _ = procGdipDrawPath.Call(c.graphics, pen, path) })
}

func (c *canvas) fillCircle(cx, cy, r float32, color Color) {
	c.brush(color, func(brush uintptr) {
		_, _, _ = procGdipFillEllipse.Call(c.graphics, brush, real32(cx-r), real32(cy-r), real32(2*r), real32(2*r))
	})
}

func (c *canvas) strokeCircle(cx, cy, r, width float32, color Color) {
	r -= width / 2
	c.pen(color, width, func(pen uintptr) {
		_, _, _ = procGdipDrawEllipse.Call(c.graphics, pen, real32(cx-r), real32(cy-r), real32(2*r), real32(2*r))
	})
}

func (c *canvas) fillRect(x, y, w, h float32, color Color) {
	c.brush(color, func(brush uintptr) {
		_, _, _ = procGdipFillRectangle.Call(c.graphics, brush, real32(x), real32(y), real32(w), real32(h))
	})
}

func (c *canvas) line(x1, y1, x2, y2, width float32, color Color) {
	c.pen(color, width, func(pen uintptr) {
		_, _, _ = procGdipDrawLine.Call(c.graphics, pen, real32(x1), real32(y1), real32(x2), real32(y2))
	})
}

// polyline strokes connected segments with round joins; points are x,y pairs.
func (c *canvas) polyline(points []float32, width float32, color Color) {
	if len(points) < 4 {
		return
	}
	var path uintptr
	if err := gdipCall(procGdipCreatePath, 0, uintptr(unsafe.Pointer(&path))); err != nil {
		return
	}
	defer procGdipDeletePath.Call(path)
	for index := 2; index+1 < len(points); index += 2 {
		_, _, _ = procGdipAddPathLine.Call(path, real32(points[index-2]), real32(points[index-1]), real32(points[index]), real32(points[index+1]))
	}
	c.pen(color, width, func(pen uintptr) { _, _, _ = procGdipDrawPath.Call(c.graphics, pen, path) })
}

// clipRound limits drawing to a rounded rectangle until resetClip.
func (c *canvas) clipRound(x, y, w, h, r float32) {
	path, err := roundedPath(x, y, w, h, r)
	if err != nil {
		return
	}
	defer procGdipDeletePath.Call(path)
	_, _, _ = procGdipSetClipPath.Call(c.graphics, path, combineModeReplace)
}

func (c *canvas) resetClip() { _, _, _ = procGdipResetClip.Call(c.graphics) }

// drawMark draws the IA Local mark, the same drawing as the monitor's
// icon.svg (a chip with a bolt on a rounded tile), at size pixels. Below 32
// pixels the chip's pins blur into the tile, so the small mark keeps only the
// chip, snapped to whole pixels, and a larger bolt.
func (c *canvas) drawMark(x, y, size float32, tile, glyph Color) {
	k := size / 32
	c.fillRound(x, y, size, size, 8*k, tile)
	at := func(values ...float32) []float32 {
		points := make([]float32, len(values))
		for index, value := range values {
			if index%2 == 0 {
				points[index] = x + value*k
			} else {
				points[index] = y + value*k
			}
		}
		return points
	}
	if size < 32 {
		stroke := float32(math.Max(1, math.Round(float64(2.5*k))))
		inset := float32(math.Round(float64(5.5 * k)))
		side := size - 2*inset
		c.strokeRound(x+inset, y+inset, side, side, 4*k+stroke/2, stroke, glyph)
		c.polyline(at(17.6, 10, 13.2, 16.4, 18.8, 16.4, 14.4, 22.8), float32(math.Max(1.4, float64(2.6*k))), glyph)
		return
	}
	stroke := 2 * k
	c.strokeRound(x+8*k, y+8*k, 16*k, 16*k, 4.5*k, stroke, glyph)
	for _, pin := range [][4]float32{
		{13, 5.5, 13, 8.5}, {19, 5.5, 19, 8.5}, {13, 23.5, 13, 26.5}, {19, 23.5, 19, 26.5},
		{5.5, 13, 8.5, 13}, {5.5, 19, 8.5, 19}, {23.5, 13, 26.5, 13}, {23.5, 19, 26.5, 19},
	} {
		points := at(pin[0], pin[1], pin[2], pin[3])
		c.line(points[0], points[1], points[2], points[3], stroke, glyph)
	}
	c.polyline(at(16.8, 12.4, 13.9, 16.5, 17.5, 16.5, 15.5, 20), stroke, glyph)
}

// RenderMark rasterises the mark into straight-alpha BGRA pixels, top-down,
// size*size*4 bytes: the layout of both a 32-bit icon and a PNG row.
func RenderMark(size int, tile, glyph Color) ([]byte, error) {
	if size < 8 || size > 512 {
		return nil, errors.New("mark size must be between 8 and 512 pixels")
	}
	if err := startGDIPlus(); err != nil {
		return nil, err
	}
	pixels := make([]byte, size*size*4)
	var bitmap uintptr
	if err := gdipCall(procGdipCreateBitmapFromScan0, uintptr(size), uintptr(size), uintptr(size*4), pixelFormat32bppARGB,
		uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bitmap))); err != nil {
		return nil, err
	}
	defer procGdipDisposeImage.Call(bitmap)
	var graphics uintptr
	if err := gdipCall(procGdipGetImageGraphicsCtx, bitmap, uintptr(unsafe.Pointer(&graphics))); err != nil {
		return nil, err
	}
	c, err := newCanvas(graphics)
	if err != nil {
		return nil, err
	}
	c.clear(0)
	c.drawMark(0, 0, float32(size), tile, glyph)
	c.close()
	runtime.KeepAlive(pixels)
	return pixels, nil
}
