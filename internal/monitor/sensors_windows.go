//go:build windows

package monitor

import (
	"encoding/binary"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AMD's driver ships atiadlxx.dll in System32; it is loaded from there only,
// never through the search path. A machine without an AMD GPU has no such file,
// and the reader then reports nothing, which the page shows as unavailable.
var (
	adlDLL                            = windows.NewLazySystemDLL("atiadlxx.dll")
	procADLMainControlCreate          = adlDLL.NewProc("ADL2_Main_Control_Create")
	procADLMainControlDestroy         = adlDLL.NewProc("ADL2_Main_Control_Destroy")
	procADLAdapterNumberOfAdaptersGet = adlDLL.NewProc("ADL2_Adapter_NumberOfAdapters_Get")
	procADLAdapterAdapterInfoGet      = adlDLL.NewProc("ADL2_Adapter_AdapterInfo_Get")
	procADLNewQueryPMLogDataGet       = adlDLL.NewProc("ADL2_New_QueryPMLogData_Get")
)

// The AdapterInfo record ADL fills is 1572 bytes: the index at offset 4 and the
// adapter's name at offset 280. The driver lists one record per display output,
// so a card with five outputs appears five times; the name is what ties them
// together.
const (
	adlAdapterInfoSize      = 1572
	adlAdapterInfoIndexAt   = 4
	adlAdapterInfoNameAt    = 280
	adlAdapterInfoNameBytes = 256
	adlRetryAfterFailure    = 30 * time.Second
)

// adlAllocate is the allocator ADL insists on being handed. The library never
// calls it for the two calls this reader makes, but it refuses to start
// without one. Callbacks cannot be released, so there is exactly one.
var adlAllocate = sync.OnceValue(func() uintptr {
	return windows.NewCallbackCDecl(func(size uintptr) uintptr {
		handle, err := windows.LocalAlloc(windows.LMEM_FIXED|windows.LMEM_ZEROINIT, uint32(size))
		if err != nil {
			return 0
		}
		return uintptr(handle)
	})
})

type adlReader struct {
	context uintptr
	// candidates are the driver's adapter indices for each device name, in the
	// driver's order; chosen remembers the one whose log answered.
	candidates  map[string][]int
	chosen      map[string]int
	failedAt    time.Time
	unavailable bool
}

func newSensorReader() sensorReader {
	return &adlReader{candidates: make(map[string][]int), chosen: make(map[string]int)}
}

func (r *adlReader) close() {
	if r.context != 0 && procADLMainControlDestroy.Find() == nil {
		procADLMainControlDestroy.Call(r.context)
	}
	r.context = 0
}

// start opens the library once. A failure is remembered for a while rather than
// retried every second: the file's absence is not going to change.
func (r *adlReader) start() bool {
	if r.context != 0 {
		return true
	}
	if r.unavailable || (!r.failedAt.IsZero() && time.Since(r.failedAt) < adlRetryAfterFailure) {
		return false
	}
	if adlDLL.Load() != nil || procADLMainControlCreate.Find() != nil ||
		procADLAdapterNumberOfAdaptersGet.Find() != nil || procADLAdapterAdapterInfoGet.Find() != nil ||
		procADLNewQueryPMLogDataGet.Find() != nil {
		r.unavailable = true
		return false
	}
	var context uintptr
	result, _, _ := procADLMainControlCreate.Call(adlAllocate(), 1, uintptr(unsafe.Pointer(&context)))
	if result != 0 || context == 0 {
		r.failedAt = time.Now()
		return false
	}
	r.context = context
	return true
}

func (r *adlReader) read(gpuName string) gpuSensors {
	if gpuName == "" || !r.start() {
		return gpuSensors{}
	}
	key := normalizeAdapterName(gpuName)
	if _, known := r.candidates[key]; !known {
		r.candidates[key] = r.adaptersNamed(gpuName)
	}
	if index, ok := r.chosen[key]; ok {
		if sensors, ok := r.query(index); ok {
			return sensors
		}
		delete(r.chosen, key)
	}
	// The first record of a card that answers is used, but one that also
	// reports power wins over one that does not.
	var fallback *gpuSensors
	fallbackIndex := -1
	for _, index := range r.candidates[key] {
		sensors, ok := r.query(index)
		if !ok {
			continue
		}
		if sensors.PowerW != nil {
			r.chosen[key] = index
			return sensors
		}
		if fallback == nil {
			fallback, fallbackIndex = &sensors, index
		}
	}
	if fallback != nil {
		r.chosen[key] = fallbackIndex
		return *fallback
	}
	// Nothing answered: the driver may have been restarted underneath the
	// library. Start over after a pause.
	r.close()
	r.candidates = make(map[string][]int)
	r.failedAt = time.Now()
	return gpuSensors{}
}

func (r *adlReader) query(index int) (gpuSensors, bool) {
	buffer := newPMLogBuffer()
	result, _, _ := procADLNewQueryPMLogDataGet.Call(r.context, uintptr(index), uintptr(unsafe.Pointer(&buffer[0])))
	if result != 0 {
		return gpuSensors{}, false
	}
	sensors := sensorsFromPMLog(decodePMLog(buffer))
	return sensors, sensors.Source != ""
}

// adaptersNamed lists the driver's adapter indices whose name is gpuName's.
func (r *adlReader) adaptersNamed(gpuName string) []int {
	var count int32
	if result, _, _ := procADLAdapterNumberOfAdaptersGet.Call(r.context, uintptr(unsafe.Pointer(&count))); result != 0 || count <= 0 || count > 256 {
		return nil
	}
	buffer := make([]byte, int(count)*adlAdapterInfoSize)
	if result, _, _ := procADLAdapterAdapterInfoGet.Call(r.context, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))); result != 0 {
		return nil
	}
	var indices []int
	for record := 0; record < int(count); record++ {
		start := record * adlAdapterInfoSize
		index := int(int32(binary.LittleEndian.Uint32(buffer[start+adlAdapterInfoIndexAt:])))
		name := buffer[start+adlAdapterInfoNameAt : start+adlAdapterInfoNameAt+adlAdapterInfoNameBytes]
		if end := strings.IndexByte(string(name), 0); end >= 0 {
			name = name[:end]
		}
		if index >= 0 && sameAdapterName(string(name), gpuName) {
			indices = append(indices, index)
		}
	}
	return indices
}
