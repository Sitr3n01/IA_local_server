package monitor

// hardwareSample is one second of machine state. Every measurement is a
// pointer so "could not be read" reaches the page as null - shown as a dash -
// and never as a plausible zero.
type hardwareSample struct {
	GPUName          string         `json:"gpu_name,omitempty"`
	GPUAdapter       string         `json:"-"`
	GPUUtil          *float64       `json:"gpu_util"`
	VRAMDedicatedMiB *float64       `json:"vram_dedicated_mib"`
	VRAMSharedMiB    *float64       `json:"vram_shared_mib"`
	VRAMTotalMiB     *float64       `json:"vram_total_mib"`
	CPUUtil          *float64       `json:"cpu_util"`
	RAMTotalBytes    *float64       `json:"ram_total_bytes"`
	RAMUsedBytes     *float64       `json:"ram_used_bytes"`
	CommitUsedBytes  *float64       `json:"commit_used_bytes"`
	CommitLimitBytes *float64       `json:"commit_limit_bytes"`
	DiskReadBPS      *float64       `json:"disk_read_bytes_per_second"`
	DiskWriteBPS     *float64       `json:"disk_write_bytes_per_second"`
	ModelProcess     *processSample `json:"model_process"`

	// Driver-reported readings for the same adapter: what the card draws, how
	// hot it is and how fast it runs. PowerSource names the interface they came
	// from and is empty when the driver offered none.
	PowerW      *float64 `json:"power_w"`
	GPUTempC    *float64 `json:"gpu_temp_c"`
	GPUHotspotC *float64 `json:"gpu_hotspot_c"`
	GPUClockMHz *float64 `json:"gpu_clock_mhz"`
	MemClockMHz *float64 `json:"mem_clock_mhz"`
	PowerSource string   `json:"power_source,omitempty"`

	// GPUProcesses is who is using the adapter, whatever program it is. It is
	// what makes the page independent of the tool running the model.
	GPUProcesses []gpuProcess `json:"gpu_processes"`
}

// gpuProcess is one process's share of the adapter. Tool is set when the image
// is a known inference tool; path is kept for grouping and never sent.
type gpuProcess struct {
	PID              uint32   `json:"pid"`
	Name             string   `json:"name"`
	Tool             string   `json:"tool,omitempty"`
	VRAMDedicatedMiB float64  `json:"vram_dedicated_mib"`
	GPUUtil          *float64 `json:"gpu_util"`
	path             string
}

// processSample describes the llama-server process that holds the model. When
// more than one is running - a probe beside the router, say - the largest is
// described and Count says the figure is not the whole story.
type processSample struct {
	PID              uint32   `json:"pid"`
	Count            int      `json:"count"`
	WorkingSetBytes  float64  `json:"working_set_bytes"`
	PrivateBytes     float64  `json:"private_bytes"`
	VRAMDedicatedMiB *float64 `json:"vram_dedicated_mib"`
}

// machineInfo is what does not change while the monitor runs.
type machineInfo struct {
	CPUName string `json:"cpu_name,omitempty"`
	Cores   int    `json:"cores,omitempty"`
	Threads int    `json:"threads,omitempty"`
}

// hardwareSampler is the platform half of the collector. preferredAdapter is
// the adapter the edge judged, when it reported one, so both processes describe
// the same device instead of each picking its own.
type hardwareSampler interface {
	sample(preferredAdapter string) hardwareSample
	info() machineInfo
	close()
}

const bytesPerMiB = 1024 * 1024

func ptr(value float64) *float64 { return &value }

func clampPercent(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 100:
		return 100
	default:
		return value
	}
}
