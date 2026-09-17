package p2p

import (
	"bufio"
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// TestEnqueueDuringTerminateNeverPanics is the direct regression test for
// NHB-AUDIT-C7: Enqueue and terminate() run concurrently from different
// goroutines with no shared lock. Before this fix, terminate() closed
// p.outbound; Enqueue's own ctx.Done() check and its actual send onto
// p.outbound were two separate, non-atomic steps, so a terminate()
// landing in between them made the send hit a closed channel and panic
// ("send on closed channel"), crashing the entire process -- reproduced
// empirically under ordinary peer churn (228 panics per 200,000 trials),
// no attacker required. This test drives many trials of exactly that
// race directly: a goroutine calling Enqueue in a tight loop while
// another calls terminate concurrently, for enough iterations that the
// pre-fix race would reliably panic at least once. A panic in either
// goroutine fails the whole test binary, which is exactly the failure
// mode being guarded against -- this test's mere survival is the
// assertion.
func TestEnqueueDuringTerminateNeverPanics(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xCD}, 32)
	cfg := baseConfig(genesis)
	cfg.PingInterval = 0

	const trials = 20000
	for i := 0; i < trials; i++ {
		server := NewServer(handler, mustKey(t), cfg)
		left, right := net.Pipe()

		peer := newPeer("peer-race", cfg.ClientVersion, left, bufio.NewReader(left), server, false, false, "")
		go peer.writeLoop()

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				msg := &Message{Type: MsgTypePing, Payload: []byte("x")}
				_ = peer.Enqueue(msg) // error return is expected once shutting down; a panic is not
			}
		}()

		go func() {
			defer wg.Done()
			peer.terminate(false, nil)
		}()

		wg.Wait()
		right.Close()
	}
}

// TestConcurrentEnqueueAndTerminateAcrossManyPeers repeats the same race
// across many peers concurrently (rather than sequentially), closer to
// the real production shape of many connections churning at once.
func TestConcurrentEnqueueAndTerminateAcrossManyPeers(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xEF}, 32)
	cfg := baseConfig(genesis)
	cfg.PingInterval = 0

	const peerCount = 200
	var wg sync.WaitGroup
	wg.Add(peerCount)
	for i := 0; i < peerCount; i++ {
		go func() {
			defer wg.Done()
			server := NewServer(handler, mustKey(t), cfg)
			left, right := net.Pipe()
			defer right.Close()

			peer := newPeer("peer-race-multi", cfg.ClientVersion, left, bufio.NewReader(left), server, false, false, "")
			go peer.writeLoop()

			var inner sync.WaitGroup
			inner.Add(2)
			go func() {
				defer inner.Done()
				for j := 0; j < 50; j++ {
					_ = peer.Enqueue(&Message{Type: MsgTypePing, Payload: []byte("x")})
				}
			}()
			go func() {
				defer inner.Done()
				peer.terminate(false, nil)
			}()
			inner.Wait()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for concurrent peer race trials to finish")
	}
}
