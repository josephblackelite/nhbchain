package p2p

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// PL-R5T-65: any peer that completes a handshake (a free identity) could ask the address book for
// other peers' addresses, a few every few seconds, and start over by reconnecting. The throttle now
// follows the host, an address is handed out once per connection, and the peers an operator
// configured as persistent are never handed out.

type hostedPexPeer struct {
	mockPexPeer
	host string
}

func (p *hostedPexPeer) RemoteHost() string { return p.host }

func newPexLimitsServer(now *time.Time) (*Server, *pexManager) {
	server := &Server{
		nodeID:        "0xaaaa",
		now:           func() time.Time { return *now },
		persistentIDs: map[string]struct{}{},
	}
	mgr := newPexManager(server)
	mgr.recordPeer("0xdead", "203.0.113.2:26656", *now)
	mgr.recordPeer("0xbeef1", "203.0.113.3:26656", *now)
	return server, mgr
}

func request(t *testing.T, mgr *pexManager, peer pexPeer, token string) {
	t.Helper()
	if err := mgr.handleRequest(peer, PexRequestPayload{Limit: 8, Token: token}); err != nil {
		t.Fatalf("handleRequest: %v", err)
	}
}

func sentCount(peer pexPeer) int {
	switch p := peer.(type) {
	case *hostedPexPeer:
		return len(p.sent)
	case *mockPexPeer:
		return len(p.sent)
	}
	return -1
}

func TestPexRequestLimits_AReconnectDoesNotRestartTheThrottle(t *testing.T) {
	now := time.Now()
	_, mgr := newPexLimitsServer(&now)

	// One host connects, asks, goes away and comes back under a new identity, again and again.
	answers, previous := 0, ""
	for i := 0; i < 8; i++ {
		if previous != "" {
			mgr.forgetPeer(previous)
		}
		peer := &hostedPexPeer{mockPexPeer: mockPexPeer{id: fmt.Sprintf("0x%x", 0x100+i)}, host: "198.51.100.7"}
		request(t, mgr, peer, "tok")
		answers += sentCount(peer)
		previous = peer.id
	}
	if answers != pexHostBurst {
		t.Fatalf("a host that reconnected 8 times got %d answers, want its allowance of %d", answers, pexHostBurst)
	}
}

func TestPexRequestLimits_TheThrottleRecoversOverTime(t *testing.T) {
	now := time.Now()
	_, mgr := newPexLimitsServer(&now)
	host := "198.51.100.8"
	drain := func() {
		for i := 0; i < 6; i++ {
			p := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x1" + string(rune('0'+i))}, host: host}
			request(t, mgr, p, "d")
		}
	}
	drain()
	late := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x77"}, host: host}
	request(t, mgr, late, "late")
	if sentCount(late) != 0 {
		t.Fatalf("a host that has used its allowance was answered at once")
	}
	now = now.Add(10 * time.Minute)
	later := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x78"}, host: host}
	request(t, mgr, later, "later")
	if sentCount(later) != 1 {
		t.Fatalf("the allowance of a host must come back with time")
	}
}

func TestPexRequestLimits_OtherHostsAreNotAffected(t *testing.T) {
	now := time.Now()
	_, mgr := newPexLimitsServer(&now)
	for i := 0; i < 6; i++ {
		request(t, mgr, &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x2" + string(rune('0'+i))}, host: "198.51.100.9"}, "x")
	}
	other := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x90"}, host: "198.51.100.10"}
	request(t, mgr, other, "y")
	if sentCount(other) != 1 {
		t.Fatalf("a different host must be answered")
	}
}

func TestPexRequestLimits_OneAnswerPerConnection(t *testing.T) {
	now := time.Now()
	_, mgr := newPexLimitsServer(&now)
	peer := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x31"}, host: "198.51.100.11"}
	request(t, mgr, peer, "one")
	now = now.Add(time.Hour) // the host's allowance has long come back
	request(t, mgr, peer, "two")
	if sentCount(peer) != 1 {
		t.Fatalf("a connection was answered %d times, want once", sentCount(peer))
	}
	// A new connection (the old one's state is forgotten on disconnect) is answered again.
	mgr.forgetPeer(peer.id)
	request(t, mgr, peer, "three")
	if sentCount(peer) != 2 {
		t.Fatalf("a new connection must be answered once")
	}
}

func TestPexRequestLimits_APeerWithoutAHostKeepsTheOldThrottle(t *testing.T) {
	now := time.Now()
	_, mgr := newPexLimitsServer(&now)
	peer := &mockPexPeer{id: "0x41"}
	request(t, mgr, peer, "one")
	if len(peer.sent) != 1 {
		t.Fatalf("the first request must be answered")
	}
}

func TestPexRequestLimits_ConfiguredPersistentPeersAreNeverHandedOut(t *testing.T) {
	now := time.Now()
	server, mgr := newPexLimitsServer(&now)
	server.persistentIDs["0xdead"] = struct{}{}

	peer := &hostedPexPeer{mockPexPeer: mockPexPeer{id: "0x51"}, host: "198.51.100.12"}
	request(t, mgr, peer, "p")
	if sentCount(peer) != 1 {
		t.Fatalf("the request must be answered")
	}
	var payload PexAddressesPayload
	if err := json.Unmarshal(peer.sent[0].Payload, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range payload.Addresses {
		if a.NodeID == "0xdead" {
			t.Fatalf("a configured persistent peer was handed out")
		}
	}
	found := false
	for _, a := range payload.Addresses {
		if a.NodeID == "0xbeef1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an ordinary peer must still be handed out, got %+v", payload.Addresses)
	}
}

// A real connection reports the host it came from, which is what the throttle keys on.
func TestPexRequestLimits_ARealPeerReportsItsHost(t *testing.T) {
	a, _ := startChallengeServer(t, "auto")
	b, bAddr := startChallengeServer(t, "auto")
	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitUntil(t, 3*time.Second, "both sides to hold the connection", func() bool {
		return a.hasPeer(b.nodeID) && b.hasPeer(a.nodeID)
	})
	b.mu.Lock()
	peer := b.peers[a.nodeID]
	b.mu.Unlock()
	if peer == nil {
		t.Fatalf("no peer")
	}
	if got := peer.RemoteHost(); got != "127.0.0.1" {
		t.Fatalf("RemoteHost() = %q, want the loopback host the connection came from", got)
	}
	var nilPeer *Peer
	if nilPeer.RemoteHost() != "" {
		t.Fatalf("a nil peer has no host")
	}
	if _, ok := pexPeer(peer).(pexHosted); !ok {
		t.Fatalf("a real peer must be a pexHosted")
	}
}
