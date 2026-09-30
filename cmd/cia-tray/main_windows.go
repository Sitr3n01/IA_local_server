//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sitr3n/local-ai-provider/internal/trayui"
)

type windowsGUIStream struct{ writer io.Writer }

func (s windowsGUIStream) Write(p []byte) (int, error) {
	n, err := s.writer.Write(p)
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		// A windowsgui binary has no console handles when launched by Explorer or
		// Start-Process without redirection. CLI actions are still controlled by
		// their exit code, so an absent optional JSON stream is not a failure.
		return len(p), nil
	}
	return n, err
}

func defaultStreams() (io.Writer, io.Writer) {
	return windowsGUIStream{writer: os.Stdout}, windowsGUIStream{writer: os.Stderr}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdout, stderr := defaultStreams()
	if err := run(ctx, os.Args[1:], stdout, stderr); err != nil {
		if errors.Is(err, trayui.ErrAlreadyRunning) {
			return
		}
		if headlessInvocation(os.Args[1:]) {
			_, _ = fmt.Fprintln(stderr, "cia-tray:", err)
			os.Exit(1)
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
