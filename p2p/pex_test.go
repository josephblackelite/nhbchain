package p2p

import (
	"encoding/json"
	"testing"
	"time"
)

type mockPexPeer struct {
	id   string
	sent []*Message
}

func (m *mockPexPeer) ID() string {
	return m.id
}

func (m *mockPexPeer) Enqueue(msg *Message) error {
	m.sent = append(m.sent, msg)
	return nil
}

func TestPexRequestDedupesByNode(t *testing.T) {
	now := time.Now()
	server := &Server{nodeID: "0xaaaa", now: func() time.Time { return now }}
	mgr := newPexManager(server)

	mgr.recordPeer("0xdead", "10.0.0.2:26656", now.Add(-time.Minute))
	mgr.recordPeer("0xdead", "10.0.0.3:26656", now)

	peer := &mockPexPeer{id: "0xbeef"}
	if err := mgr.handleRequest(peer, PexRequestPayload{Limit: 8, Token: "tok"}); err != nil {
		t.Fatalf("handleRequest failed: %v", err)
	}
	if len(peer.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(peer.sent))
	}

	var payload PexAddressesPayload
	if err := json.Unmarshal(peer.sent[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Addresses) != 1 {
		t.Fatalf("expected 1 address, got %d", len(payload.Addresses))
	}
	got := payload.Addresses[0]
	if got.NodeID != "0xdead" {
		t.Fatalf("unexpected node id: %s", got.NodeID)
	}
	if got.Addr != "10.0.0.3:26656" {
		t.Fatalf("expected latest addr, got %s", got.Addr)
	}
}

func TestPexRequestFiltersExpired(t *testing.T) {
	now := time.Now()
	server := &Server{nodeID: "0xaaaa", now: func() time.Time { return now }}
	mgr := newPexManager(server)

	mgr.recordPeer("0xdead", "10.0.0.2:26656", now.Add(-2*pexAddressTTL))

	peer := &mockPexPeer{id: "0xbeef"}
	if err := mgr.handleRequest(peer, PexRequestPayload{Limit: 8, Token: "tok"}); err != nil {
		t.Fatalf("handleRequest failed: %v", err)
	}
	if len(peer.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(peer.sent))
	}

	var payload PexAddressesPayload
	if err := json.Unmarshal(peer.sent[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Addresses) != 0 {
		t.Fatalf("expected no addresses, got %d", len(payload.Addresses))
	}
}

func TestPexEchoSuppressionPreventsLoop(t *testing.T) {
	now := time.Now()
	server := &Server{nodeID: "0xaaaa", now: func() time.Time { return now }}
	mgr := newPexManager(server)

	mgr.recordPeer("0xdead", "10.0.0.2:26656", now)

	peer := &mockPexPeer{id: "0xbeef"}
	if err := mgr.handleRequest(peer, PexRequestPayload{Limit: 8, Token: "loop"}); err != nil {
		t.Fatalf("handleRequest failed: %v", err)
	}

	mgr.handleAddresses(peer, PexAddressesPayload{
		Token:     "loop",
		Addresses: []PexAddress{{NodeID: "0xfeed", Addr: "10.1.0.5:26656", LastSeen: now}},
	})

	mgr.mu.Lock()
	_, exists := mgr.book["0xfeed"]
	mgr.mu.Unlock()
	if exists {
		t.Fatalf("echo suppression failed: looped address stored")
	}
}
