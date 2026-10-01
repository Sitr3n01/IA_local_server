package claudedesktop

import (
	"path/filepath"
	"strings"
)

// packageProcess is one running process of the discovered package executable.
// Electron starts every helper (renderer, GPU, utility, crash handler) with a
// --type= switch and the instance's --user-data-dir=; the main process that
// owns the windows carries neither and is the helpers' parent.
type packageProcess struct {
	pid     uint32
	parent  uint32
	helper  bool
	dataDir string
}

// classifyArguments reads the two switches that place a process in an
// instance. The main process may hold its own user-data directory only in
// memory, which is why instances are found through their helpers.
func classifyArguments(args []string) (helper bool, dataDir string) {
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--type="):
			helper = true
		case strings.HasPrefix(arg, "--user-data-dir="):
			dataDir = strings.TrimPrefix(arg, "--user-data-dir=")
		}
	}
	return helper, dataDir
}

// instanceMain returns the main process of the instance whose user-data
// directory is dataDir, or zero when none runs. Only a package main process
// counts as a parent, so a recycled PID never stands in for one.
func instanceMain(processes []packageProcess, dataDir string) uint32 {
	mains := make(map[uint32]bool, len(processes))
	for _, process := range processes {
		if !process.helper {
			mains[process.pid] = true
		}
	}
	want := filepath.Clean(dataDir)
	for _, process := range processes {
		if process.helper && process.dataDir != "" && mains[process.parent] && strings.EqualFold(filepath.Clean(process.dataDir), want) {
			return process.parent
		}
	}
	return 0
}

// unclaimedMains returns main processes that no helper names as its parent
// yet: a process the shell has just started, before it has picked an
// instance. Those are the only processes a stalled activation may resume.
func unclaimedMains(processes []packageProcess) []uint32 {
	claimed := make(map[uint32]bool, len(processes))
	for _, process := range processes {
		if process.helper {
			claimed[process.parent] = true
		}
	}
	var unclaimed []uint32
	for _, process := range processes {
		if !process.helper && !claimed[process.pid] {
			unclaimed = append(unclaimed, process.pid)
		}
	}
	return unclaimed
}
