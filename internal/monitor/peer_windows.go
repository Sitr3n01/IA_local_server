//go:build windows

package monitor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Loopback is a routing constraint, not authentication: every local account can
// reach 127.0.0.1. The administrative pipe admits only the serving user, so a
// monitor that relayed any loopback caller to it would hand another account
// that user's authority. Before a mutation is relayed, the kernel's own TCP
// table names the process holding the other end of the connection, and that
// process must run as the user this monitor runs as.
var (
	iphlpapiDLL             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapiDLL.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPIDListener    = 3
	tcpTableOwnerPIDConnections = 4
	afInet                      = 2
	afInet6                     = 23
	errorInsufficientBuffer     = 122
	tcpRow4Size                 = 24 // MIB_TCPROW_OWNER_PID
	tcpRow6Size                 = 56 // MIB_TCP6ROW_OWNER_PID
)

var errPeerNotFound = errors.New("no TCP connection matches the request's endpoints")

var ownSID = sync.OnceValues(func() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
})

// sameUserPeer reports whether the process at the client end of a connection
// runs as this process's user. Any failure to establish that is a refusal.
func sameUserPeer(client, server netip.AddrPort) (bool, error) {
	pid, err := connectionOwner(client, server)
	if err != nil {
		return false, err
	}
	return processRunsAsUs(pid)
}

// connectionOwner finds the row for the client's end of the connection: its
// local endpoint is the request's remote address and its remote endpoint is
// the one this server accepted on.
func connectionOwner(client, server netip.AddrPort) (uint32, error) {
	client = netip.AddrPortFrom(client.Addr().Unmap(), client.Port())
	server = netip.AddrPortFrom(server.Addr().Unmap(), server.Port())
	family, rowSize := uint32(afInet), tcpRow4Size
	if client.Addr().Is6() {
		family, rowSize = afInet6, tcpRow6Size
	}
	table, err := tcpConnectionTable(family)
	if err != nil {
		return 0, err
	}
	if len(table) < 4 {
		return 0, errPeerNotFound
	}
	count := int(binary.LittleEndian.Uint32(table))
	for index := 0; index < count; index++ {
		start := 4 + index*rowSize
		if start+rowSize > len(table) {
			break
		}
		local, remote, pid := parseTCPRow(family, table[start:start+rowSize])
		if local == client && remote == server {
			return pid, nil
		}
	}
	return 0, errPeerNotFound
}

// parseTCPRow reads one owner-PID row. Addresses are stored in network order;
// each port is a DWORD whose low two bytes hold the port in network order.
func parseTCPRow(family uint32, row []byte) (local, remote netip.AddrPort, pid uint32) {
	port := func(field []byte) uint16 { return uint16(field[0])<<8 | uint16(field[1]) }
	if family == afInet {
		local = netip.AddrPortFrom(netip.AddrFrom4([4]byte(row[4:8])), port(row[8:12]))
		remote = netip.AddrPortFrom(netip.AddrFrom4([4]byte(row[12:16])), port(row[16:20]))
		return local, remote, binary.LittleEndian.Uint32(row[20:24])
	}
	local = netip.AddrPortFrom(netip.AddrFrom16([16]byte(row[0:16])), port(row[20:24]))
	remote = netip.AddrPortFrom(netip.AddrFrom16([16]byte(row[24:40])), port(row[44:48]))
	return local, remote, binary.LittleEndian.Uint32(row[52:56])
}

func tcpConnectionTable(family uint32) ([]byte, error) {
	return tcpTable(family, tcpTableOwnerPIDConnections)
}

// tcpTable reads one of the kernel's owner-PID TCP tables: established
// connections, or the sockets that are listening.
func tcpTable(family, class uint32) ([]byte, error) {
	if err := procGetExtendedTcpTable.Find(); err != nil {
		return nil, err
	}
	var size uint32
	for attempt := 0; attempt < 4; attempt++ {
		var buffer []byte
		var pointer uintptr
		if size > 0 {
			buffer = make([]byte, size)
			pointer = uintptr(unsafe.Pointer(&buffer[0]))
		}
		result, _, _ := procGetExtendedTcpTable.Call(pointer, uintptr(unsafe.Pointer(&size)), 0,
			uintptr(family), uintptr(class), 0)
		switch result {
		case 0:
			if buffer == nil {
				return nil, errPeerNotFound
			}
			return buffer[:size], nil
		case errorInsufficientBuffer:
			// Connections come and go between the two calls; ask again with
			// the size just reported.
			continue
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable failed: %d", result)
		}
	}
	return nil, errors.New("the TCP connection table kept growing while it was being read")
}

func processRunsAsUs(pid uint32) (bool, error) {
	if pid == 0 {
		return false, errPeerNotFound
	}
	self, err := ownSID()
	if err != nil {
		return false, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		// Another account's process is usually not even openable, which is
		// itself the answer.
		return false, fmt.Errorf("open peer process: %w", err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return false, fmt.Errorf("open peer token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return false, err
	}
	return user.User.Sid.Equals(self), nil
}
