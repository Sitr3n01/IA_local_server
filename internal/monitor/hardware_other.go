//go:build !windows

package monitor

import "runtime"

// On other platforms the monitor still serves the edge's telemetry; the
// machine cards read as unavailable rather than as zero.
type unavailableSampler struct{}

func newHardwareSampler() hardwareSampler { return unavailableSampler{} }

func (unavailableSampler) sample(string) hardwareSample { return hardwareSample{} }

func (unavailableSampler) info() machineInfo { return machineInfo{Threads: runtime.NumCPU()} }

func (unavailableSampler) close() {}
