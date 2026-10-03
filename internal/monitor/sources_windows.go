//go:build windows

package monitor

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// windowsHost answers discovery's questions from the kernel: the process table,
// the listening-socket table and the token of the process behind a socket.
type windowsHost struct {
	mu    sync.Mutex
	paths imagePaths
	home  string
}

func newHostView() hostView {
	home, _ := os.UserHomeDir()
	return &windowsHost{paths: imagePaths{known: make(map[uint32]cachedPath)}, home: home}
}

func (h *windowsHost) processes() map[uint32]procEntry { return snapshotProcesses() }

func (h *windowsHost) imagePath(pid uint32, exe string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paths.lookup(pid, exe)
}

func (h *windowsHost) sameUser(pid uint32) bool {
	ok, err := processRunsAsUs(pid)
	return err == nil && ok
}

func (h *windowsHost) lmStudioState() (uint16, uint32, bool) {
	if h.home == "" {
		return 0, 0, false
	}
	return lmStudioStateFile(filepath.Join(h.home, ".lmstudio", ".internal", "http-server.json"))
}

// listeners returns the sockets a loopback client can reach: bound to
// loopback, or to every interface. A socket bound to one LAN address is not
// reachable that way and is not the monitor's to open.
func (h *windowsHost) listeners() []tcpListener {
	var out []tcpListener
	seen := make(map[[2]uint32]struct{})
	for _, family := range []uint32{afInet, afInet6} {
		table, err := tcpTable(family, tcpTableOwnerPIDListener)
		if err != nil || len(table) < 4 {
			continue
		}
		rowSize := tcpRow4Size
		if family == afInet6 {
			rowSize = tcpRow6Size
		}
		count := int(binary.LittleEndian.Uint32(table))
		for index := 0; index < count; index++ {
			start := 4 + index*rowSize
			if start+rowSize > len(table) {
				break
			}
			local, _, pid := parseTCPRow(family, table[start:start+rowSize])
			addr, ok := loopbackReachable(local.Addr())
			if !ok || pid == 0 {
				continue
			}
			key := [2]uint32{pid, uint32(local.Port())}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, tcpListener{Addr: addr, Port: local.Port(), PID: pid})
		}
	}
	return out
}

// loopbackReachable maps a bound address to the loopback address that reaches
// it, or reports that none does.
func loopbackReachable(bound netip.Addr) (netip.Addr, bool) {
	bound = bound.Unmap()
	switch {
	case bound.IsLoopback() && bound.Is4(), bound.IsUnspecified() && bound.Is4():
		return netip.MustParseAddr("127.0.0.1"), true
	case bound.IsLoopback() && bound.Is6(), bound.IsUnspecified() && bound.Is6():
		return netip.MustParseAddr("::1"), true
	}
	return netip.Addr{}, false
}

func (h *windowsHost) launchInfo(pid uint32) (launchInfo, bool) { return readLaunchInfo(pid) }

// serverLog finds where a llama.cpp server behind a tool writes its log: the file
// its own arguments name, else the newest log in the folder LM Studio and Bionic
// write theirs to.
func (h *windowsHost) serverLog(tool string, launch launchInfo) string {
	if launch.LogFile != "" && filepath.IsAbs(launch.LogFile) && regularFile(launch.LogFile) {
		return launch.LogFile
	}
	if tool != toolLMStudio || h.home == "" {
		return ""
	}
	for _, folder := range []string{
		filepath.Join(h.home, ".lmstudio", "apps", "bionic", "server-logs"),
		filepath.Join(h.home, ".lmstudio", "server-logs"),
	} {
		if path := latestServerLog(folder); path != "" {
			return path
		}
	}
	return ""
}

func (h *windowsHost) memory(pid uint32) (float64, bool) {
	counters, ok := processMemory(pid)
	if !ok {
		return 0, false
	}
	return float64(counters.WorkingSetSize), true
}

func (h *windowsHost) startTime(pid uint32) (time.Time, bool) { return processStart(pid) }
