//go:build windows

package edge

import (
	"errors"
	"runtime"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// unclosable lists processes a person cannot close to free memory: the
// kernel's own accounting entries, Windows services, and this deployment,
// whose model is already counted as reclaimable.
var unclosable = map[string]bool{
	"system": true, "registry": true, "memory compression": true, "secure system": true,
	"smss": true, "csrss": true, "wininit": true, "winlogon": true, "services": true,
	"lsass": true, "svchost": true, "dwm": true, "fontdrvhost": true, "msmpeng": true,
	"llama-server": true, "llama-swap": true,
}

// topMemoryConsumers sums memory by executable name and returns the largest
// groups a person could close: resident memory for a physical-memory refusal,
// private commit for a commit refusal. It reads the whole process table in
// one NtQuerySystemInformation call and opens no process, so a process this
// account may not open - a virtual machine's vmmem, notably - is still
// counted; opening each process skipped exactly those. The list is a hint,
// not an accounting.
func topMemoryConsumers(limit int, byCommit bool) []memoryConsumer {
	buffer, err := systemProcessInformation()
	if err != nil {
		return nil
	}
	totals := map[string]float64{}
	for offset := 0; offset+int(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{})) <= len(buffer); {
		entry := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buffer[offset]))
		name := strings.TrimSuffix(entry.ImageName.String(), ".exe")
		lower := strings.ToLower(name)
		if name != "" && !unclosable[lower] && !strings.HasPrefix(lower, "cia-") {
			size := entry.WorkingSetSize
			if byCommit {
				size = entry.PagefileUsage
			}
			totals[name] += float64(size) / float64(uint64(1)<<30)
		}
		if entry.NextEntryOffset == 0 {
			break
		}
		offset += int(entry.NextEntryOffset)
	}
	runtime.KeepAlive(buffer)

	consumers := make([]memoryConsumer, 0, len(totals))
	for name, size := range totals {
		if size >= 0.1 {
			consumers = append(consumers, memoryConsumer{Name: name, GiB: size})
		}
	}
	sort.Slice(consumers, func(i, j int) bool { return consumers[i].GiB > consumers[j].GiB })
	if len(consumers) > limit {
		consumers = consumers[:limit]
	}
	return consumers
}

// systemProcessInformation returns the SystemProcessInformation table. The
// table grows between the sizing call and the read when processes start, so
// the buffer gets headroom and a few attempts.
func systemProcessInformation() ([]byte, error) {
	size := uint32(1 << 20)
	for attempt := 0; attempt < 4; attempt++ {
		buffer := make([]byte, size)
		var needed uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buffer[0]), size, &needed)
		if err == nil {
			if needed == 0 || needed > size {
				return buffer, nil
			}
			return buffer[:needed], nil
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || needed > 64<<20 {
			return nil, err
		}
		size = needed + 64<<10
	}
	return nil, errors.New("the process table kept growing while it was read")
}
