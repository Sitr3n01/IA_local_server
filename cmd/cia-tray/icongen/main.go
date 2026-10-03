//go:build windows

// Command icongen renders the IA Local mark into the PNG files go-winres packs
// into cia-tray.exe's icon, so Explorer, Task Manager and the notification
// area settings show the same mark the tray draws at run time. It is run by
// go generate in cmd/cia-tray; the PNGs and the .syso are committed.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

// iconSizes are the sizes Windows asks an application icon for across 100 to
// 400 percent scaling, from the small taskbar icon to the large tile.
var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func main() {
	out := flag.String("out", "winres", "directory the PNG files are written to")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, size := range iconSizes {
		pixels, err := trayui.RenderMark(size, trayui.Brand, 0xffffffff)
		if err != nil {
			fail(err)
		}
		picture := image.NewNRGBA(image.Rect(0, 0, size, size))
		for index := 0; index < len(pixels); index += 4 {
			// RenderMark returns BGRA; image.NRGBA is RGBA.
			picture.Pix[index] = pixels[index+2]
			picture.Pix[index+1] = pixels[index+1]
			picture.Pix[index+2] = pixels[index]
			picture.Pix[index+3] = pixels[index+3]
		}
		path := filepath.Join(*out, fmt.Sprintf("icon%d.png", size))
		file, err := os.Create(path)
		if err != nil {
			fail(err)
		}
		if err := png.Encode(file, picture); err != nil {
			_ = file.Close()
			fail(err)
		}
		if err := file.Close(); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icongen:", err)
	os.Exit(1)
}
