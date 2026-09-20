package rpc

// What the public reads cost, counted in reads from the block store rather than
// in time so that the tests do not depend on the speed of the machine, and what
// happens to a crowd of them at once. These use only the server's public
// behaviour (ServeHTTP and the default configuration) and the test chains'
// helpers, so they can be pointed at an older tree to see what it does there.

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core"
)

// TestUnknownHashCostsAFewReadsHoweverLongTheChain: a lookup for a hash that is
// in no block used to read the last 50,000 blocks, whichever method it came in
// through, and was open to anyone.
func TestUnknownHashCostsAFewReadsHoweverLongTheChain(t *testing.T) {
	for _, blocks := range []int{300, 6000} {
		rc := buildRandomChain(t, 61, blocks, false)
		srv := quietServer(t, rc.Node, costServerConfig())
		for _, tc := range []struct {
			method string
			params []any
		}{
			{"nhb_getTransactionReceipt", []any{unknownHash()}},
			{"nhb_getTransaction", []any{unknownHash()}},
			{"nhb_searchExplorer", []any{unknownHash()}},
		} {
			before := rc.Reads.gets.Load()
			call := doRPC(t, srv, nil, "203.0.113.5:4000", tc.method, tc.params...)
			reads := rc.Reads.gets.Load() - before
			if call.Code != http.StatusOK || call.Resp.Error != nil {
				t.Fatalf("%s: HTTP %d, %s", tc.method, call.Code, call.Body)
			}
			if reads > 6 {
				t.Fatalf("%s on a %d-block chain read the store %d times for a hash that is in no block", tc.method, blocks, reads)
			}
		}
	}
}

// TestKnownHashesAreStillFoundAfterARestart reopens the store and checks that
// every lookup answers as before, that an unknown hash then costs a few reads,
// and that a store which lost its index gets it back when the node starts and
// answers the same again.
func TestKnownHashesAreStillFoundAfterARestart(t *testing.T) {
	rc := buildRandomChain(t, 71, 400, true)
	key := rc.Key
	lookups := func(srv *Server) map[string]string {
		out := map[string]string{}
		inputs := append([]string{unknownHash(), "not-a-hash", "0x", strings.Repeat("ab", 31)}, rc.Hashes...)
		for _, in := range inputs {
			// nhb_getTransaction only: a receipt re-executes the transaction against the
			// node's current state, which a restart does not preserve.
			call := doRPC(t, srv, nil, "203.0.113.5:1", "nhb_getTransaction", in)
			if call.Code != http.StatusOK || call.Resp.Error != nil {
				t.Fatalf("%q: HTTP %d %s", in, call.Code, call.Body)
			}
			out[in] = string(call.Body)
		}
		return out
	}
	before := lookups(quietServer(t, rc.Node, costServerConfig()))
	for _, hx := range rc.Hashes {
		if !strings.Contains(before[hx], hx) {
			t.Fatalf("hash %s was not found before the restart: %s", hx, before[hx])
		}
	}
	rc.CloseDB()

	reopen := func() (*core.Node, *countingDB, func()) {
		db, closeDB := openRandStore(t, true, rc.Dir)
		node, err := core.NewNode(db, key, "", true, false)
		if err != nil {
			t.Fatalf("restart: %v", err)
		}
		return node, db, closeDB
	}
	node2, reads2, close2 := reopen()
	srv2 := quietServer(t, node2, costServerConfig())
	if after := lookups(srv2); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("lookups differ after the restart")
	}
	base := reads2.gets.Load()
	if call := doRPC(t, srv2, nil, "203.0.113.5:2", "nhb_getTransaction", unknownHash()); call.Resp.Error != nil {
		t.Fatalf("unknown hash after restart: %s", call.Body)
	}
	if reads := reads2.gets.Load() - base; reads > 4 {
		t.Fatalf("an unknown hash read the store %d times after the restart", reads)
	}
	close2()

	// Lose the index entirely (a store from before it existed) and restart again:
	// the node rebuilds it at start-up and the answers are the same.
	db3, close3 := openRandStore(t, true, rc.Dir)
	batch := db3.NewBatch()
	if err := batch.Delete([]byte("txHashIndexBackfillDone")); err != nil {
		t.Fatalf("drop the marker: %v", err)
	}
	for _, hx := range rc.Hashes {
		raw, _ := hex.DecodeString(hx)
		if err := batch.Delete(append([]byte("txhash:"), raw...)); err != nil {
			t.Fatalf("drop an entry: %v", err)
		}
	}
	if err := batch.Write(); err != nil {
		t.Fatalf("drop the index: %v", err)
	}
	close3()
	node4, reads4, close4 := reopen()
	defer close4()
	srv4 := quietServer(t, node4, costServerConfig())
	if after := lookups(srv4); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("lookups differ after the index was rebuilt")
	}
	base = reads4.gets.Load()
	doRPC(t, srv4, nil, "203.0.113.5:3", "nhb_getTransaction", unknownHash())
	if reads := reads4.gets.Load() - base; reads > 4 {
		t.Fatalf("an unknown hash read the store %d times after the index was rebuilt", reads)
	}
}

func TestExplorerSnapshotIsComputedOncePerWindowAndHeight(t *testing.T) {
	rc := buildRandomChain(t, 91, 400, false)
	srv := quietServer(t, rc.Node, costServerConfig())
	settleActivityIndex(srv)
	const window = 77 // not the default window, so the loop's snapshot does not serve it

	before := rc.Reads.gets.Load()
	first := doRPC(t, srv, nil, "203.0.113.20:1", "nhb_getExplorerSnapshot", window)
	built := rc.Reads.gets.Load() - before
	if first.Code != http.StatusOK || first.Resp.Error != nil {
		t.Fatalf("snapshot: HTTP %d %s", first.Code, first.Body)
	}
	if built < window {
		t.Fatalf("building a %d-block snapshot read only %d blocks", window, built)
	}
	// Any number of further requests for the same window at the same height are
	// served from the cache: no reads at all.
	before = rc.Reads.gets.Load()
	var wg sync.WaitGroup
	bodies := make([]string, 40)
	for i := range bodies {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			bodies[i] = string(doRPC(t, srv, nil, fmt.Sprintf("203.0.113.%d:2", 30+i), "nhb_getExplorerSnapshot", window).Body)
		}()
	}
	wg.Wait()
	if reads := rc.Reads.gets.Load() - before; reads != 0 {
		t.Fatalf("40 requests for a cached window read the store %d times", reads)
	}
	// The snapshot carries the clock and the mempool size, which may differ from
	// one build to the next but never within one cached snapshot.
	for i, body := range bodies {
		if body != string(first.Body) {
			t.Fatalf("response %d differs from the first", i)
		}
	}

	// A new block invalidates it; the next request rebuilds it once.
	rc.addBlockOn(t)
	before = rc.Reads.gets.Load()
	second := doRPC(t, srv, nil, "203.0.113.21:1", "nhb_getExplorerSnapshot", window)
	if second.Resp.Error != nil || string(second.Body) == string(first.Body) {
		t.Fatalf("the snapshot did not change with a new block")
	}
	// It is recomputed, from the blocks of the window that are already known and
	// the one that is new: a few reads, not a window's worth.
	if reads := rc.Reads.gets.Load() - before; reads < 1 || reads > 6 {
		t.Fatalf("the snapshot for the new height cost %d reads; it should read only the new block (and the one that stopped being the tip)", reads)
	}
}

// TestTxWindowStatsCacheIsBounded: the result cache is keyed by the caller's
// lookbackSeconds, which is any positive number.
func TestTxWindowStatsCacheIsBounded(t *testing.T) {
	rc := buildRandomChain(t, 93, 100, false)
	srv := newTestServer(t, rc.Node, nil, costServerConfig())
	const bound = 32
	for lookback := 1; lookback <= 3*bound; lookback++ {
		call := doRPC(t, srv, nil, "203.0.113.23:1", "nhb_txWindowStats", map[string]any{"lookbackSeconds": lookback})
		if call.Code != http.StatusOK || call.Resp.Error != nil {
			t.Fatalf("lookback %d: HTTP %d %s", lookback, call.Code, call.Body)
		}
	}
	srv.txWindowStatsMu.Lock()
	kept := len(srv.txWindowStatsCache)
	srv.txWindowStatsMu.Unlock()
	if kept > bound {
		t.Fatalf("the result cache holds %d entries after %d distinct lookbacks; the bound is %d", kept, 3*bound, bound)
	}
}

// TestConcurrentHeavyQueriesAreRefusedNotPiledUp: forty clients each ask, at the
// same moment, for a hash that is in no block of a store whose index is not
// complete, which is a scan of the whole chain. The node must serve what it can
// afford and refuse the rest promptly, with the documented error and a hint to
// retry, instead of running all forty at once.
func TestConcurrentHeavyQueriesAreRefusedNotPiledUp(t *testing.T) {
	rc := buildRandomChain(t, 101, 30000, false)
	removeCompletenessMarker(t, rc)
	srv := quietServer(t, rc.Node, costServerConfig())

	const clients = 40
	type result struct {
		call rpcCall
	}
	results := make([]result, clients)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < clients; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i].call = doRPC(t, srv, nil, fmt.Sprintf("203.0.113.%d:5000", 100+i), "nhb_getTransaction", fmt.Sprintf("0x%064x", uint64(i)+1))
		}()
	}
	close(start)
	wg.Wait()

	// A query the pool admitted ends with its answer or, on a machine so busy
	// that a whole-chain scan outlasts the query deadline, with the documented
	// timeout; either way it was admitted and not refused.
	served, refused, timedOut := 0, 0, 0
	for i, r := range results {
		switch {
		case r.call.Code == http.StatusOK && r.call.Resp.Error == nil:
			served++
		case r.call.Code == http.StatusServiceUnavailable:
			timedOut++
		case r.call.Code == http.StatusTooManyRequests && r.call.errCode() == codeRateLimited:
			refused++
			if r.call.Header.Get("Retry-After") == "" {
				t.Fatalf("client %d: a refusal without a Retry-After hint", i)
			}
			if data, ok := errData(r.call); !ok || data["reason"] == nil || data["retryAfterMs"] == nil {
				t.Fatalf("client %d: a refusal must say why and when to retry: %s", i, r.call.Body)
			}
			// The bound is far above what a refusal takes (the queue wait is a tenth of a
			// second) and far below the seconds a scan takes: on a busy machine a
			// goroutine can be woken late, and that is not the pool's doing.
			if r.call.Duration > 10*time.Second {
				t.Fatalf("client %d: a refusal took %s", i, r.call.Duration)
			}
		default:
			t.Fatalf("client %d: unexpected outcome HTTP %d: %s", i, r.call.Code, r.call.Body)
		}
	}
	if refused == 0 {
		t.Fatalf("all %d concurrent whole-chain scans ran; none was refused", clients)
	}
	if served+timedOut == 0 {
		t.Fatalf("none of the %d queries was admitted", clients)
	}
	t.Logf("%d served, %d timed out, %d refused", served, timedOut, refused)
}
