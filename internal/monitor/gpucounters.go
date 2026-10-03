package monitor

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// Windows publishes GPU activity per process, per adapter and per engine:
//
//	\GPU Engine(pid_4242_luid_0x00000000_0x0000EAAD_phys_0_eng_3_engtype_Compute_0)\Utilization Percentage
//	\GPU Adapter Memory(luid_0x00000000_0x0000EAAD_phys_0)\Dedicated Usage
//	\GPU Process Memory(pid_4242_luid_0x00000000_0x0000EAAD_phys_0)\Dedicated Usage
//
// The adapter part of the instance name is the key that ties the three
// together, and it is the same string the edge reports as gpu_memory.adapter.
// This file holds the parsing and the arithmetic so they are testable on any
// platform; hardware_windows.go only moves the numbers.

var (
	adapterInstancePattern = regexp.MustCompile(`^luid_0x([0-9A-Fa-f]{8})_0x([0-9A-Fa-f]{8})_phys_\d+$`)
	engineInstancePattern  = regexp.MustCompile(`^pid_(\d+)_(luid_0x[0-9A-Fa-f]{8}_0x[0-9A-Fa-f]{8}_phys_\d+)_eng_(\d+)_engtype_`)
)

// chooseAdapter picks the adapter to describe. The edge's choice wins when it
// is present, so the monitor and the pressure verdict talk about one device.
// Otherwise it applies the edge's own rule - the adapter holding the most
// dedicated memory - with a deterministic tie-break, so an idle host does not
// alternate between instances from one second to the next.
func chooseAdapter(dedicated map[string]float64, preferred string) string {
	if preferred != "" {
		if _, ok := dedicated[preferred]; ok {
			return preferred
		}
	}
	chosen := ""
	chosenBytes := -1.0
	for instance, value := range dedicated {
		if !adapterInstancePattern.MatchString(instance) {
			continue
		}
		if value > chosenBytes || (value == chosenBytes && instance < chosen) {
			chosen = instance
			chosenBytes = value
		}
	}
	return chosen
}

// adapterUtilization is the figure Task Manager shows for a GPU: every process's
// share of an engine is summed, and the adapter is as busy as its busiest
// engine. Averaging engines would report a card saturated on compute as a few
// percent busy, because the copy, video and 3D engines sit idle beside it.
//
// It returns nil when no engine of the adapter produced a reading, which is
// what a rate counter does on its first collection.
func adapterUtilization(engines map[string]float64, adapter string) *float64 {
	if adapter == "" {
		return nil
	}
	perEngine := make(map[string]float64)
	for instance, value := range engines {
		match := engineInstancePattern.FindStringSubmatch(instance)
		if match == nil || match[2] != adapter {
			continue
		}
		perEngine[match[3]] += value
	}
	if len(perEngine) == 0 {
		return nil
	}
	busiest := 0.0
	for _, value := range perEngine {
		if value > busiest {
			busiest = value
		}
	}
	return ptr(clampPercent(busiest))
}

var processInstancePattern = regexp.MustCompile(`^pid_(\d+)_(luid_0x[0-9A-Fa-f]{8}_0x[0-9A-Fa-f]{8}_phys_\d+)$`)

// dedicatedByPID is every process's dedicated memory on one adapter, in bytes.
func dedicatedByPID(processes map[string]float64, adapter string) map[uint32]float64 {
	out := make(map[uint32]float64)
	if adapter == "" {
		return out
	}
	for instance, value := range processes {
		match := processInstancePattern.FindStringSubmatch(instance)
		if match == nil || match[2] != adapter {
			continue
		}
		pid, err := strconv.ParseUint(match[1], 10, 32)
		if err != nil {
			continue
		}
		out[uint32(pid)] += value
	}
	return out
}

// utilizationByPID is each process's busiest engine on one adapter, the same
// rule adapterUtilization applies to the adapter as a whole: a process
// saturating the compute engine is busy, whatever its copy engine does.
func utilizationByPID(engines map[string]float64, adapter string) map[uint32]float64 {
	perEngine := make(map[uint32]map[string]float64)
	for instance, value := range engines {
		match := engineInstancePattern.FindStringSubmatch(instance)
		if match == nil || match[2] != adapter {
			continue
		}
		pid, err := strconv.ParseUint(match[1], 10, 32)
		if err != nil {
			continue
		}
		if perEngine[uint32(pid)] == nil {
			perEngine[uint32(pid)] = make(map[string]float64)
		}
		perEngine[uint32(pid)][match[3]] += value
	}
	out := make(map[uint32]float64, len(perEngine))
	for pid, engines := range perEngine {
		busiest := 0.0
		for _, value := range engines {
			if value > busiest {
				busiest = value
			}
		}
		out[pid] = clampPercent(busiest)
	}
	return out
}

const (
	// A process is listed when it holds at least this much dedicated memory or
	// is doing measurable work. The desktop's many small clients stay out.
	gpuProcessMinMiB  = 100.0
	gpuProcessMinUtil = 3.0
	gpuProcessLimit   = 8
)

// buildGPUProcesses joins the two per-process counters with the process table.
// path resolves an image path for the processes worth naming, and may return
// "" for one that cannot be opened.
func buildGPUProcesses(dedicated, utilization map[uint32]float64, table map[uint32]procEntry, path func(uint32) string) []gpuProcess {
	pids := make(map[uint32]struct{}, len(dedicated)+len(utilization))
	for pid := range dedicated {
		pids[pid] = struct{}{}
	}
	for pid := range utilization {
		pids[pid] = struct{}{}
	}
	list := make([]gpuProcess, 0, len(pids))
	for pid := range pids {
		mib := dedicated[pid] / bytesPerMiB
		util, hasUtil := utilization[pid]
		if mib < gpuProcessMinMiB && !(hasUtil && util >= gpuProcessMinUtil) {
			continue
		}
		entry := table[pid]
		imagePath := ""
		if path != nil {
			imagePath = path(pid)
		}
		name := entry.Exe
		if name == "" && imagePath != "" {
			name = filepath.Base(imagePath)
		}
		if name == "" {
			name = "PID " + strconv.FormatUint(uint64(pid), 10)
		}
		process := gpuProcess{PID: pid, Name: name, Tool: classifyTool(entry.Exe, imagePath), VRAMDedicatedMiB: mib, path: imagePath}
		if hasUtil {
			process.GPUUtil = ptr(util)
		}
		list = append(list, process)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].VRAMDedicatedMiB != list[j].VRAMDedicatedMiB {
			return list[i].VRAMDedicatedMiB > list[j].VRAMDedicatedMiB
		}
		return list[i].PID < list[j].PID
	})
	if len(list) > gpuProcessLimit {
		list = list[:gpuProcessLimit]
	}
	return list
}

// processDedicatedMiB is one process's dedicated memory on one adapter.
func processDedicatedMiB(processes map[string]float64, pid uint32, adapter string) *float64 {
	if adapter == "" || processes == nil {
		return nil
	}
	value, ok := processes["pid_"+strconv.FormatUint(uint64(pid), 10)+"_"+adapter]
	if !ok {
		return nil
	}
	return ptr(value / bytesPerMiB)
}

// parseAdapterLUID recovers the locally unique identifier Windows gives the
// adapter, high part first as the counter names print it.
func parseAdapterLUID(instance string) (high, low uint32, ok bool) {
	match := adapterInstancePattern.FindStringSubmatch(instance)
	if match == nil {
		return 0, 0, false
	}
	highValue, errHigh := strconv.ParseUint(match[1], 16, 32)
	lowValue, errLow := strconv.ParseUint(match[2], 16, 32)
	if errHigh != nil || errLow != nil {
		return 0, 0, false
	}
	return uint32(highValue), uint32(lowValue), true
}
