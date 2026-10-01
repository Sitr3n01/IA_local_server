package claudedesktop

import (
	"reflect"
	"testing"
)

func TestClassifyArgumentsReadsTheInstanceSwitches(t *testing.T) {
	helper, dataDir := classifyArguments([]string{
		`C:\Program Files\WindowsApps\Claude_1_x64__x\app\Claude.exe`,
		"--type=renderer",
		`--user-data-dir=C:\Users\me\AppData\Local\Claude-3p`,
		`--desktop-managed-config={"deploymentMode":"3p"}`,
	})
	if !helper || dataDir != `C:\Users\me\AppData\Local\Claude-3p` {
		t.Fatalf("helper=%v dataDir=%q", helper, dataDir)
	}
	if helper, dataDir := classifyArguments([]string{`C:\x\Claude.exe`}); helper || dataDir != "" {
		t.Fatalf("a main process was classified as helper=%v dataDir=%q", helper, dataDir)
	}
}

// The process shape below is the one measured on 2026-10-01 with both
// deployments running: each main process has no switches, and each helper
// names its instance's user-data directory and its main process as parent.
func TestInstanceMainTellsTheTwoDeploymentsApart(t *testing.T) {
	const (
		signedIn = `C:\Users\me\AppData\Roaming\Claude`
		local    = `C:\Users\me\AppData\Local\Claude-3p`
	)
	processes := []packageProcess{
		{pid: 22784, parent: 1},
		{pid: 9364, parent: 22784, helper: true, dataDir: signedIn},
		{pid: 3516, parent: 22784, helper: true, dataDir: signedIn},
		{pid: 20980, parent: 1},
		{pid: 7032, parent: 20980, helper: true, dataDir: local},
	}
	if main := instanceMain(processes, signedIn); main != 22784 {
		t.Fatalf("signed-in main=%d, want 22784", main)
	}
	if main := instanceMain(processes, `c:\users\me\appdata\local\claude-3p\`); main != 20980 {
		t.Fatalf("local main=%d, want 20980 despite case and a trailing separator", main)
	}
	if main := instanceMain(processes[:3], local); main != 0 {
		t.Fatalf("a local main was found where none runs: %d", main)
	}
}

func TestInstanceMainIgnoresAHelperWhoseParentIsNotAPackageMain(t *testing.T) {
	processes := []packageProcess{
		{pid: 7032, parent: 4444, helper: true, dataDir: `C:\Local\Claude-3p`},
	}
	if main := instanceMain(processes, `C:\Local\Claude-3p`); main != 0 {
		t.Fatalf("an orphaned helper's parent PID was trusted: %d", main)
	}
}

func TestUnclaimedMainsAreOnlyMainsWithoutHelpers(t *testing.T) {
	processes := []packageProcess{
		{pid: 22784, parent: 1},
		{pid: 9364, parent: 22784, helper: true, dataDir: `C:\Roaming\Claude`},
		{pid: 31000, parent: 1},
	}
	if got := unclaimedMains(processes); !reflect.DeepEqual(got, []uint32{31000}) {
		t.Fatalf("unclaimed mains=%v, want only the new process", got)
	}
}
