//go:build windows

package monitor

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
)

func TestConnectionOwnerNamesThisProcess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if server := <-accepted; server != nil {
		defer server.Close()
	}

	clientEnd := netip.MustParseAddrPort(client.LocalAddr().String())
	serverEnd := netip.MustParseAddrPort(listener.Addr().String())
	pid, err := connectionOwner(clientEnd, serverEnd)
	if err != nil || pid != uint32(os.Getpid()) {
		t.Fatalf("owner = %d, %v; want this process %d", pid, err, os.Getpid())
	}
	if same, err := sameUserPeer(clientEnd, serverEnd); err != nil || !same {
		t.Fatalf("sameUserPeer = %v, %v; a connection from this process is from this user", same, err)
	}
	// The server's own end of the same connection is a different row, and a
	// lookup with the endpoints swapped must not find the client's.
	if pid, err := connectionOwner(serverEnd, clientEnd); err == nil && pid != uint32(os.Getpid()) {
		t.Fatalf("swapped lookup found pid %d", pid)
	}
}

func TestSameUserPeerRefusesAConnectionThatDoesNotExist(t *testing.T) {
	same, err := sameUserPeer(netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2"))
	if same || !errors.Is(err, errPeerNotFound) {
		t.Fatalf("sameUserPeer = %v, %v; want a refusal", same, err)
	}
}

// verifiedPeer reads the endpoints from a real request, the way the server
// receives them.
func TestVerifiedPeerAcceptsThisUsersRealConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if verifiedPeer(r) {
			_, _ = io.WriteString(w, "same user")
			return
		}
		http.Error(w, "refused", http.StatusForbidden)
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "same user" {
		t.Fatalf("verifiedPeer on this process's own request = %d %s", response.StatusCode, body)
	}
}

func TestParseTCPRowReadsNetworkOrder(t *testing.T) {
	row := make([]byte, tcpRow4Size)
	copy(row[4:8], []byte{127, 0, 0, 1})
	copy(row[8:10], []byte{0xD3, 0x31}) // 54065
	copy(row[12:16], []byte{127, 0, 0, 1})
	copy(row[16:18], []byte{0x46, 0xAF}) // 18095
	binary.LittleEndian.PutUint32(row[20:24], 4242)
	local, remote, pid := parseTCPRow(afInet, row)
	if local != netip.MustParseAddrPort("127.0.0.1:54065") || remote != netip.MustParseAddrPort("127.0.0.1:18095") || pid != 4242 {
		t.Fatalf("parsed %v -> %v pid %d", local, remote, pid)
	}

	row6 := make([]byte, tcpRow6Size)
	loopback := netip.IPv6Loopback().As16()
	copy(row6[0:16], loopback[:])
	copy(row6[20:22], []byte{0xD3, 0x31})
	copy(row6[24:40], loopback[:])
	copy(row6[44:46], []byte{0x46, 0xAF})
	binary.LittleEndian.PutUint32(row6[52:56], 77)
	local, remote, pid = parseTCPRow(afInet6, row6)
	if local != netip.MustParseAddrPort("[::1]:54065") || remote != netip.MustParseAddrPort("[::1]:18095") || pid != 77 {
		t.Fatalf("parsed v6 %v -> %v pid %d", local, remote, pid)
	}
}
