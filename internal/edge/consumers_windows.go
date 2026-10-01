//go:build windows

package edge

import (
	"errors"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// unclosable lists processes a person cannot close to free memory: the
// kernel's own accounting entries, Windows services, and this deployment,
// whose model is already counted as reclaimable.
var unclosable = map[string]bool{
	"system": true, "registry": true, "memory compression": true, "secure system": true,
	"smss": true, "csrss": true, "wininit": true, "winlogon": true, "services": true,
	"lsass": true, "svchost": true, "dwm": true, "fontdrvhost": true, "msmpeng": true,
	"llama-server": true, "llama-swap": true,
}

// topMemoryConsumers sums resident memory by executable name and returns the
// largest groups a person could close. A process that exits or denies access
// while it is read is skipped; the list is a hint, not an accounting.
func topMemoryConsumers(limit int) []memoryConsumer {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	totals := map[string]float64{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := strings.TrimSuffix(windows.UTF16ToString(entry.ExeFile[:]), ".exe")
		lower := strings.ToLower(name)
		if unclosable[lower] || strings.HasPrefix(lower, "cia-") {
			continue
		}
		if resident, ok := residentBytes(entry.ProcessID); ok {
			totals[name] += float64(resident) / float64(uint64(1)<<30)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil
	}

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

func residentBytes(pid uint32) (uintptr, bool) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(process)
	counters := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, _ := getProcessMemoryInfo.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB))
	if result == 0 {
		return 0, false
	}
	return counters.WorkingSetSize, true
}
