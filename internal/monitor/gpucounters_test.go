package monitor

import "testing"

const (
	discrete   = "luid_0x00000000_0x00018459_phys_0"
	integrated = "luid_0x00000000_0x0000D4F2_phys_0"
)

func TestChooseAdapterPrefersTheEdgesChoice(t *testing.T) {
	dedicated := map[string]float64{discrete: 4 << 30, integrated: 512 << 20}
	if got := chooseAdapter(dedicated, integrated); got != integrated {
		t.Fatalf("chooseAdapter kept %q, want the edge's %q", got, integrated)
	}
}

func TestChooseAdapterFallsBackToTheBusiestAdapter(t *testing.T) {
	dedicated := map[string]float64{discrete: 4 << 30, integrated: 512 << 20, "not-an-adapter": 64 << 30}
	if got := chooseAdapter(dedicated, "luid_0x00000000_0x0000FFFF_phys_0"); got != discrete {
		t.Fatalf("chooseAdapter = %q, want the busiest real adapter %q", got, discrete)
	}
}

// The tie-break is the edge's: the lexically smaller instance wins, so both
// processes land on the same adapter when nothing separates them.
func TestChooseAdapterBreaksTiesTheSameWayEveryTime(t *testing.T) {
	dedicated := map[string]float64{discrete: 0, integrated: 0}
	for attempt := 0; attempt < 50; attempt++ {
		if got := chooseAdapter(dedicated, ""); got != integrated {
			t.Fatalf("attempt %d chose %q; an idle host must not alternate", attempt, got)
		}
	}
	if got := chooseAdapter(nil, ""); got != "" {
		t.Fatalf("no adapters chose %q", got)
	}
}

func TestAdapterUtilizationIsTheBusiestEngineSummedAcrossProcesses(t *testing.T) {
	engines := map[string]float64{
		"pid_100_" + discrete + "_eng_0_engtype_3D":          7,
		"pid_200_" + discrete + "_eng_0_engtype_3D":          5,
		"pid_300_" + discrete + "_eng_3_engtype_Compute_0":   61,
		"pid_300_" + discrete + "_eng_4_engtype_Compute_1":   20,
		"pid_100_" + integrated + "_eng_0_engtype_3D":        99,
		"pid_400_" + discrete + "_eng_5_engtype_VideoDecode": 0,
	}
	got := adapterUtilization(engines, discrete)
	if got == nil || *got != 61 {
		t.Fatalf("utilization = %v, want 61: the compute engine, not an average and not the other adapter", got)
	}
}

func TestAdapterUtilizationClampsAndReportsAbsence(t *testing.T) {
	engines := map[string]float64{
		"pid_1_" + discrete + "_eng_0_engtype_3D": 70,
		"pid_2_" + discrete + "_eng_0_engtype_3D": 45,
	}
	if got := adapterUtilization(engines, discrete); got == nil || *got != 100 {
		t.Fatalf("utilization = %v, want the sum clamped to 100", got)
	}
	if got := adapterUtilization(engines, integrated); got != nil {
		t.Fatalf("an adapter with no engine readings reported %v, want nil", *got)
	}
	if got := adapterUtilization(nil, ""); got != nil {
		t.Fatalf("no adapter reported %v", *got)
	}
}

func TestProcessDedicatedMiBReadsOneProcessOnOneAdapter(t *testing.T) {
	processes := map[string]float64{
		"pid_4242_" + discrete:   12 << 30,
		"pid_4242_" + integrated: 64 << 20,
		"pid_77_" + discrete:     1 << 30,
	}
	got := processDedicatedMiB(processes, 4242, discrete)
	if got == nil || *got != 12*1024 {
		t.Fatalf("process VRAM = %v MiB, want 12288", got)
	}
	if got := processDedicatedMiB(processes, 9, discrete); got != nil {
		t.Fatalf("an absent process reported %v", *got)
	}
}

func TestParseAdapterLUIDReadsHighPartFirst(t *testing.T) {
	high, low, ok := parseAdapterLUID("luid_0x00000001_0x00018459_phys_0")
	if !ok || high != 1 || low != 0x18459 {
		t.Fatalf("parseAdapterLUID = %#x %#x %v", high, low, ok)
	}
	for _, bad := range []string{"", "luid_0x1_0x2_phys_0", "pid_1_luid_0x00000000_0x00018459_phys_0", "luid_0x00000000_0x00018459"} {
		if _, _, ok := parseAdapterLUID(bad); ok {
			t.Errorf("parseAdapterLUID(%q) accepted a malformed instance", bad)
		}
	}
}
