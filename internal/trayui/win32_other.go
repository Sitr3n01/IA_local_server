//go:build !windows

package trayui

import (
	"context"
	"errors"
)

// Run is intentionally unavailable away from Windows. Keeping the stub lets
// non-Windows static analysis type-check the command without pretending that a
// notification-area implementation exists.
func Run(context.Context, Controller, Options) error {
	return errors.New("cia-tray is supported only on Windows")
}
