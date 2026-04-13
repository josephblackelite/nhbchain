package p2p

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
)

type writeResult struct {
	n   int
	err error
}

func TestPeerReadLoopRejectsOversizedLine(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAB}, 32)

	cfg := baseConfig(genesis)
	cfg.MaxMessageBytes = 128
	cfg.RateMsgsPerSec = 1000
	cfg.RateBurst = 1000
	cfg.ReadTimeout = time.Second
	cfg.WriteTimeout = time.Second
	cfg.PingInterval = 0

	server := NewServer(handler, mustKey(t), cfg)

	left, right := net.Pipe()
	defer right.Close()

	peer := newPeer("peer-oversized", cfg.ClientVersion, left, bufio.NewReader(left), server, false, false, "")
	server.mu.Lock()
	server.peers[peer.id] = peer
	server.mu.Unlock()

	done := make(chan struct{})
	go func() {
		peer.readLoop()
		close(done)
	}()

	payload := bytes.Repeat([]byte{'x'}, 8192)
	results := make(chan writeResult, 1)
	go func() {
		total := 0
		chunk := 512
		for total < len(payload) {
			end := total + chunk
			if end > len(payload) {
				end = len(payload)
			}
			n, err := right.Write(payload[total:end])
			total += n
			if err != nil {
				results <- writeResult{n: total, err: err}
				return
			}
		}
		results <- writeResult{n: total, err: nil}
	}()

	select {
	case <-peer.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not close after oversized message")
	}

	var res writeResult
	select {
	case res = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not finish")
	}

	if res.err == nil {
		t.Fatal("expected writer to fail after protocol violation")
	}
	if res.n >= len(payload) {
		t.Fatalf("writer sent entire payload (%d bytes) despite limit", res.n)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit")
	}

	server.mu.Lock()
	metrics := server.metrics[peer.id]
	server.mu.Unlock()
	if metrics == nil || metrics.invalid == 0 {
		t.Fatalf("expected invalid metric to be recorded, got %+v", metrics)
	}
}
