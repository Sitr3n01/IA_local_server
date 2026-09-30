//go:build windows

package monitor

import (
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type windowsTerminator struct{}

func newTerminator() processTerminator { return windowsTerminator{} }

const terminateWait = 5 * time.Second

// identify opens the process with the least access that can name it. A process
// that is not the monitor's user's refuses this or fails the token check, and
// either is "not permitted".
func (windowsTerminator) identify(pid uint32) (processIdentity, error) {
	if pid <= 4 {
		return processIdentity{}, errNotPermitted
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return processIdentity{}, errProcessGone
		}
		return processIdentity{}, errNotPermitted
	}
	defer windows.CloseHandle(handle)
	return identityOf(handle, pid)
}

func identityOf(handle windows.Handle, pid uint32) (processIdentity, error) {
	if same, err := processRunsAsUs(pid); err != nil || !same {
		return processIdentity{}, errNotPermitted
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return processIdentity{}, errNotPermitted
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return processIdentity{}, errNotPermitted
	}
	// A process that has already exited can still be opened while something
	// holds a handle to it; its exit time is set.
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return processIdentity{}, errProcessGone
	}
	return processIdentity{
		PID:     pid,
		Exe:     filepath.Base(windows.UTF16ToString(buffer[:size])),
		Started: time.Unix(0, creation.Nanoseconds()),
	}, nil
}

// terminate re-identifies the process through a handle it then ends, so nothing
// can swap the target between the check and the kill.
func (windowsTerminator) terminate(target processIdentity) error {
	access := uint32(windows.PROCESS_TERMINATE | windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	handle, err := windows.OpenProcess(access, false, target.PID)
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return errProcessGone
		}
		return errNotPermitted
	}
	defer windows.CloseHandle(handle)

	current, err := identityOf(handle, target.PID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(current.Exe, target.Exe) || !current.Started.Equal(target.Started) {
		return errProcessChanged
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return errTerminateFailed
	}
	event, err := windows.WaitForSingleObject(handle, uint32(terminateWait.Milliseconds()))
	if err != nil || event != windows.WAIT_OBJECT_0 {
		return errTerminateSlow
	}
	return nil
}
