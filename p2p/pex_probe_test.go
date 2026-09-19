package p2p

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startLoopbackServer runs a server on an ephemeral loopback port and returns it
// with its peerstore and the address it is reachable at.
func startLoopbackServer(t *testing.T) (*Server, *Peerstore, string) {
	t.Helper()
	cfg := baseConfig(bytes.Repeat([]byte{0xD1}, 32))
	cfg.EnablePEX = true
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	cfg.HandshakeTimeout = 3 * time.Second
	cfg.RateMsgsPerSec = 1000
	cfg.RateBurst = 1000
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	store := newTestPeerstore(t)
	server.SetPeerstore(store)
	go server.Start()
	t.Cleanup(func() { server.Stop() })

	var addr string
	waitUntil(t, 3*time.Second, "the server to listen", func() bool {
		for _, candidate := range server.ListenAddresses() {
			if !strings.HasSuffix(candidate, ":0") {
				addr = candidate
				return true
			}
		}
		return false
	})
	return server, store, addr
}

// askForAddresses sends a peer exchange request from server to the connected
// peer, through the peer's ordinary send path.
func askForAddresses(t *testing.T, server *Server, peerID, token string) {
	t.Helper()
	server.mu.RLock()
	peer := server.peers[peerID]
	server.mu.RUnlock()
	if peer == nil {
		t.Fatalf("not connected to %s", peerID)
	}
	msg, err := NewPexRequestMessage(pexResponseMax, token)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := peer.Enqueue(msg); err != nil {
		t.Fatalf("send request: %v", err)
	}
}

func TestPeerLearnedByExchangeIsPersistedOnlyAfterTheHandshake(t *testing.T) {
	a, aStore, _ := startLoopbackServer(t)
	b, _, bAddr := startLoopbackServer(t)
	c, _, cAddr := startLoopbackServer(t)

	// a may only reach c once the test lets it.
	var dialing atomic.Bool
	release := make(chan struct{})
	a.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		if addr == cAddr {
			dialing.Store(true)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return defaultDialer(ctx, addr)
	}

	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect to hub: %v", err)
	}
	waitUntil(t, 3*time.Second, "the hub to see us", func() bool { return b.hasPeer(a.nodeID) })
	b.pex.recordPeer(c.nodeID, cAddr, time.Now())

	askForAddresses(t, a, b.nodeID, "learn-c")
	waitUntil(t, 3*time.Second, "a to start dialing the address it learned", dialing.Load)

	if _, ok := aStore.ByNodeID(c.nodeID); ok {
		t.Fatalf("an address learned by exchange must not be persisted before the peer answered a handshake")
	}
	if a.hasPeer(c.nodeID) {
		t.Fatalf("not connected yet")
	}

	close(release)
	waitUntil(t, 5*time.Second, "a to reach the learned peer", func() bool { return a.hasPeer(c.nodeID) })
	waitUntil(t, 3*time.Second, "the learned peer to be persisted", func() bool {
		entry, ok := aStore.ByNodeID(c.nodeID)
		return ok && entry.Addr == cAddr && entry.Score >= 1
	})
}

func TestUnreachableAddressesLearnedByExchangeAreTriedOnceAndForgotten(t *testing.T) {
	a, aStore, _ := startLoopbackServer(t)
	b, _, bAddr := startLoopbackServer(t)

	var mu sync.Mutex
	dials := map[string]int{}
	a.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		mu.Lock()
		dials[addr]++
		mu.Unlock()
		if addr == bAddr {
			return defaultDialer(ctx, addr)
		}
		return nil, fmt.Errorf("nobody home")
	}

	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect to hub: %v", err)
	}
	waitUntil(t, 3*time.Second, "the hub to see us", func() bool { return b.hasPeer(a.nodeID) })
	deadID, deadAddr := fmt.Sprintf("0x%064x", 99), "127.0.0.1:1"
	b.pex.recordPeer(deadID, deadAddr, time.Now())

	askForAddresses(t, a, b.nodeID, "learn-dead")
	waitUntil(t, 3*time.Second, "the dead address to be forgotten", func() bool {
		mu.Lock()
		tried := dials[deadAddr]
		mu.Unlock()
		a.pex.mu.Lock()
		_, known := a.pex.book[deadID]
		a.pex.mu.Unlock()
		return tried == 1 && !known
	})
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	tried := dials[deadAddr]
	mu.Unlock()
	if tried != 1 {
		t.Fatalf("expected the address to be tried exactly once, got %d", tried)
	}
	if _, ok := aStore.ByNodeID(deadID); ok {
		t.Fatalf("an address that never answered must not be persisted")
	}
}

func TestLearnedAddressesAreDialedAFewAtATime(t *testing.T) {
	a, _, _ := startLoopbackServer(t)
	b, _, bAddr := startLoopbackServer(t)

	var inFlight, peak atomic.Int32
	gate := make(chan struct{})
	a.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		if addr == bAddr {
			return defaultDialer(ctx, addr)
		}
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil, fmt.Errorf("nobody home")
	}
	defer close(gate)

	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect to hub: %v", err)
	}
	waitUntil(t, 3*time.Second, "the hub to see us", func() bool { return b.hasPeer(a.nodeID) })
	for i := 0; i < pexResponseMax; i++ {
		b.pex.recordPeer(fmt.Sprintf("0x%064x", 1000+i), fmt.Sprintf("127.0.0.1:%d", 2+i), time.Now())
	}

	askForAddresses(t, a, b.nodeID, "learn-many")
	waitUntil(t, 3*time.Second, "the first dials to start", func() bool { return inFlight.Load() > 0 })
	time.Sleep(200 * time.Millisecond)
	if got := peak.Load(); got > pexMaxProbes {
		t.Fatalf("expected at most %d addresses to be dialed at once, saw %d", pexMaxProbes, got)
	}
}

func TestUnsolicitedAddressesEndAConnectionWithAPeer(t *testing.T) {
	a, aStore, _ := startLoopbackServer(t)
	b, _, bAddr := startLoopbackServer(t)

	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect to hub: %v", err)
	}
	waitUntil(t, 3*time.Second, "the hub to see us", func() bool { return b.hasPeer(a.nodeID) })

	// a never asked b for addresses.
	msg, err := NewPexAddressesMessage(PexAddressesPayload{Token: "unasked", Addresses: fakePexAddresses(20, time.Now())})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if err := b.Broadcast(msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	waitUntil(t, 3*time.Second, "the receiving side to end the connection", func() bool { return !a.hasPeer(b.nodeID) })
	a.pex.mu.Lock()
	learned := len(a.pex.book)
	a.pex.mu.Unlock()
	// The only entry is the peer a itself connected to.
	if learned > 1 {
		t.Fatalf("expected nothing to be learned from an unsolicited message, book has %d entries", learned)
	}
	for _, entry := range aStore.Snapshot() {
		if strings.HasPrefix(entry.Addr, "10.") {
			t.Fatalf("an unsolicited address reached the peerstore: %+v", entry)
		}
	}
}
