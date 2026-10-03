//go:build windows

package monitor

import (
	"encoding/binary"
	"math"
	"math/bits"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Counters are added with PdhAddEnglishCounterW for the reason internal/edge
// gives: counter paths are localized, and the reference workstation runs a
// pt-BR Windows on which the English path through PdhAddCounterW does not
// resolve. One query is kept open for the life of the process because three of
// these are rates, and a rate needs two collections to exist.
var (
	pdhDLL                           = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = pdhDLL.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = pdhDLL.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = pdhDLL.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue  = pdhDLL.NewProc("PdhGetFormattedCounterValue")
	procPdhGetFormattedCounterArrayW = pdhDLL.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = pdhDLL.NewProc("PdhCloseQuery")

	kernel32DLL                          = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx             = kernel32DLL.NewProc("GlobalMemoryStatusEx")
	procK32GetProcessMemoryInfo          = kernel32DLL.NewProc("K32GetProcessMemoryInfo")
	procGetLogicalProcessorInformationEx = kernel32DLL.NewProc("GetLogicalProcessorInformationEx")
	procQueryFullProcessImageNameW       = kernel32DLL.NewProc("QueryFullProcessImageNameW")

	// The adapter's name and size come from the kernel-mode thunks rather than
	// DXGI: they take the LUID the counters already print, and they are plain
	// calls where DXGI would need COM interop for the same two facts.
	gdi32DLL                      = windows.NewLazySystemDLL("gdi32.dll")
	procD3DKMTOpenAdapterFromLuid = gdi32DLL.NewProc("D3DKMTOpenAdapterFromLuid")
	procD3DKMTQueryAdapterInfo    = gdi32DLL.NewProc("D3DKMTQueryAdapterInfo")
	procD3DKMTCloseAdapter        = gdi32DLL.NewProc("D3DKMTCloseAdapter")
)

const (
	pdhFmtDouble  = 0x00000200
	pdhMoreData   = 0x800007D2
	pdhCStatusOK  = 0x00000000
	pdhCStatusNew = 0x00000001

	counterGPUEngine        = `\GPU Engine(*)\Utilization Percentage`
	counterAdapterDedicated = `\GPU Adapter Memory(*)\Dedicated Usage`
	counterAdapterShared    = `\GPU Adapter Memory(*)\Shared Usage`
	counterProcessDedicated = `\GPU Process Memory(*)\Dedicated Usage`
	// % Processor Utility, not % Processor Time: it is the figure Task Manager
	// shows, scaled by the frequency the cores actually ran at.
	counterCPU       = `\Processor Information(_Total)\% Processor Utility`
	counterDiskRead  = `\PhysicalDisk(_Total)\Disk Read Bytes/sec`
	counterDiskWrite = `\PhysicalDisk(_Total)\Disk Write Bytes/sec`

	modelProcessName = "llama-server.exe"

	kmtQueryGetSegmentSize          = 3
	kmtQueryAdapterRegistryInfo     = 8
	relationProcessorCore           = 0
	processorRelationshipMaskOffset = 32
	groupAffinitySize               = 16
)

// pdhCounterValueDouble mirrors PDH_FMT_COUNTERVALUE; see internal/edge for why
// the padding word matters.
type pdhCounterValueDouble struct {
	CStatus     uint32
	_           uint32
	DoubleValue float64
}

type pdhCounterValueItemDouble struct {
	Name  *uint16
	Value pdhCounterValueDouble
}

type memoryStatusEx struct {
	Length            uint32
	MemoryLoad        uint32
	TotalPhysical     uint64
	AvailablePhysical uint64
	TotalPageFile     uint64
	AvailablePageFile uint64
	TotalVirtual      uint64
	AvailableVirtual  uint64
	AvailableExtended uint64
}

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS_EX.
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
	PrivateUsage               uintptr
}

type d3dkmtOpenAdapterFromLUID struct {
	AdapterLUID windows.LUID
	Adapter     uint32
}

type d3dkmtQueryAdapterInfo struct {
	Adapter  uint32
	Type     uint32
	Data     unsafe.Pointer
	DataSize uint32
}

type d3dkmtCloseAdapter struct {
	Adapter uint32
}

type d3dkmtAdapterRegistryInfo struct {
	AdapterString [windows.MAX_PATH]uint16
	BIOSString    [windows.MAX_PATH]uint16
	DACType       [windows.MAX_PATH]uint16
	ChipType      [windows.MAX_PATH]uint16
}

type d3dkmtSegmentSizeInfo struct {
	DedicatedVideoMemorySize  uint64
	DedicatedSystemMemorySize uint64
	SharedSystemMemorySize    uint64
}

type adapterIdentity struct {
	name     string
	totalMiB float64
}

type windowsSampler struct {
	mu       sync.Mutex
	query    uintptr
	counters map[string]uintptr
	adapters map[string]adapterIdentity
	machine  machineInfo
	sensors  sensorReader
	paths    imagePaths
}

func newHardwareSampler() hardwareSampler {
	sampler := &windowsSampler{
		counters: make(map[string]uintptr),
		adapters: make(map[string]adapterIdentity),
		machine:  readMachineInfo(),
		sensors:  newSensorReader(),
		paths:    imagePaths{known: make(map[uint32]cachedPath)},
	}
	var query uintptr
	if pdhCall(procPdhOpenQueryW, 0, 0, uintptr(unsafe.Pointer(&query))) != 0 {
		// Memory and the model process are still readable without PDH.
		return sampler
	}
	sampler.query = query
	// Each counter is optional on its own: a virtual machine without a WDDM
	// driver has no GPU counter set and still has a CPU.
	for _, path := range []string{counterGPUEngine, counterAdapterDedicated, counterAdapterShared,
		counterProcessDedicated, counterCPU, counterDiskRead, counterDiskWrite} {
		pathPtr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		var counter uintptr
		if pdhCall(procPdhAddEnglishCounterW, query, uintptr(unsafe.Pointer(pathPtr)), 0, uintptr(unsafe.Pointer(&counter))) == 0 {
			sampler.counters[path] = counter
		}
	}
	// Prime the rates, so the first real sample a second from now has a
	// previous collection to be measured against.
	pdhCall(procPdhCollectQueryData, query)
	return sampler
}

func (s *windowsSampler) info() machineInfo { return s.machine }

func (s *windowsSampler) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sensors.close()
	if s.query != 0 {
		pdhCall(procPdhCloseQuery, s.query)
		s.query = 0
	}
}

func (s *windowsSampler) sample(preferredAdapter string) hardwareSample {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out hardwareSample
	readSystemMemory(&out)
	table := snapshotProcesses()

	var processVRAM map[string]float64
	if s.query != 0 && pdhCall(procPdhCollectQueryData, s.query) == 0 {
		dedicated := formattedArray(s.counters[counterAdapterDedicated])
		adapter := chooseAdapter(dedicated, preferredAdapter)
		if adapter != "" {
			out.GPUAdapter = adapter
			out.VRAMDedicatedMiB = ptr(dedicated[adapter] / bytesPerMiB)
			if shared, ok := formattedArray(s.counters[counterAdapterShared])[adapter]; ok {
				out.VRAMSharedMiB = ptr(shared / bytesPerMiB)
			}
			identity := s.identity(adapter)
			out.GPUName = identity.name
			if identity.totalMiB > 0 {
				out.VRAMTotalMiB = ptr(identity.totalMiB)
			}
			engines := formattedArray(s.counters[counterGPUEngine])
			out.GPUUtil = adapterUtilization(engines, adapter)
			processVRAM = formattedArray(s.counters[counterProcessDedicated])
			out.GPUProcesses = buildGPUProcesses(dedicatedByPID(processVRAM, adapter),
				utilizationByPID(engines, adapter), table, func(pid uint32) string { return s.paths.lookup(pid, table[pid].Exe) })
			readings := s.sensors.read(identity.name)
			out.PowerW, out.GPUTempC, out.GPUHotspotC = readings.PowerW, readings.TempC, readings.HotspotC
			out.GPUClockMHz, out.MemClockMHz, out.PowerSource = readings.GPUClockMHz, readings.MemClockMHz, readings.Source
		}
		if cpu := formattedValue(s.counters[counterCPU]); cpu != nil {
			out.CPUUtil = ptr(clampPercent(*cpu))
		}
		out.DiskReadBPS = formattedValue(s.counters[counterDiskRead])
		out.DiskWriteBPS = formattedValue(s.counters[counterDiskWrite])
	}
	out.ModelProcess = readModelProcess(table, processVRAM, out.GPUAdapter)
	return out
}

// identity is cached per adapter: a device's name and size do not change while
// it is plugged in, and the lookup opens a kernel handle.
func (s *windowsSampler) identity(adapter string) adapterIdentity {
	if identity, ok := s.adapters[adapter]; ok {
		return identity
	}
	identity := queryAdapterIdentity(adapter)
	s.adapters[adapter] = identity
	return identity
}

func pdhCall(proc *windows.LazyProc, args ...uintptr) uint32 {
	ret, _, _ := proc.Call(args...)
	return uint32(ret)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// formattedValue reads a single-instance counter. A rate without a previous
// collection, or any other invalid reading, is nil rather than zero.
func formattedValue(counter uintptr) *float64 {
	if counter == 0 {
		return nil
	}
	var value pdhCounterValueDouble
	if pdhCall(procPdhGetFormattedCounterValue, counter, pdhFmtDouble, 0, uintptr(unsafe.Pointer(&value))) != 0 {
		return nil
	}
	if (value.CStatus != pdhCStatusOK && value.CStatus != pdhCStatusNew) || !finite(value.DoubleValue) || value.DoubleValue < 0 {
		return nil
	}
	return ptr(value.DoubleValue)
}

// formattedArray reads every instance of a wildcard counter. Instances with an
// invalid status are left out, so a missing key means "not measured".
func formattedArray(counter uintptr) map[string]float64 {
	if counter == 0 {
		return nil
	}
	var bufferSize, itemCount uint32
	status := pdhCall(procPdhGetFormattedCounterArrayW, counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)), 0)
	if status != pdhMoreData || bufferSize == 0 || itemCount == 0 {
		return nil
	}
	buffer := make([]byte, bufferSize)
	status = pdhCall(procPdhGetFormattedCounterArrayW, counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buffer[0])))
	if status != 0 {
		return nil
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
		if item.Name == nil || !finite(item.Value.DoubleValue) || item.Value.DoubleValue < 0 {
			continue
		}
		values[windows.UTF16PtrToString(item.Name)] = item.Value.DoubleValue
	}
	return values
}

func readSystemMemory(out *hardwareSample) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if result, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); result == 0 {
		return
	}
	if status.TotalPhysical > 0 && status.AvailablePhysical <= status.TotalPhysical {
		out.RAMTotalBytes = ptr(float64(status.TotalPhysical))
		out.RAMUsedBytes = ptr(float64(status.TotalPhysical - status.AvailablePhysical))
	}
	// The commit limit is what the edge's admission reasons about: a model that
	// fits in RAM can still be refused for want of commit.
	if status.TotalPageFile > 0 && status.AvailablePageFile <= status.TotalPageFile {
		out.CommitLimitBytes = ptr(float64(status.TotalPageFile))
		out.CommitUsedBytes = ptr(float64(status.TotalPageFile - status.AvailablePageFile))
	}
}

// readModelProcess finds llama-server by image name. It reads sizes only:
// the process is opened for limited query access, never for reading memory.
func readModelProcess(table map[uint32]procEntry, processVRAM map[string]float64, adapter string) *processSample {
	var best *processSample
	count := 0
	for _, entry := range table {
		if !strings.EqualFold(entry.Exe, modelProcessName) {
			continue
		}
		count++
		counters, ok := processMemory(entry.PID)
		if !ok {
			continue
		}
		if best == nil || float64(counters.PrivateUsage) > best.PrivateBytes {
			best = &processSample{
				PID:             entry.PID,
				WorkingSetBytes: float64(counters.WorkingSetSize),
				PrivateBytes:    float64(counters.PrivateUsage),
			}
		}
	}
	if best == nil {
		return nil
	}
	best.Count = count
	best.VRAMDedicatedMiB = processDedicatedMiB(processVRAM, best.PID, adapter)
	return best
}

// snapshotProcesses is the machine's process table: names and parents, which
// need no access to any process.
func snapshotProcesses() map[uint32]procEntry {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return map[uint32]procEntry{}
	}
	defer windows.CloseHandle(snapshot)
	table := make(map[uint32]procEntry, 256)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		table[entry.ProcessID] = procEntry{PID: entry.ProcessID, PPID: entry.ParentProcessID, Exe: windows.UTF16ToString(entry.ExeFile[:])}
	}
	return table
}

// imagePaths remembers where a process's program lives. Asking means opening
// the process, so a pid is asked about once per program: a pid reused by another
// image is noticed by its name changing.
type imagePaths struct {
	known map[uint32]cachedPath
}

type cachedPath struct {
	exe  string
	path string
}

func (p *imagePaths) lookup(pid uint32, exe string) string {
	if cached, ok := p.known[pid]; ok && cached.exe == exe {
		return cached.path
	}
	if len(p.known) > 4096 {
		p.known = make(map[uint32]cachedPath)
	}
	path := queryImagePath(pid)
	p.known[pid] = cachedPath{exe: exe, path: path}
	return path
}

// queryImagePath asks for the full image name with the least access there is.
// A process of another account or a protected one refuses, and the caller then
// works from the name alone.
func queryImagePath(pid uint32) string {
	if pid == 0 || procQueryFullProcessImageNameW.Find() != nil {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if result, _, _ := procQueryFullProcessImageNameW.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size))); result == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

func processMemory(pid uint32) (processMemoryCounters, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return processMemoryCounters{}, false
	}
	defer windows.CloseHandle(handle)
	counters := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB))
	return counters, result != 0
}

func queryAdapterIdentity(adapter string) adapterIdentity {
	high, low, ok := parseAdapterLUID(adapter)
	if !ok || procD3DKMTOpenAdapterFromLuid.Find() != nil || procD3DKMTQueryAdapterInfo.Find() != nil || procD3DKMTCloseAdapter.Find() != nil {
		return adapterIdentity{}
	}
	open := d3dkmtOpenAdapterFromLUID{AdapterLUID: windows.LUID{LowPart: low, HighPart: int32(high)}}
	if status, _, _ := procD3DKMTOpenAdapterFromLuid.Call(uintptr(unsafe.Pointer(&open))); status != 0 {
		return adapterIdentity{}
	}
	defer closeAdapter(open.Adapter)

	var identity adapterIdentity
	var registryInfo d3dkmtAdapterRegistryInfo
	if queryAdapterInfo(open.Adapter, kmtQueryAdapterRegistryInfo, unsafe.Pointer(&registryInfo), unsafe.Sizeof(registryInfo)) {
		identity.name = cleanName(windows.UTF16ToString(registryInfo.AdapterString[:]))
	}
	var segments d3dkmtSegmentSizeInfo
	if queryAdapterInfo(open.Adapter, kmtQueryGetSegmentSize, unsafe.Pointer(&segments), unsafe.Sizeof(segments)) {
		identity.totalMiB = float64(segments.DedicatedVideoMemorySize) / bytesPerMiB
	}
	return identity
}

func queryAdapterInfo(adapter, kind uint32, data unsafe.Pointer, size uintptr) bool {
	query := d3dkmtQueryAdapterInfo{Adapter: adapter, Type: kind, Data: data, DataSize: uint32(size)}
	status, _, _ := procD3DKMTQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&query)))
	return status == 0
}

func closeAdapter(adapter uint32) {
	request := d3dkmtCloseAdapter{Adapter: adapter}
	procD3DKMTCloseAdapter.Call(uintptr(unsafe.Pointer(&request)))
}

// cleanName collapses the padding some drivers and CPUs put in their names.
func cleanName(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func readMachineInfo() machineInfo {
	info := machineInfo{CPUName: readCPUName()}
	info.Cores, info.Threads = readProcessorTopology()
	if info.Threads == 0 {
		info.Threads = runtime.NumCPU()
	}
	return info
}

func readCPUName() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	name, _, err := key.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return cleanName(name)
}

// readProcessorTopology counts physical cores and the logical processors they
// carry. runtime.NumCPU would answer the second question only, and only for
// the processor group this process was placed in.
func readProcessorTopology() (cores, threads int) {
	if procGetLogicalProcessorInformationEx.Find() != nil {
		return 0, 0
	}
	var length uint32
	procGetLogicalProcessorInformationEx.Call(relationProcessorCore, 0, uintptr(unsafe.Pointer(&length)))
	if length == 0 {
		return 0, 0
	}
	buffer := make([]byte, length)
	if result, _, _ := procGetLogicalProcessorInformationEx.Call(relationProcessorCore,
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&length))); result == 0 {
		return 0, 0
	}
	for offset := uint32(0); offset+8 <= length; {
		relationship := binary.LittleEndian.Uint32(buffer[offset:])
		size := binary.LittleEndian.Uint32(buffer[offset+4:])
		if size == 0 || offset+size > length {
			break
		}
		if relationship == relationProcessorCore && size >= processorRelationshipMaskOffset {
			cores++
			groups := uint32(binary.LittleEndian.Uint16(buffer[offset+30:]))
			for group := uint32(0); group < groups; group++ {
				start := offset + processorRelationshipMaskOffset + group*groupAffinitySize
				if start+8 > offset+size {
					break
				}
				threads += bits.OnesCount64(binary.LittleEndian.Uint64(buffer[start:]))
			}
		}
		offset += size
	}
	return cores, threads
}
