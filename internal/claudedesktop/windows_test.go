//go:build windows

package claudedesktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLiveInstancesAreFoundByTheirDataDirectory reads the real process table.
// It is opt-in because it needs an installed, running Claude Desktop, and it
// is read-only: it neither launches nor foregrounds anything.
func TestLiveInstancesAreFoundByTheirDataDirectory(t *testing.T) {
	if os.Getenv("CIA_CLAUDE_LIVE_TEST") != "1" {
		t.Skip("set CIA_CLAUDE_LIVE_TEST=1 with Claude Desktop running")
	}
	identity, paths, err := DiscoverWindows(context.Background(), filepath.Join(t.TempDir(), "backup"))
	if err != nil {
		t.Fatal(err)
	}
	processes, err := packageProcesses(identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) == 0 {
		t.Fatal("no Claude Desktop package process is running")
	}
	signedIn := instanceMain(processes, paths.firstPartyDataDir())
	local := instanceMain(processes, paths.ThirdPartyStateDir)
	t.Logf("%d package processes; signed-in main=%d local main=%d unclaimed=%v", len(processes), signedIn, local, unclaimedMains(processes))
	if signedIn == 0 && local == 0 {
		t.Fatal("package processes run, but neither instance was identified")
	}
	if signedIn != 0 && signedIn == local {
		t.Fatalf("both data directories resolved to the same main process %d", signedIn)
	}
}
