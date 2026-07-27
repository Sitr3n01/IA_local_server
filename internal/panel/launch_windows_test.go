//go:build windows

package panel

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsLaunchWindowPolicy(t *testing.T) {
	codex := exec.Command("unused.exe")
	applyLaunchAttributes(codex, ClientCodex)
	if codex.SysProcAttr == nil || codex.SysProcAttr.CreationFlags != windows.CREATE_NEW_CONSOLE {
		t.Fatalf("Codex creation flags = %#v", codex.SysProcAttr)
	}

	for _, client := range []Client{ClientOpenCode} {
		command := exec.Command("unused.exe")
		applyLaunchAttributes(command, client)
		if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags != windows.CREATE_NO_WINDOW {
			t.Fatalf("%s creation flags = %#v", client, command.SysProcAttr)
		}
	}
}
