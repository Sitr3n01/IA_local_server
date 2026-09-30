//go:build windows

package monitor

import (
	"encoding/binary"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ntdllDLL                      = windows.NewLazySystemDLL("ntdll.dll")
	procNtQueryInformationProcess = ntdllDLL.NewProc("NtQueryInformationProcess")
)

const (
	// ProcessCommandLineInformation, available since Windows 8.1. It needs only
	// PROCESS_QUERY_LIMITED_INFORMATION: the kernel copies the string out, so
	// the caller never reads the process's memory.
	processCommandLineInformation = 60
	statusInfoLengthMismatch      = 0xC0000004
	statusBufferOverflow          = 0x80000005
	statusBufferTooSmall          = 0xC0000023
	maxCommandLineBytes           = 64 << 10
	unicodeStringHeaderBytes      = 16
)

// readLaunchInfo returns the allowlisted part of a process's command line. The
// line is decomposed and reduced in this function and never leaves it.
func readLaunchInfo(pid uint32) (launchInfo, bool) {
	if pid == 0 || procNtQueryInformationProcess.Find() != nil {
		return launchInfo{}, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return launchInfo{}, false
	}
	defer windows.CloseHandle(handle)

	size := uint32(512)
	for attempt := 0; attempt < 3; attempt++ {
		buffer := make([]byte, size)
		var needed uint32
		status, _, _ := procNtQueryInformationProcess.Call(uintptr(handle), processCommandLineInformation,
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(size), uintptr(unsafe.Pointer(&needed)))
		switch uint32(status) {
		case 0:
			// A UNICODE_STRING header (Length, MaximumLength, padding, pointer)
			// with the characters right behind it.
			length := int(binary.LittleEndian.Uint16(buffer))
			if length == 0 || length%2 != 0 || unicodeStringHeaderBytes+length > len(buffer) {
				return launchInfo{}, false
			}
			characters := make([]uint16, length/2)
			for index := range characters {
				characters[index] = binary.LittleEndian.Uint16(buffer[unicodeStringHeaderBytes+index*2:])
			}
			args, err := windows.DecomposeCommandLine(windows.UTF16ToString(characters))
			if err != nil {
				return launchInfo{}, false
			}
			info := parseLaunchArguments(args)
			return info, info.File != "" || info.Alias != ""
		case statusInfoLengthMismatch, statusBufferOverflow, statusBufferTooSmall:
			if needed == 0 || needed > maxCommandLineBytes {
				return launchInfo{}, false
			}
			size = needed
		default:
			return launchInfo{}, false
		}
	}
	return launchInfo{}, false
}

// processStart is when the process was created, which for a model server is
// close to when it loaded its model.
func processStart(pid uint32) (time.Time, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}
