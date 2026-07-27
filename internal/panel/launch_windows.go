//go:build windows

package panel

import (
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func powerShellExecutable() (string, error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(systemDirectory, "WindowsPowerShell", "v1.0", "powershell.exe"), nil
}

func applyLaunchAttributes(command *exec.Cmd, client Client) {
	// Codex is a genuine interactive TUI (confirmed by running codex.exe
	// directly) and needs a real console to attach to; OpenCode's launcher
	// only bridges to its own Electron window, so it stays invisible.
	flags := uint32(windows.CREATE_NO_WINDOW)
	if client == ClientCodex {
		flags = windows.CREATE_NEW_CONSOLE
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}
