//go:build windows

package monitor

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHelperSleeper is not a test: when the environment asks for it, the test
// binary re-executes itself as a process that does nothing for a minute, with
// whatever arguments the caller gave it, so the Windows calls can be exercised
// on a process this test owns and may end.
func TestHelperSleeper(t *testing.T) {
	if os.Getenv("CIA_MONITOR_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func spawnHelper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperSleeper$", "--"}, args...)...)
	command.Env = append(os.Environ(), "CIA_MONITOR_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	return command
}

func TestReadLaunchInfoFromARealProcessKeepsOnlyTheAllowlistedFlags(t *testing.T) {
	secret := "sk-live-THIS-MUST-NOT-BE-KEPT-9f8e7d6c"
	helper := spawnHelper(t, "--model", `C:\somewhere\private\real-model-Q4_K_M.gguf`, "--api-key", secret, "-c", "4096", "-np", "2")
	var info launchInfo
	var ok bool
	for attempt := 0; attempt < 20 && !ok; attempt++ {
		info, ok = readLaunchInfo(uint32(helper.Process.Pid))
		if !ok {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatal("the command line of a live process of this user could not be read")
	}
	if info.File != "real-model-Q4_K_M.gguf" || info.Context == nil || *info.Context != 4096 || info.Parallel == nil || *info.Parallel != 2 {
		t.Fatalf("info = %+v", info)
	}
	if strings.Contains(info.File+info.Alias, secret) || strings.Contains(info.File, "private") {
		t.Fatalf("the reader kept more than the allowlist: %+v", info)
	}
	if _, ok := readLaunchInfo(0); ok {
		t.Fatal("pid 0 answered")
	}
}

func TestProcessStartAndMemoryOfALiveProcess(t *testing.T) {
	helper := spawnHelper(t)
	pid := uint32(helper.Process.Pid)
	started, ok := processStart(pid)
	if !ok || time.Since(started) > time.Minute || started.After(time.Now().Add(time.Second)) {
		t.Fatalf("start = %v ok=%v", started, ok)
	}
	if counters, ok := processMemory(pid); !ok || counters.WorkingSetSize == 0 {
		t.Fatalf("memory = %+v ok=%v", counters, ok)
	}
}

func TestTerminatorEndsExactlyTheIdentifiedProcess(t *testing.T) {
	terminator := newTerminator()
	helper := spawnHelper(t)
	pid := uint32(helper.Process.Pid)

	identity, err := terminator.identify(pid)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if identity.PID != pid || !strings.EqualFold(identity.Exe, "monitor.test.exe") && !strings.HasSuffix(strings.ToLower(identity.Exe), ".exe") {
		t.Fatalf("identity = %+v", identity)
	}

	// A pid reused by another program - a different name or a different start
	// time - is refused and the process lives on.
	other := identity
	other.Exe = "notepad.exe"
	if err := terminator.terminate(other); !errors.Is(err, errProcessChanged) {
		t.Fatalf("terminate with another name = %v, want errProcessChanged", err)
	}
	other = identity
	other.Started = identity.Started.Add(time.Hour)
	if err := terminator.terminate(other); !errors.Is(err, errProcessChanged) {
		t.Fatalf("terminate with another start time = %v, want errProcessChanged", err)
	}
	if again, err := terminator.identify(pid); err != nil || again != identity {
		t.Fatalf("the process did not survive a refused terminate: %+v %v", again, err)
	}

	if err := terminator.terminate(identity); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- helper.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the process was still running after terminate returned")
	}
	if _, err := terminator.identify(pid); err == nil {
		t.Fatal("a process that was ended could still be identified")
	}
}

func TestTerminatorRefusesSystemPidsAndVanishedProcesses(t *testing.T) {
	terminator := newTerminator()
	for _, pid := range []uint32{0, 4} {
		if _, err := terminator.identify(pid); !errors.Is(err, errNotPermitted) {
			t.Errorf("identify(%d) = %v, want errNotPermitted", pid, err)
		}
	}
	if _, err := terminator.identify(0x7FFFFFF0); !errors.Is(err, errProcessGone) {
		t.Errorf("identify of a pid that does not exist = %v, want errProcessGone", err)
	}
	if err := terminator.terminate(processIdentity{PID: 0x7FFFFFF0, Exe: "x.exe"}); !errors.Is(err, errProcessGone) {
		t.Errorf("terminate of a pid that does not exist = %v, want errProcessGone", err)
	}
}
