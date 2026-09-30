package monitor

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// A model that another tool loaded cannot be unloaded through that tool's API:
// the monitor holds no credential and only ever reads. What it can do, with the
// operator's confirmation, is end the process that holds the model, which frees
// the memory the way closing the tool would.
//
// The page never names a process. It names a source from the list the monitor
// itself discovered, and the monitor resolves the process from that list,
// checks it is still the same program and belongs to the same user, and asks
// the operator in a native dialog the page cannot answer.

// processIdentity pins a process to what was on screen when the operator was
// asked, so a pid that has been reused by another program is not ended.
type processIdentity struct {
	PID     uint32
	Exe     string
	Started time.Time
}

type processTerminator interface {
	// identify describes a live process of this user, or says why it may not be
	// ended.
	identify(pid uint32) (processIdentity, error)
	// terminate ends the process only if it is still the one identified.
	terminate(target processIdentity) error
}

var (
	errProcessGone     = errors.New("the process is gone")
	errProcessChanged  = errors.New("the pid now belongs to another program")
	errNotPermitted    = errors.New("the process may not be ended by the monitor")
	errTerminateFailed = errors.New("the process could not be ended")
	errTerminateSlow   = errors.New("the process did not end in time")
)

// neverEnd lists programs the monitor refuses to end whatever asked: the
// operating system's, the desktop's, and the components of this deployment. A
// source cannot normally be any of them; this is the belt to that suspender.
var neverEnd = map[string]struct{}{
	"system": {}, "registry": {}, "smss.exe": {}, "csrss.exe": {}, "wininit.exe": {}, "winlogon.exe": {},
	"services.exe": {}, "lsass.exe": {}, "svchost.exe": {}, "dwm.exe": {}, "explorer.exe": {},
	"taskmgr.exe": {}, "cia-edge.exe": {}, "cia-supervisor.exe": {}, "cia-monitor.exe": {},
	"cia-tray.exe": {}, "llama-swap.exe": {}, "cia-credential.exe": {},
}

func endable(exe string, pid, selfPID uint32) bool {
	if pid <= 4 || pid == selfPID {
		return false
	}
	_, denied := neverEnd[strings.ToLower(strings.TrimSpace(exe))]
	return !denied && strings.TrimSpace(exe) != ""
}

// stopTarget chooses the process to end for a source: the one that holds the
// model, which is the process with the most dedicated GPU memory among the
// source's own. Processes of the edge are never candidates.
func stopTarget(source inferenceSource, gpu []gpuProcess, procs map[uint32]procEntry, selfPID uint32) (gpuProcess, bool) {
	var best gpuProcess
	found := false
	for _, process := range gpu {
		belongs := process.PID == source.PID || (source.tool != "" && process.Tool == source.tool)
		if !belongs || process.VRAMDedicatedMiB < sourceMinVRAMMiB || managedByEdge(procs, process.PID) {
			continue
		}
		if !endable(process.Name, process.PID, selfPID) {
			continue
		}
		if !found || process.VRAMDedicatedMiB > best.VRAMDedicatedMiB {
			best, found = process, true
		}
	}
	return best, found
}

func describeTarget(process gpuProcess) string {
	return fmt.Sprintf("%s · PID %d", process.Name, process.PID)
}
