//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdout, stderr := defaultStreams()
	if err := run(ctx, os.Args[1:], stdout, stderr); err != nil {
		if errors.Is(err, trayui.ErrAlreadyRunning) {
			return
		}
		reportFatal(err.Error())
		os.Exit(1)
	}
}

func reportFatal(message string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	text, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("CIA Local AI")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
