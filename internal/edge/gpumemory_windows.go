//go:build windows

package edge

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Adapter memory is read through PDH rather than DXGI. DXGI would require COM
// interop for IDXGIAdapter3::QueryVideoMemoryInfo, and PDH exposes the same two
// quantities the Windows GPU counter set already publishes.
//
// PdhAddEnglishCounterW, not PdhAddCounterW: counter path names are localized,
// and \GPU Adapter Memory(*)\Dedicated Usage does not resolve on a non-English
// Windows. The English variant takes the invariant name and is the reason this
// probe works on the pt-BR reference workstation, where the localized path
// fails. The same defect was found and fixed in scripts/v2/Telemetry.ps1.
var (
	pdhDLL                           = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = pdhDLL.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = pdhDLL.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = pdhDLL.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArrayW = pdhDLL.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = pdhDLL.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble  = 0x00000200
	pdhMoreData   = 0x800007D2
	pdhCStatusOK  = 0x00000000
	pdhCStatusNew = 0x00000001

	counterDedicatedUsage = `\GPU Adapter Memory(*)\Dedicated Usage`
	counterSharedUsage    = `\GPU Adapter Memory(*)\Shared Usage`

	bytesPerMiB = 1024 * 1024
)

// pdhCounterValueDouble mirrors PDH_FMT_COUNTERVALUE. The union is 8-byte
// aligned, so CStatus is followed by four bytes of padding on amd64 before the
// double begins. Getting this wrong reads the padding as the mantissa and
// produces plausible-looking garbage rather than an error.
type pdhCounterValueDouble struct {
	CStatus     uint32
	_           uint32
	DoubleValue float64
}

// pdhCounterValueItemDouble mirrors PDH_FMT_COUNTERVALUE_ITEM_W.
type pdhCounterValueItemDouble struct {
	Name  *uint16
	Value pdhCounterValueDouble
}

func pdhCall(proc *windows.LazyProc, args ...uintptr) uint32 {
	ret, _, _ := proc.Call(args...)
	return uint32(ret)
}

// readAdapterCounter returns one counter's value per adapter instance, in bytes.
func readAdapterCounter(path string) (map[string]float64, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	var query uintptr
	if status := pdhCall(procPdhOpenQueryW, 0, 0, uintptr(unsafe.Pointer(&query))); status != 0 {
		return nil, fmt.Errorf("PdhOpenQueryW failed: 0x%X", status)
	}
	defer pdhCall(procPdhCloseQuery, query)

	var counter uintptr
	if status := pdhCall(procPdhAddEnglishCounterW, query, uintptr(unsafe.Pointer(pathPtr)), 0, uintptr(unsafe.Pointer(&counter))); status != 0 {
		return nil, fmt.Errorf("PdhAddEnglishCounterW(%s) failed: 0x%X", path, status)
	}

	// These are raw gauges, so one collection is sufficient; a rate counter
	// would need two separated in time.
	if status := pdhCall(procPdhCollectQueryData, query); status != 0 {
		return nil, fmt.Errorf("PdhCollectQueryData failed: 0x%X", status)
	}

	var bufferSize, itemCount uint32
	status := pdhCall(procPdhGetFormattedCounterArrayW, counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)), 0)
	if status != pdhMoreData {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW sizing failed: 0x%X", status)
	}
	if bufferSize == 0 || itemCount == 0 {
		return map[string]float64{}, nil
	}

	buffer := make([]byte, bufferSize)
	status = pdhCall(procPdhGetFormattedCounterArrayW, counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buffer[0])))
	if status != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW failed: 0x%X", status)
	}

	values := make(map[string]float64, itemCount)
	itemSize := unsafe.Sizeof(pdhCounterValueItemDouble{})
	for index := uint32(0); index < itemCount; index++ {
		offset := uintptr(index) * itemSize
		if offset+itemSize > uintptr(len(buffer)) {
			break
		}
		item := (*pdhCounterValueItemDouble)(unsafe.Pointer(&buffer[offset]))
		if item.Value.CStatus != pdhCStatusOK && item.Value.CStatus != pdhCStatusNew {
			continue
		}
		name := ""
		if item.Name != nil {
			name = windows.UTF16PtrToString(item.Name)
		}
		values[name] = item.Value.DoubleValue
	}
	return values, nil
}

// gpuMemoryStatus reads the busiest adapter's dedicated and shared usage.
//
// The busiest adapter is the one under test. Summing across instances would fold
// an idle integrated GPU into the discrete card's figure and understate how
// close the discrete adapter is to its own budget; the reference workstation
// exposes three LUIDs of which only one is ever non-zero.
func gpuMemoryStatus() (gpuMemorySnapshot, error) {
	dedicated, err := readAdapterCounter(counterDedicatedUsage)
	if err != nil {
		return gpuMemorySnapshot{}, err
	}
	if len(dedicated) == 0 {
		return gpuMemorySnapshot{}, errGPUMemoryUnavailable
	}

	busiest := ""
	busiestBytes := -1.0
	for instance, value := range dedicated {
		// Deterministic when two adapters tie, so repeated probes on an idle
		// host do not alternate between instances.
		if value > busiestBytes || (value == busiestBytes && strings.Compare(instance, busiest) < 0) {
			busiest = instance
			busiestBytes = value
		}
	}
	if busiestBytes < 0 {
		return gpuMemorySnapshot{}, errGPUMemoryUnavailable
	}

	// A missing shared counter is reported as zero rather than as a failure: the
	// dedicated reading is still worth having, and shared usage of zero is a
	// truthful description of an adapter that is not paging.
	sharedBytes := 0.0
	if shared, sharedErr := readAdapterCounter(counterSharedUsage); sharedErr == nil {
		sharedBytes = shared[busiest]
	}

	return gpuMemorySnapshot{
		Adapter:      busiest,
		DedicatedMiB: busiestBytes / bytesPerMiB,
		SharedMiB:    sharedBytes / bytesPerMiB,
	}, nil
}
