package p2p

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var claimTestGenesis = bytes.Repeat([]byte{0xE5}, 32)

// newClaimTestServer returns a server that keeps its peers long enough to look
// at them.
func newClaimTestServer(t *testing.T, mutate func(*ServerConfig)) *Server {
	t.Helper()
	cfg := baseConfig(claimTestGenesis)
	cfg.EnablePEX = true
	cfg.ReadTimeout = time.Minute
	cfg.WriteTimeout = 5 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	t.Cleanup(func() { server.Stop() })
	return server
}

// newClaimRemote returns a node that advertises the given addresses in its
// handshake.
func newClaimRemote(t *testing.T, advertised ...string) *Server {
	t.Helper()
	remote := NewServer(noopHandler{}, mustKey(t), baseConfig(claimTestGenesis))
	for _, addr := range advertised {
		remote.addListenAddress(addr)
	}
	return remote
}

// answerDials makes every dial of server succeed with a connection that the
// remote node answers, and whose remote address is the dialed one.
func answerDials(t *testing.T, server, remote *Server) {
	t.Helper()
	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		target, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return nil, err
		}
		left, right := net.Pipe()
		t.Cleanup(func() {
			left.Close()
			right.Close()
		})
		go func() {
			if _, err := bufio.NewReader(right).ReadBytes('\n'); err != nil {
				return
			}
			packet, err := remote.buildHandshake()
			if err != nil {
				return
			}
			data, err := json.Marshal(packet)
			if err != nil {
				return
			}
			_, _ = right.Write(append(data, '\n'))
		}()
		return addrConn{Conn: left, remote: target}, nil
	}
}

// waitForInbound waits until an inbound handshake has been dealt with in full:
// the peer is registered and everything recorded about it has been written.
func waitForInbound(t *testing.T, server *Server, nodeID string) {
	t.Helper()
	waitUntil(t, 2*time.Second, "the peer to register", func() bool { return server.hasPeer(nodeID) })
	waitUntil(t, 2*time.Second, "the handshake to finish", func() bool {
		server.inboundMu.Lock()
		defer server.inboundMu.Unlock()
		return server.pendingHandshakes == 0
	})
}

func pexEntryFor(server *Server, nodeID string) (pexEntry, bool) {
	server.pex.mu.Lock()
	defer server.pex.mu.Unlock()
	entry, ok := server.pex.book[nodeID]
	if !ok {
		return pexEntry{}, false
	}
	return *entry, true
}

// addressesHandedOut returns what a peer asking this node for addresses gets,
// by node ID.
func addressesHandedOut(t *testing.T, server *Server) map[string]string {
	t.Helper()
	asker := &mockPexPeer{id: "0xbeef"}
	if err := server.pex.handleRequest(asker, PexRequestPayload{Limit: 8, Token: "tok"}); err != nil {
		t.Fatalf("handle request: %v", err)
	}
	handed := make(map[string]string)
	for _, msg := range asker.sent {
		var payload PexAddressesPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		for _, addr := range payload.Addresses {
			handed[addr.NodeID] = addr.Addr
		}
	}
	return handed
}

func TestListenAddressAtAnotherHostIsNotTakenForAConnectionToIt(t *testing.T) {
	const configured = "198.51.100.20:6001"
	var dials atomic.Int32
	server := newClaimTestServer(t, func(cfg *ServerConfig) {
		cfg.PersistentPeers = []string{configured}
	})
	store := newTestPeerstore(t)
	server.SetPeerstore(store)
	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("dial blocked")
	}

	// The peer connects from one host and says it listens at another, which is
	// the address of a configured peer.
	stranger := newClaimRemote(t, configured)
	dialHelloFrom(t, server, stranger, tcpAddr("203.0.113.9", 50000))
	waitForInbound(t, server, stranger.nodeID)

	if server.isConnectedToAddress(configured) {
		t.Fatalf("an address that a peer only says it listens at must not count as a connection to that address")
	}
	if err := server.Connect(configured); err == nil || dials.Load() != 1 {
		t.Fatalf("expected Connect to dial the configured address once, got %d dials and error %v", dials.Load(), err)
	}
	if entry, ok := store.ByNodeID(stranger.nodeID); ok {
		t.Fatalf("an address at another host must not be persisted, got %+v", entry)
	}
	if entry, ok := store.Get(configured); ok {
		t.Fatalf("the configured address must not be recorded against the peer, got %+v", entry)
	}
	if entry, ok := pexEntryFor(server, stranger.nodeID); ok {
		t.Fatalf("an address at another host must not enter the address book, got %+v", entry)
	}
	if handed := addressesHandedOut(t, server); len(handed) != 0 {
		t.Fatalf("nothing is known to pass on, got %v", handed)
	}
}

func TestListenAddressAtTheConnectionsOwnHostIsKept(t *testing.T) {
	const advertised = "203.0.113.9:6001"
	server := newClaimTestServer(t, nil)
	store := newTestPeerstore(t)
	server.SetPeerstore(store)

	remote := newClaimRemote(t, advertised)
	dialHelloFrom(t, server, remote, tcpAddr("203.0.113.9", 50000))
	waitForInbound(t, server, remote.nodeID)

	if !server.isConnectedToAddress(advertised) {
		t.Fatalf("an address at the connection's own host is how this node recognises the connection")
	}
	entry, ok := store.ByNodeID(remote.nodeID)
	if !ok || entry.Addr != advertised {
		t.Fatalf("expected the advertised address to be persisted, got %+v ok=%v", entry, ok)
	}
	book, ok := pexEntryFor(server, remote.nodeID)
	if !ok || book.Addr != advertised || !book.verified {
		t.Fatalf("expected the advertised address in the address book, got %+v ok=%v", book, ok)
	}
	if handed := addressesHandedOut(t, server); handed[remote.nodeID] != advertised {
		t.Fatalf("expected the address to be passed on, got %v", handed)
	}
}

func TestListenAddressOfAPeerWeDialedCannotReplaceTheAddressWeDialed(t *testing.T) {
	const dialed = "198.51.100.30:6001"
	cases := []struct {
		name   string
		claims []string
		want   string
	}{
		{"another host only", []string{"203.0.113.50:6001"}, dialed},
		{"another host, then its own host at another port", []string{"203.0.113.50:6001", "198.51.100.30:7001"}, "198.51.100.30:7001"},
		{"no address", nil, dialed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newClaimTestServer(t, nil)
			store := newTestPeerstore(t)
			server.SetPeerstore(store)
			remote := newClaimRemote(t, tc.claims...)
			answerDials(t, server, remote)

			if err := server.Connect(dialed); err != nil {
				t.Fatalf("connect: %v", err)
			}
			if !server.isConnectedToAddress(dialed) {
				t.Fatalf("the address that was dialed must identify the connection")
			}
			entry, ok := store.ByNodeID(remote.nodeID)
			if !ok || entry.Addr != tc.want {
				t.Fatalf("expected %s to be persisted, got %+v ok=%v", tc.want, entry, ok)
			}
			book, ok := pexEntryFor(server, remote.nodeID)
			if !ok || book.Addr != tc.want {
				t.Fatalf("expected %s in the address book, got %+v ok=%v", tc.want, book, ok)
			}
			for _, claim := range tc.claims {
				if claim == tc.want {
					continue
				}
				if _, ok := store.Get(claim); ok {
					t.Fatalf("%s is not at the host that was dialed and must not be persisted", claim)
				}
			}
		})
	}
}

func TestInboundPeerNeverTakesAnAddressAnotherPeerHolds(t *testing.T) {
	const addr = "198.51.100.40:6001"
	server := newClaimTestServer(t, nil)
	register := func(id string, inbound bool) *Peer {
		t.Helper()
		left, right := net.Pipe()
		t.Cleanup(func() {
			left.Close()
			right.Close()
		})
		peer := newPeer(id, "test/1.0", left, bufio.NewReader(left), server, inbound, false, addr)
		if err := server.registerPeer(peer); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		return peer
	}
	holder := func() string {
		server.mu.RLock()
		defer server.mu.RUnlock()
		return server.byAddr[addr]
	}

	outbound := register("outbound-peer", false)
	inbound := register("inbound-peer", true)
	if got := holder(); got != "outbound-peer" {
		t.Fatalf("an inbound peer must not take an address from the peer holding it, address is held by %q", got)
	}
	server.removePeer(inbound, false, nil)
	if !server.isConnectedToAddress(addr) {
		t.Fatalf("removing the inbound peer must not release an address it does not hold")
	}
	server.removePeer(outbound, false, nil)
	if server.isConnectedToAddress(addr) {
		t.Fatalf("removing the holder must release the address")
	}

	// A peer this node dialed always indexes the address it dialed.
	inbound = register("inbound-peer-2", true)
	outbound = register("outbound-peer-2", false)
	if got := holder(); got != "outbound-peer-2" {
		t.Fatalf("the peer that was dialed at an address must hold it, address is held by %q", got)
	}
	server.removePeer(inbound, false, nil)
	if got := holder(); got != "outbound-peer-2" {
		t.Fatalf("removing the inbound peer must leave the address with its holder, got %q", got)
	}
}

func TestSeedConnectedInboundIsNotDialedAgain(t *testing.T) {
	const seedAddr = "198.51.100.7:6001"
	seed := newClaimRemote(t, seedAddr)
	var dials atomic.Int32
	server := newClaimTestServer(t, func(cfg *ServerConfig) {
		cfg.Seeds = []string{seed.nodeID + "@" + seedAddr}
	})
	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("dial blocked")
	}

	// The seed reaches this node from an address other than the one it
	// advertises, as a node behind an address-translating gateway does, so the
	// connection is not known by the seed's address.
	dialHelloFrom(t, server, seed, tcpAddr("203.0.113.9", 50000))
	waitForInbound(t, server, seed.nodeID)
	if server.isConnectedToAddress(seedAddr) {
		t.Fatalf("the seed's address is not the connection's host and must not identify the connection")
	}

	server.startConnManager()
	time.Sleep(300 * time.Millisecond)
	if n := dials.Load(); n != 0 {
		t.Fatalf("a seed this node is already connected to was dialed %d times", n)
	}
	for _, candidate := range server.connMgr.selectDialCandidates(4) {
		if strings.TrimSpace(candidate.Addr) == seedAddr {
			t.Fatalf("a seed this node is already connected to was offered as a dial candidate")
		}
	}
}

func TestSourceBoundAddrs(t *testing.T) {
	pipe, other := net.Pipe()
	defer pipe.Close()
	defer other.Close()

	cases := []struct {
		name   string
		addrs  []string
		remote net.Addr
		want   []string
	}{
		{"the connection's host at any port", []string{"203.0.113.9:6001", "203.0.113.10:6001", "203.0.113.9:7001"},
			tcpAddr("203.0.113.9", 50000), []string{"203.0.113.9:6001", "203.0.113.9:7001"}},
		{"another host", []string{"203.0.113.10:6001"}, tcpAddr("203.0.113.9", 50000), nil},
		{"IPv6", []string{"[2001:db8::1]:6001", "[2001:db8::2]:6001"}, tcpAddr("2001:db8::1", 50000), []string{"[2001:db8::1]:6001"}},
		{"IPv4 address of an IPv6 socket", []string{"203.0.113.9:6001"},
			&net.TCPAddr{IP: net.ParseIP("::ffff:203.0.113.9"), Port: 50000}, []string{"203.0.113.9:6001"}},
		{"a host name never qualifies", []string{"validator.example.net:6001"}, tcpAddr("203.0.113.9", 50000), nil},
		{"a malformed address", []string{"203.0.113.9"}, tcpAddr("203.0.113.9", 50000), nil},
		{"a remote that is not an IP address", []string{"203.0.113.9:6001"}, pipe.RemoteAddr(), nil},
		{"no remote", []string{"203.0.113.9:6001"}, nil, nil},
		{"no addresses", nil, tcpAddr("203.0.113.9", 50000), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceBoundAddrs(tc.addrs, tc.remote)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}
