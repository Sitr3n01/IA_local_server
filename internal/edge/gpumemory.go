package edge

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// The edge has always compared a manifest-declared VRAM peak against a
// manifest-declared device budget, both static and both derived from llama.cpp's
// own load-time buffer sizes.
//
// llama.cpp's figures are accurate for llama.cpp. Measured against the adapter,
// its reported charge (13012 MiB) matches the marginal dedicated memory the
// model actually cost (12961 MiB) to within 0.4%. What they omit is everything
// else on the card: this workstation's desktop compositor and browser hold
// roughly 3.0 GiB before a model loads, so a 12.7 GiB model on a 15.9 GiB
// adapter lands at 97-98% occupancy rather than the 80% the load log implies.
// Past that point the AMD driver pages the excess over PCIe, prompt processing
// loses a factor of three, and nothing raises an error
// (benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md).
//
// A static manifest cannot see the desktop, which is why the live reading below
// exists. It is the only place the deployment learns how much of the card it is
// actually competing for.
//
// This file adds the missing live measurement. It is observability, not
// admission: the verdict is reported through /api/v1/status and the metrics
// endpoint so an operator can see degradation that is otherwise invisible, and
// it never refuses a request or unloads a model. Acting on one instant of a
// noisy signal would trade a silent slowdown for a silent outage, which is a
// worse failure and a policy decision the operator is better placed to make.

// gpuMemorySnapshot is one adapter-level reading. Both fields come from the same
// probe call so they describe the same instant; a caller must not mix a
// dedicated value from one sample with a shared value from another.
type gpuMemorySnapshot struct {
	// Adapter is the device instance the reading came from, for the case where
	// a host exposes more than one GPU. It is diagnostic only.
	Adapter      string
	DedicatedMiB float64
	SharedMiB    float64
}

// Thresholds are derived from measured configurations on the reference
// workstation rather than chosen for roundness:
//
//	idle desktop        19.1% dedicated,  412 MiB shared   healthy
//	4-block @ 512 ctx   96.4% dedicated,  514 MiB shared   1034 t/s pp512, healthy
//	4-block @ 32k ctx   97.6% dedicated, 1253 MiB shared    195 t/s pp32768, degraded
//	full residency      98.6% dedicated, 1350 MiB shared    270 t/s pp512, degraded
//
// Occupancy alone cannot separate these: the fastest configuration measured sits
// at 96.4%. Shared usage separates them, but only above the idle baseline, since
// a desktop compositor holds a few hundred mebibytes with no model loaded at
// all. The floor therefore sits between the healthy 514 MiB case and the
// degraded 1253 MiB one. Keep this table in step with the same constants in
// scripts/v2/Telemetry.ps1; the two are expected to agree.
const (
	gpuOccupancyWarnRatio = 0.95
	gpuSharedFloorMiB     = 1024.0
)

// Pressure states. Unknown is distinct from ok on purpose: a host with no WDDM
// driver, or a probe that failed, has not been shown to be healthy.
const (
	gpuPressureUnknown   = "unknown"
	gpuPressureOK        = "ok"
	gpuPressureElevated  = "elevated"
	gpuPressurePressured = "pressured"
)

// gpuPressureStatus is the operator-facing verdict. Pointers are nil when the
// probe produced nothing, so a report can distinguish "not measured" from
// "measured and empty" - the same discipline the resource profile fields use.
type gpuPressureStatus struct {
	State        string   `json:"state"`
	Occupancy    *float64 `json:"occupancy,omitempty"`
	DedicatedMiB *float64 `json:"dedicated_mib,omitempty"`
	SharedMiB    *float64 `json:"shared_mib,omitempty"`
	BudgetMiB    *float64 `json:"budget_mib,omitempty"`
	Adapter      string   `json:"adapter,omitempty"`
	Message      string   `json:"message"`
}

// classifyGPUPressure turns a snapshot into a verdict against a declared budget.
//
// budgetMiB is the manifest's runtimes[].device.vram_mib rather than a
// driver-reported total, so the verdict is measured against the same number
// admission control uses. A non-positive budget yields unknown: there is nothing
// to compare against, and inferring one from the driver would reintroduce the
// disagreement this probe exists to expose.
func classifyGPUPressure(snapshot gpuMemorySnapshot, budgetMiB float64, probeErr error) gpuPressureStatus {
	if probeErr != nil {
		return gpuPressureStatus{
			State:   gpuPressureUnknown,
			Message: "GPU adapter memory is not observable on this host",
		}
	}
	if budgetMiB <= 0 {
		return gpuPressureStatus{
			State:        gpuPressureUnknown,
			DedicatedMiB: floatPointer(snapshot.DedicatedMiB),
			SharedMiB:    floatPointer(snapshot.SharedMiB),
			Adapter:      snapshot.Adapter,
			Message:      "no device VRAM budget is declared, so adapter usage cannot be classified",
		}
	}

	occupancy := snapshot.DedicatedMiB / budgetMiB
	status := gpuPressureStatus{
		Occupancy:    floatPointer(roundGiB(occupancy)),
		DedicatedMiB: floatPointer(snapshot.DedicatedMiB),
		SharedMiB:    floatPointer(snapshot.SharedMiB),
		BudgetMiB:    floatPointer(budgetMiB),
		Adapter:      snapshot.Adapter,
	}

	// Both conditions are required for pressured. Shared usage on its own rises
	// with ordinary desktop compositing and says nothing about the model; high
	// occupancy on its own is the intended state of a configuration sized to the
	// card. Driver paging is what the pair identifies.
	switch {
	case occupancy >= gpuOccupancyWarnRatio && snapshot.SharedMiB >= gpuSharedFloorMiB:
		status.State = gpuPressurePressured
		status.Message = fmt.Sprintf(
			"GPU memory pressure detected: %.0f of %.0f MiB dedicated (%.1f%%), %.0f MiB shared. "+
				"Driver-level VRAM paging is likely; prompt processing degrades without an error being raised. "+
				"Consider a smaller context profile or a wider CPU tensor split.",
			snapshot.DedicatedMiB, budgetMiB, occupancy*100, snapshot.SharedMiB)
	case occupancy >= gpuOccupancyWarnRatio:
		status.State = gpuPressureElevated
		status.Message = fmt.Sprintf(
			"GPU memory is elevated: %.0f of %.0f MiB dedicated (%.1f%%), %.0f MiB shared.",
			snapshot.DedicatedMiB, budgetMiB, occupancy*100, snapshot.SharedMiB)
	default:
		status.State = gpuPressureOK
		status.Message = fmt.Sprintf(
			"GPU memory is within budget: %.0f of %.0f MiB dedicated (%.1f%%), %.0f MiB shared.",
			snapshot.DedicatedMiB, budgetMiB, occupancy*100, snapshot.SharedMiB)
	}
	return status
}

// deviceVRAMBudgetMiB reports the budget to classify against, taken from the
// model that is actually loaded. Models can declare different device budgets
// when they run on different runtimes, so classifying against an arbitrary one
// would compare a reading to the wrong card.
func (s *Server) deviceVRAMBudgetMiB(activeModel string) float64 {
	if activeModel != "" {
		if model, ok := s.modelByID(activeModel); ok && model.DeviceVRAMGiB != nil {
			return *model.DeviceVRAMGiB * 1024
		}
	}
	return 0
}

var errGPUMemoryUnavailable = errors.New("GPU adapter memory counters are unavailable on this platform")

// gpuMemoryCacheTTL throttles the probe. A PDH query costs a few milliseconds,
// which is nothing against one request but is worth avoiding when the tray
// dashboard polls /api/v1/status on a timer. Nothing here samples on its own
// schedule: the probe runs when a status or metrics reader asks for it and not
// otherwise, so an idle deployment performs no GPU work at all.
const gpuMemoryCacheTTL = 2 * time.Second

type gpuMemoryCache struct {
	mu       sync.Mutex
	at       time.Time
	snapshot gpuMemorySnapshot
	err      error
	// now is injectable so the cache can be tested without sleeping.
	now func() time.Time
}

func (c *gpuMemoryCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// sample returns a snapshot no older than gpuMemoryCacheTTL, calling probe at
// most once per interval. A failed probe is cached too: a host without the GPU
// counter set fails every time, and retrying it per request would turn a missing
// capability into a per-request cost.
func (c *gpuMemoryCache) sample(probe func() (gpuMemorySnapshot, error)) (gpuMemorySnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if !c.at.IsZero() && now.Sub(c.at) < gpuMemoryCacheTTL {
		return c.snapshot, c.err
	}
	snapshot, err := probe()
	c.at = now
	c.snapshot = snapshot
	c.err = err
	return snapshot, err
}

// gpuPressure is the verdict reported through the control surface.
func (s *Server) gpuPressure(activeModel string) gpuPressureStatus {
	if s.gpuMemory == nil {
		return gpuPressureStatus{State: gpuPressureUnknown, Message: "GPU adapter memory probing is disabled"}
	}
	snapshot, err := s.gpuCache.sample(s.gpuMemory)
	return classifyGPUPressure(snapshot, s.deviceVRAMBudgetMiB(activeModel), err)
}

// gpuPressureLevel maps a state to an ordered gauge, so a scrape can alert on
// "at least elevated" without string matching. Unknown is -1 rather than 0
// because an unobservable host is not a healthy one, and a dashboard that
// averages the series should not read it as ok.
func gpuPressureLevel(state string) int {
	switch state {
	case gpuPressureOK:
		return 0
	case gpuPressureElevated:
		return 1
	case gpuPressurePressured:
		return 2
	default:
		return -1
	}
}
