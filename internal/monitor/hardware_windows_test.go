//go:build windows

package monitor

import (
	"testing"
	"time"
)

// The sampler reads this machine, so the assertions are about coherence rather
// than about particular numbers: whatever is reported must be internally
// consistent, and whatever cannot be read must be absent rather than zero.
func TestWindowsSamplerReportsCoherentReadings(t *testing.T) {
	sampler := newHardwareSampler()
	defer sampler.close()

	time.Sleep(250 * time.Millisecond)
	sample := sampler.sample("")

	if sample.RAMTotalBytes == nil || sample.RAMUsedBytes == nil {
		t.Fatal("physical memory is always readable on Windows and was not reported")
	}
	if *sample.RAMUsedBytes > *sample.RAMTotalBytes || *sample.RAMTotalBytes <= 0 {
		t.Fatalf("RAM used %v exceeds total %v", *sample.RAMUsedBytes, *sample.RAMTotalBytes)
	}
	if sample.CommitUsedBytes == nil || sample.CommitLimitBytes == nil || *sample.CommitUsedBytes > *sample.CommitLimitBytes {
		t.Fatalf("commit is incoherent: %v of %v", sample.CommitUsedBytes, sample.CommitLimitBytes)
	}
	for name, value := range map[string]*float64{"gpu": sample.GPUUtil, "cpu": sample.CPUUtil} {
		if value != nil && (*value < 0 || *value > 100) {
			t.Errorf("%s utilization %v is outside 0-100", name, *value)
		}
	}
	if sample.VRAMDedicatedMiB != nil && sample.GPUAdapter == "" {
		t.Error("an adapter reading was reported without saying which adapter it came from")
	}
	if process := sample.ModelProcess; process != nil && (process.Count < 1 || process.PrivateBytes <= 0) {
		t.Errorf("model process is incoherent: %+v", *process)
	}

	info := sampler.info()
	if info.Threads < 1 || (info.Cores > 0 && info.Cores > info.Threads) {
		t.Errorf("processor topology is incoherent: %+v", info)
	}
	t.Logf("machine %+v", info)
	t.Logf("gpu %q adapter %q util %v dedicated %v shared %v total %v", sample.GPUName, sample.GPUAdapter,
		deref(sample.GPUUtil), deref(sample.VRAMDedicatedMiB), deref(sample.VRAMSharedMiB), deref(sample.VRAMTotalMiB))
	t.Logf("cpu %v ram %v/%v commit %v/%v disk r %v w %v", deref(sample.CPUUtil), deref(sample.RAMUsedBytes),
		deref(sample.RAMTotalBytes), deref(sample.CommitUsedBytes), deref(sample.CommitLimitBytes),
		deref(sample.DiskReadBPS), deref(sample.DiskWriteBPS))
	if sample.ModelProcess != nil {
		t.Logf("model process %+v vram %v", *sample.ModelProcess, deref(sample.ModelProcess.VRAMDedicatedMiB))
	}
}

func deref(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
