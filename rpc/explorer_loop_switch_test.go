package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func loopServer(t *testing.T, rc *randChain, disabled bool) *Server {
	t.Helper()
	cfg := costServerConfig()
	cfg.DisableExplorerLoop = disabled
	return newTestServer(t, rc.Node, nil, cfg)
}

// By default the loop runs, as it always has.
func TestExplorerLoopRunsByDefault(t *testing.T) {
	rc := buildRandomChain(t, 141, 60, false)
	srv := loopServer(t, rc, false)
	deadline := time.Now().Add(10 * time.Second)
	for srv.cachedExplorerSnapshot(explorerDefaultRecentBlocks) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the background loop never produced a snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With the switch set nothing runs in the background, a snapshot that is asked
// for is built on demand, and the stream refuses connections.
func TestDisabledExplorerLoopDoesNothingInTheBackgroundAndStillServesSnapshots(t *testing.T) {
	rc := buildRandomChain(t, 142, 300, false)
	srv := loopServer(t, rc, true)

	before := rc.Reads.gets.Load()
	time.Sleep(400 * time.Millisecond) // the loop would have built its first snapshot by now
	if srv.cachedExplorerSnapshot(explorerDefaultRecentBlocks) != nil {
		t.Fatalf("a snapshot was built in the background although the loop is disabled")
	}
	if reads := rc.Reads.gets.Load() - before; reads != 0 {
		t.Fatalf("a disabled loop still read the store %d times in the background", reads)
	}
	if _, _, complete := srv.currentExplorerActivityTotals(); complete {
		t.Fatalf("the payment index advanced in the background although the loop is disabled")
	}

	// A request builds it, on demand, and the all-time totals move on with it.
	call := doRPC(t, srv, nil, "203.0.113.5:1", "nhb_getExplorerSnapshot")
	if call.Code != http.StatusOK || call.Resp.Error != nil {
		t.Fatalf("snapshot on demand: HTTP %d %s", call.Code, call.Body)
	}
	var result struct {
		Result ExplorerSnapshotResult `json:"result"`
	}
	if err := json.Unmarshal(call.Body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Result.LatestHeight != rc.Height || !result.Result.ActivityIndexComplete {
		t.Fatalf("the on-demand snapshot is not current: height %d (want %d), index complete %v", result.Result.LatestHeight, rc.Height, result.Result.ActivityIndexComplete)
	}
	// It is the snapshot the loop would have built.
	want, err := srv.legacyBuildExplorerSnapshot(explorerDefaultRecentBlocks)
	if err != nil {
		t.Fatalf("reference snapshot: %v", err)
	}
	if a, b := mustJSON(t, normalizeSnapshot(want)), mustJSON(t, normalizeSnapshot(&result.Result)); a != b {
		t.Fatalf("the on-demand default snapshot differs from the reference\nwant %s\n got %s", a, b)
	}
	// And asking again at the same height is free.
	before = rc.Reads.gets.Load()
	doRPC(t, srv, nil, "203.0.113.6:1", "nhb_getExplorerSnapshot")
	if reads := rc.Reads.gets.Load() - before; reads != 0 {
		t.Fatalf("a second request at the same height read the store %d times", reads)
	}

	// The stream refuses connections instead of idling.
	rec := httptest.NewRecorder()
	srv.handleExplorerWS(rec, httptest.NewRequest(http.MethodGet, "/ws/explorer", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ws/explorer with the loop disabled: HTTP %d, want 503", rec.Code)
	}
}
