package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The tests in this file drive the peer exchange the way a remote peer can: by
// handing a connected peer's control-message handler whatever bytes the peer
// chose to send. They pin down that a peer cannot make the address book or the
// peerstore grow by flooding address messages.

func newPexTestServer(t *testing.T) (*Server, *Peerstore) {
	t.Helper()
	cfg := baseConfig(bytes.Repeat([]byte{0xA1}, 32))
	cfg.EnablePEX = true
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	store := newTestPeerstore(t)
	server.SetPeerstore(store)
	// Candidates learned from replies are dialed once; without a dialer they are
	// only recorded, which keeps these tests off the network.
	server.dialFn = nil
	return server, store
}

func newPexTestPeer(t *testing.T, server *Server, id string) *Peer {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		left.Close()
		right.Close()
	})
	return newPeer(id, "test/1.0", left, bufio.NewReader(left), server, false, false, "")
}

// requestAddresses sends a peer exchange request to the peer through the same
// send path a real request would take.
func requestAddresses(t *testing.T, peer *Peer, token string) {
	t.Helper()
	msg, err := NewPexRequestMessage(pexResponseMax, token)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := peer.Enqueue(msg); err != nil {
		t.Fatalf("enqueue request: %v", err)
	}
	<-peer.outbound
}

func pexReply(t *testing.T, token string, addrs []PexAddress) *Message {
	t.Helper()
	msg, err := NewPexAddressesMessage(PexAddressesPayload{Token: token, Addresses: addrs})
	if err != nil {
		t.Fatalf("build reply: %v", err)
	}
	return msg
}

func fakePexAddresses(n int, seen time.Time) []PexAddress {
	addrs := make([]PexAddress, n)
	for i := range addrs {
		addrs[i] = PexAddress{
			NodeID:   fmt.Sprintf("0x%064x", i+1),
			Addr:     fmt.Sprintf("10.%d.%d.%d:26656", (i>>16)&0xff, (i>>8)&0xff, i&0xff),
			LastSeen: seen,
		}
	}
	return addrs
}

func TestPexRefusesUnsolicitedAddresses(t *testing.T) {
	server, store := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")

	for i := 0; i < 20; i++ {
		_, err := peer.handleControlMessage(pexReply(t, fmt.Sprintf("flood-%d", i), fakePexAddresses(1000, time.Now())))
		if err == nil {
			t.Fatalf("message %d: expected address message without a request to be refused", i)
		}
	}
	if got := len(server.pex.book); got != 0 {
		t.Fatalf("expected an empty address book, got %d entries", got)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("expected an empty peerstore, got %d entries", got)
	}
}

func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// TestPexFloodLeavesMemoryAndStoresBounded replays the flood that used to take a
// node down: messages of about a megabyte each, every entry a fresh node ID and
// address. Before the limits, 30 of them left over 200,000 entries in the book
// and in the peerstore and about 90 MB of heap behind.
func TestPexFloodLeavesMemoryAndStoresBounded(t *testing.T) {
	server, store := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	now := time.Now()

	before := heapInUse()
	const messages, perMessage = 30, 7000
	for i := 0; i < messages; i++ {
		entries := make([]PexAddress, perMessage)
		for j := range entries {
			n := i*perMessage + j + 1
			entries[j] = PexAddress{
				NodeID:   fmt.Sprintf("0x%064x", n),
				Addr:     fmt.Sprintf("10.%d.%d.%d:26656", (n>>16)&0xff, (n>>8)&0xff, n&0xff),
				LastSeen: now,
			}
		}
		// The peer is disconnected after the first refusal; a flooder that kept
		// sending regardless is what is played here.
		_, _ = peer.handleControlMessage(pexReply(t, fmt.Sprintf("flood-%d", i), entries))
	}
	after := heapInUse()

	if got := len(server.pex.book); got > pexBookMax {
		t.Fatalf("address book holds %d entries, cap is %d", got, pexBookMax)
	}
	if got := len(store.Snapshot()); got > defaultPeerstoreMaxEntries {
		t.Fatalf("peerstore holds %d entries, cap is %d", got, defaultPeerstoreMaxEntries)
	}
	if after > before && after-before > 16<<20 {
		t.Fatalf("the flood left %d MB of heap behind", (after-before)>>20)
	}
}

func TestPexRefusesAddressesWithoutToken(t *testing.T) {
	server, _ := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	requestAddresses(t, peer, "open-request")

	if _, err := peer.handleControlMessage(pexReply(t, "", fakePexAddresses(10, time.Now()))); err != nil {
		t.Fatalf("unexpected error for a reply without a token: %v", err)
	}
	if got := len(server.pex.book); got != 0 {
		t.Fatalf("expected a reply without a token to be ignored, book has %d entries", got)
	}
	if !server.pex.expectingReply(peer.id) {
		t.Fatalf("a reply without a token must not close the open request")
	}
}

func TestPexTakesOnlyTheReplyToARequestItSent(t *testing.T) {
	server, _ := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	requestAddresses(t, peer, "the-token")

	if _, err := peer.handleControlMessage(pexReply(t, "some-other-token", fakePexAddresses(5, time.Now()))); err != nil {
		t.Fatalf("unexpected error for a reply with the wrong token: %v", err)
	}
	if got := len(server.pex.book); got != 0 {
		t.Fatalf("expected a reply with the wrong token to be ignored, book has %d entries", got)
	}

	if _, err := peer.handleControlMessage(pexReply(t, "the-token", fakePexAddresses(5, time.Now()))); err != nil {
		t.Fatalf("reply to our request: %v", err)
	}
	if got := len(server.pex.book); got != 5 {
		t.Fatalf("expected the reply to our request to be taken, book has %d entries", got)
	}

	// The request is closed by its reply: the same token cannot be used again.
	if _, err := peer.handleControlMessage(pexReply(t, "the-token", fakePexAddresses(50, time.Now()))); err == nil {
		t.Fatalf("expected a second reply to the same request to be refused")
	}
	if got := len(server.pex.book); got != 5 {
		t.Fatalf("expected the replayed reply to add nothing, book has %d entries", got)
	}
}

func TestPexRepliesAreCutToTheResponseMax(t *testing.T) {
	server, _ := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	requestAddresses(t, peer, "big-reply")

	if _, err := peer.handleControlMessage(pexReply(t, "big-reply", fakePexAddresses(300, time.Now()))); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if got := len(server.pex.book); got != pexResponseMax {
		t.Fatalf("expected at most %d entries from one reply, got %d", pexResponseMax, got)
	}
}

func TestPexRefusesAnEnormousReply(t *testing.T) {
	server, _ := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	requestAddresses(t, peer, "enormous")

	if _, err := peer.handleControlMessage(pexReply(t, "enormous", fakePexAddresses(6000, time.Now()))); err == nil {
		t.Fatalf("expected a reply of several thousand entries to be refused")
	}
	if got := len(server.pex.book); got != 0 {
		t.Fatalf("expected nothing to be taken from an enormous reply, got %d entries", got)
	}
}

func TestPexLearnedAddressesAreNeitherPersistedNorRelayed(t *testing.T) {
	server, store := newPexTestServer(t)
	peer := newPexTestPeer(t, server, "0xf100d")
	requestAddresses(t, peer, "learn")

	if _, err := peer.handleControlMessage(pexReply(t, "learn", fakePexAddresses(10, time.Now()))); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("addresses learned from a peer must not reach the peerstore before the peer is reached, got %d entries", got)
	}

	other := &mockPexPeer{id: "0xbeef"}
	if err := server.pex.handleRequest(other, PexRequestPayload{Limit: pexResponseMax, Token: "ask"}); err != nil {
		t.Fatalf("handleRequest: %v", err)
	}
	if len(other.sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(other.sent))
	}
	var payload PexAddressesPayload
	if err := decodeMessage(other.sent[0], &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Addresses) != 0 {
		t.Fatalf("addresses this node has not verified must not be handed to other peers, got %d", len(payload.Addresses))
	}
}

func TestPexAddressBookStaysWithinItsCap(t *testing.T) {
	server, _ := newPexTestServer(t)
	now := time.Now()
	for i := 0; i < 5*pexBookMax; i++ {
		server.pex.recordPeer(fmt.Sprintf("0x%064x", i+1), fmt.Sprintf("10.9.%d.%d:26656", (i>>8)&0xff, i&0xff), now)
	}
	if got := len(server.pex.book); got > pexBookMax {
		t.Fatalf("address book holds %d entries, cap is %d", got, pexBookMax)
	}
}

func TestPexSeedsAreKeptWhenTheBookIsFull(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xA2}, 32))
	cfg.EnablePEX = true
	cfg.Seeds = []string{"0xseed@10.7.7.7:26656"}
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	now := time.Now()
	for i := 0; i < 3*pexBookMax; i++ {
		server.pex.recordPeer(fmt.Sprintf("0x%064x", i+1), fmt.Sprintf("10.9.%d.%d:26656", (i>>8)&0xff, i&0xff), now)
	}
	if _, ok := server.pex.book["0xseed"]; !ok {
		t.Fatalf("expected the configured seed to survive eviction")
	}
}

func TestPexAddressBookEvictsUnverifiedEntriesFirst(t *testing.T) {
	server, _ := newPexTestServer(t)
	now := time.Now()
	server.pex.recordPeer("0xverified", "10.8.8.8:26656", now)

	peer := newPexTestPeer(t, server, "0xf100d")
	for round := 0; round < 2*pexBookMax/pexResponseMax; round++ {
		token := fmt.Sprintf("round-%d", round)
		requestAddresses(t, peer, token)
		addrs := fakePexAddresses(pexResponseMax, now)
		for i := range addrs {
			addrs[i].NodeID = fmt.Sprintf("0x%064x", round*pexResponseMax+i+1)
		}
		if _, err := peer.handleControlMessage(pexReply(t, token, addrs)); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if got := len(server.pex.book); got > pexBookMax {
			t.Fatalf("round %d: address book holds %d entries, cap is %d", round, got, pexBookMax)
		}
	}
	if _, ok := server.pex.book["0xverified"]; !ok {
		t.Fatalf("a peer this node reached itself must not be pushed out by addresses it was only told about")
	}
}

func TestPexTokenLengthIsBounded(t *testing.T) {
	server, _ := newPexTestServer(t)
	peer := &mockPexPeer{id: "0xbeef"}
	huge := strings.Repeat("t", 1<<20)

	if err := server.pex.handleRequest(peer, PexRequestPayload{Limit: 8, Token: huge}); err == nil {
		t.Fatalf("expected a request with an oversized token to be refused")
	}
	if got := len(peer.sent); got != 0 {
		t.Fatalf("expected no reply to an oversized token, got %d messages", got)
	}
}

func TestPexAnswersOnlyAFewRequestsInARow(t *testing.T) {
	server, _ := newPexTestServer(t)
	server.pex.recordPeer("0xdead", "10.0.0.2:26656", time.Now())
	peer := &mockPexPeer{id: "0xbeef"}

	for i := 0; i < 50; i++ {
		if err := server.pex.handleRequest(peer, PexRequestPayload{Limit: 8, Token: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if got := len(peer.sent); got == 0 || got > pexRequestBurst {
		t.Fatalf("expected between 1 and %d replies to a burst of 50 requests, got %d", pexRequestBurst, got)
	}
}

func TestPexQueueFullDropsTheReplyWithoutError(t *testing.T) {
	server, _ := newPexTestServer(t)
	server.pex.recordPeer("0xdead", "10.0.0.2:26656", time.Now())
	peer := newPexTestPeer(t, server, "0xf100d")
	for i := 0; i < outboundQueueSize; i++ {
		if err := peer.Enqueue(&Message{Type: MsgTypeTx, Payload: []byte("x")}); err != nil {
			t.Fatalf("fill queue: %v", err)
		}
	}
	if err := server.pex.handleRequest(peer, PexRequestPayload{Limit: 8, Token: "ask"}); err != nil {
		t.Fatalf("a full queue must drop the reply, not fail the request: %v", err)
	}
}

// decodeMessage decodes the payload of a message into v.
func decodeMessage(msg *Message, v any) error {
	return json.Unmarshal(msg.Payload, v)
}
