//go:build !windows

package monitor

import (
	"errors"
	"net/netip"
)

// Without a way to name the process behind a connection, no caller can be shown
// to be the serving user, so every mutation is refused. The controls are
// reported unavailable on these platforms anyway.
func sameUserPeer(netip.AddrPort, netip.AddrPort) (bool, error) {
	return false, errors.New("the connection's owner cannot be verified on this platform")
}
