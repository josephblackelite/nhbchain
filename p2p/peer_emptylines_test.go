package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// Ledger PL-EXT-46 (bug-bounty report of 2026-11-18): a blank line was skipped before any rate limit was consulted and the
// read deadline was renewed on every outer iteration, so a peer could hold an inbound slot with one newline every few seconds,
// no protocol traffic and no rate-limit cost. A blank line now counts against the same limits as a message, and a run of
// them is a protocol violation.

type blankLinePeer struct {
	server *Server
	remote *Server
	conn   net.Conn
}

// connectBlankLinePeer completes a handshake against an inbound server and waits until the peer is registered.
func connectBlankLinePeer(t *testing.T, cfg ServerConfig) *blankLinePeer {
	t.Helper()
	handler := noopHandler{}
	server := NewServer(handler, mustKey(t), cfg)
	remote := NewServer(handler, mustKey(t), cfg)
	left, right := net.Pipe()
	t.Cleanup(func() { right.Close() })
	go server.handleInbound(left)

	reader := bufio.NewReader(right)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	payload, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	if _, err := right.Write(append(data, '\n')); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	p := &blankLinePeer{server: server, remote: remote, conn: right}
	if !p.wait(func() bool { return p.registered() }) {
		t.Fatal("peer never registered after the handshake")
	}
	// Drain what the server writes after the handshake so a blocking pipe never stalls its write loop.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := right.Read(buf); err != nil {
				return
			}
		}
	}()
	return p
}

func (p *blankLinePeer) registered() bool {
	p.server.mu.RLock()
	defer p.server.mu.RUnlock()
	_, ok := p.server.peers[p.remote.nodeID]
	return ok
}

func (p *blankLinePeer) wait(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func (p *blankLinePeer) blank(t *testing.T, n int) {
	t.Helper()
	if n <= 0 {
		return
	}
	if _, err := p.conn.Write(bytes.Repeat([]byte("\n"), n)); err != nil {
		// The peer may already have been dropped; the caller checks registration.
		return
	}
}

func (p *blankLinePeer) message(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(&Message{Type: 1, Payload: []byte("hello")})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if _, err := p.conn.Write(append(data, '\n')); err != nil {
		return
	}
}

func TestBlankLinesCountAgainstThePeerRateLimit(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xD1}, 32))
	cfg.RateMsgsPerSec = 1
	cfg.RateBurst = 4
	cfg.PeerBanDuration = 100 * time.Millisecond
	// A long read timeout, so that only the limit under test can drop the peer within the wait.
	cfg.ReadTimeout = 10 * time.Second
	p := connectBlankLinePeer(t, cfg)
	// Eight blank lines are below the run limit, so only the rate limit (burst 4, 1 a second) can drop the peer.
	p.blank(t, 8)
	if !p.wait(func() bool { return !p.registered() }) {
		t.Fatal("a peer that sent eight blank lines in a burst past a limit of four stayed connected")
	}
}

func TestMessagesStillCountAgainstTheRateLimit(t *testing.T) {
	// The limits moved into one helper for messages and blank lines; messages must still be limited. A long read timeout, so
	// that only the rate limit can drop the peer within the wait.
	cfg := baseConfig(bytes.Repeat([]byte{0xD5}, 32))
	cfg.RateMsgsPerSec = 1
	cfg.RateBurst = 4
	cfg.PeerBanDuration = 100 * time.Millisecond
	cfg.ReadTimeout = 10 * time.Second
	p := connectBlankLinePeer(t, cfg)
	for i := 0; i < 8; i++ {
		p.message(t)
	}
	if !p.wait(func() bool { return !p.registered() }) {
		t.Fatal("a peer that sent eight messages in a burst past a limit of four stayed connected")
	}
}

func TestARunOfBlankLinesIsAProtocolViolation(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xD2}, 32))
	cfg.RateMsgsPerSec = 100_000
	cfg.RateBurst = 100_000
	cfg.ReadTimeout = 10 * time.Second
	p := connectBlankLinePeer(t, cfg)

	p.blank(t, maxConsecutiveEmptyLines)
	time.Sleep(150 * time.Millisecond)
	if !p.registered() {
		t.Fatalf("a peer was dropped after exactly %d blank lines, the allowance", maxConsecutiveEmptyLines)
	}
	p.blank(t, 1)
	if !p.wait(func() bool { return !p.registered() }) {
		t.Fatalf("a peer that sent %d blank lines in a row stayed connected", maxConsecutiveEmptyLines+1)
	}
}

func TestAMessageEndsTheRunOfBlankLines(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xD3}, 32))
	cfg.RateMsgsPerSec = 100_000
	cfg.RateBurst = 100_000
	cfg.ReadTimeout = 10 * time.Second
	p := connectBlankLinePeer(t, cfg)

	for i := 0; i < 3; i++ {
		p.blank(t, maxConsecutiveEmptyLines)
		p.message(t)
	}
	time.Sleep(200 * time.Millisecond)
	if !p.registered() {
		t.Fatal("blank lines separated by real messages were counted as one run")
	}
}

func TestBlankLinesNoLongerKeepASlotPastTheReadTimeout(t *testing.T) {
	// The report: one newline every 100 ms against a 250 ms read timeout kept the peer for many timeouts. With the run limit
	// the peer is dropped after seventeen of them.
	cfg := baseConfig(bytes.Repeat([]byte{0xD4}, 32))
	cfg.RateMsgsPerSec = 100_000
	cfg.RateBurst = 100_000
	cfg.ReadTimeout = 250 * time.Millisecond
	p := connectBlankLinePeer(t, cfg)

	start := time.Now()
	for i := 0; i < 60 && p.registered(); i++ {
		p.blank(t, 1)
		time.Sleep(20 * time.Millisecond)
	}
	if p.registered() {
		t.Fatalf("a peer that sent only newlines for %s still holds its slot", time.Since(start))
	}
}
