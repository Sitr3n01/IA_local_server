//go:build !windows

package edge

// Adapter memory is read from the Windows GPU performance counter set, which has
// no portable equivalent. Reporting unavailable keeps the pressure verdict at
// "unknown" rather than at "ok", because a host that cannot be observed has not
// been shown to be healthy.
func gpuMemoryStatus() (gpuMemorySnapshot, error) {
	return gpuMemorySnapshot{}, errGPUMemoryUnavailable
}
