//go:build !windows

package panel

import "os/exec"

func powerShellExecutable() (string, error) {
	return exec.LookPath("pwsh")
}

func applyLaunchAttributes(_ *exec.Cmd, _ Client) {}
