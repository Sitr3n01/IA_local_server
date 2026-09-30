package monitor

import "testing"

const testAdapter = "luid_0x00000000_0x0001840d_phys_0"

func TestDedicatedByPIDSumsInstancesOfTheChosenAdapterOnly(t *testing.T) {
	got := dedicatedByPID(map[string]float64{
		"pid_4242_" + testAdapter:                  8 * bytesPerMiB * 1024,
		"pid_17_" + testAdapter:                    300 * bytesPerMiB,
		"pid_17_luid_0x00000000_0x0001a846_phys_0": 999 * bytesPerMiB,
		"not a process instance":                   5,
		"pid_99999999999_" + testAdapter:           1,
	}, testAdapter)
	if len(got) != 2 || got[4242] != 8*bytesPerMiB*1024 || got[17] != 300*bytesPerMiB {
		t.Fatalf("dedicated = %v", got)
	}
	if len(dedicatedByPID(nil, "")) != 0 {
		t.Fatal("no adapter should yield nothing")
	}
}

func TestUtilizationByPIDTakesTheBusiestEngineOfEachProcess(t *testing.T) {
	got := utilizationByPID(map[string]float64{
		"pid_4242_" + testAdapter + "_eng_0_engtype_3D":             10,
		"pid_4242_" + testAdapter + "_eng_3_engtype_Compute":        62,
		"pid_4242_" + testAdapter + "_eng_3_engtype_Compute_1":      20,
		"pid_17_" + testAdapter + "_eng_0_engtype_3D":               4,
		"pid_17_luid_0x00000000_0x0001a846_phys_0_eng_0_engtype_3D": 90,
	}, testAdapter)
	if got[4242] != 82 || got[17] != 4 || len(got) != 2 {
		t.Fatalf("utilization = %v", got)
	}
}

func TestBuildGPUProcessesListsTheHeavyAndTheBusyLargestFirst(t *testing.T) {
	table := map[uint32]procEntry{
		100: {PID: 100, Exe: "Bionic.exe"},
		200: {PID: 200, Exe: "chrome.exe"},
		300: {PID: 300, Exe: "dwm.exe"},
		400: {PID: 400, Exe: "quiet.exe"},
	}
	list := buildGPUProcesses(
		map[uint32]float64{100: 9000 * bytesPerMiB, 200: 1200 * bytesPerMiB, 300: 60 * bytesPerMiB, 400: 20 * bytesPerMiB, 500: 4000 * bytesPerMiB},
		map[uint32]float64{100: 88, 300: 12, 400: 0.5},
		table,
		func(pid uint32) string {
			if pid == 100 {
				return `C:\Users\me\AppData\Local\Programs\Bionic\Bionic.exe`
			}
			return ""
		})
	names := []string{}
	for _, process := range list {
		names = append(names, process.Name)
	}
	// 500 is not in the process table: it keeps a placeholder name. 400 is
	// small and idle and stays out; 300 is small but busy and stays in.
	want := []string{"Bionic.exe", "PID 500", "chrome.exe", "dwm.exe"}
	if len(names) != len(want) {
		t.Fatalf("listed %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("listed %v, want %v", names, want)
		}
	}
	if list[0].Tool != toolLMStudio || list[0].GPUUtil == nil || *list[0].GPUUtil != 88 {
		t.Fatalf("first = %+v", list[0])
	}
	if list[1].GPUUtil != nil {
		t.Fatalf("a process with no engine reading got a utilization: %+v", list[1])
	}
}

func TestBuildGPUProcessesIsBounded(t *testing.T) {
	dedicated := map[uint32]float64{}
	for pid := uint32(1); pid <= 40; pid++ {
		dedicated[pid] = float64(pid) * 200 * bytesPerMiB
	}
	list := buildGPUProcesses(dedicated, nil, nil, nil)
	if len(list) != gpuProcessLimit || list[0].PID != 40 {
		t.Fatalf("got %d entries starting at pid %d", len(list), list[0].PID)
	}
}
