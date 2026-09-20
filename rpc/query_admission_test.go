package rpc

// Tests of what the query pool does to a public read: a query the pool refuses
// stops before it has read more than its small free budget, the lending reads
// are refused before they touch the state lock, and a query whose deadline
// passes or whose client goes away stops reading. Counted in reads from the
// block store, not in time.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func gatedTestServer(tb testing.TB, rc *randChain, cfg ServerConfig) *Server {
	tb.Helper()
	base := costServerConfig()
	base.QueryMaxConcurrent = cfg.QueryMaxConcurrent
	base.QueryMaxPerClient = cfg.QueryMaxPerClient
	base.QueryQueueDepth = cfg.QueryQueueDepth
	base.QueryQueueWait = cfg.QueryQueueWait
	base.QueryTimeout = cfg.QueryTimeout
	return quietServer(tb, rc.Node, base)
}

func TestRefusedQueryStopsBeforeReadingMuchOfTheChain(t *testing.T) {
	rc := buildRandomChain(t, 81, 3000, false)
	removeCompletenessMarker(t, rc)
	srv := gatedTestServer(t, rc, ServerConfig{QueryMaxConcurrent: 1, QueryQueueDepth: 1, QueryQueueWait: 20 * time.Millisecond})
	held, err := srv.queryGate.acquire(context.Background(), "someone-else", false)
	if err != nil {
		t.Fatalf("hold the pool: %v", err)
	}
	defer held()

	before := rc.Reads.gets.Load()
	call := doRPC(t, srv, nil, "203.0.113.7:5000", "nhb_getTransaction", unknownHash())
	reads := rc.Reads.gets.Load() - before

	if call.Code != http.StatusTooManyRequests || call.errCode() != codeRateLimited {
		t.Fatalf("expected HTTP 429 with the rate-limit code, got HTTP %d: %s", call.Code, call.Body)
	}
	if ra := call.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("a refused query must carry a Retry-After hint, got %q", ra)
	}
	data, _ := call.Resp.Error.Data.(map[string]interface{})
	if data["reason"] == nil || data["retryAfterMs"] == nil {
		t.Fatalf("the refusal must say why and when to retry: %v", call.Resp.Error.Data)
	}
	if reads > int64(queryFreeBlockBudget)+8 {
		t.Fatalf("a refused query read the store %d times; its free budget is %d blocks", reads, queryFreeBlockBudget)
	}
	if call.Duration > 250*time.Millisecond {
		t.Fatalf("a refused query took %s", call.Duration)
	}
}

func TestBusyPoolRefusesLendingReadsBeforeTheyTouchState(t *testing.T) {
	rc := buildRandomChain(t, 82, 200, false)
	srv := gatedTestServer(t, rc, ServerConfig{QueryMaxConcurrent: 1, QueryQueueDepth: 1, QueryQueueWait: 20 * time.Millisecond})
	held, err := srv.queryGate.acquire(context.Background(), "someone-else", false)
	if err != nil {
		t.Fatalf("hold the pool: %v", err)
	}
	for _, method := range []string{"lending_getMarket", "lend_getPools", "market_listOpenListings"} {
		before := rc.Reads.gets.Load()
		call := doRPC(t, srv, nil, "203.0.113.8:5000", method)
		if call.Code != http.StatusTooManyRequests || call.errCode() != codeRateLimited {
			t.Fatalf("%s: expected HTTP 429 / %d while the pool is full, got HTTP %d: %s", method, codeRateLimited, call.Code, call.Body)
		}
		if reads := rc.Reads.gets.Load() - before; reads != 0 {
			t.Fatalf("%s read the store %d times before being refused", method, reads)
		}
	}
	held()
	for _, method := range []string{"lending_getMarket", "lend_getPools", "market_listOpenListings"} {
		if call := doRPC(t, srv, nil, "203.0.113.8:5000", method); call.Code != http.StatusOK || call.Resp.Error != nil {
			t.Fatalf("%s should answer once the pool has room: HTTP %d %s", method, call.Code, call.Body)
		}
	}
	if running, waiting := srv.queryGate.load(); running != 0 || waiting != 0 {
		t.Fatalf("slots leaked: %d running, %d waiting", running, waiting)
	}
}

func TestQueryDeadlineStopsAScan(t *testing.T) {
	rc := buildRandomChain(t, 83, 20000, false)
	removeCompletenessMarker(t, rc)
	srv := gatedTestServer(t, rc, ServerConfig{QueryTimeout: time.Millisecond})
	before := rc.Reads.gets.Load()
	call := doRPC(t, srv, nil, "203.0.113.9:5000", "nhb_getTransaction", unknownHash())
	reads := rc.Reads.gets.Load() - before
	if call.Code != http.StatusServiceUnavailable || call.errCode() != codeQueryTimeout {
		t.Fatalf("expected HTTP 503 with code %d, got HTTP %d: %s", codeQueryTimeout, call.Code, call.Body)
	}
	if reads > 15000 {
		t.Fatalf("the deadline did not stop the scan: %d reads of a %d-block chain", reads, rc.Height)
	}
	if running, waiting := srv.queryGate.load(); running != 0 || waiting != 0 {
		t.Fatalf("slots leaked: %d running, %d waiting", running, waiting)
	}
}

func TestClientDisconnectStopsAScan(t *testing.T) {
	rc := buildRandomChain(t, 84, 30000, false)
	removeCompletenessMarker(t, rc)
	srv := gatedTestServer(t, rc, ServerConfig{QueryTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	before := rc.Reads.gets.Load()
	done := make(chan rpcCall, 1)
	go func() { done <- doRPC(t, srv, ctx, "203.0.113.10:5000", "nhb_getTransactionReceipt", unknownHash()) }()
	// Let the scan get going, then hang up.
	deadline := time.Now().Add(5 * time.Second)
	for rc.Reads.gets.Load()-before < 200 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case call := <-done:
		reads := rc.Reads.gets.Load() - before
		if call.Code != 499 {
			t.Fatalf("expected the cancelled request to end with 499, got HTTP %d: %s", call.Code, call.Body)
		}
		if reads >= 29000 {
			t.Fatalf("the scan went on after the client left: %d reads", reads)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the scan was still running 5s after its client disconnected")
	}
	if running, waiting := srv.queryGate.load(); running != 0 || waiting != 0 {
		t.Fatalf("slots leaked: %d running, %d waiting", running, waiting)
	}
}

func TestSnapshotCacheKeepsAFewWindowsOnly(t *testing.T) {
	rc := buildRandomChain(t, 92, 100, false)
	srv := newTestServer(t, rc.Node, nil, costServerConfig())
	for window := 10; window < 10+3*explorerSnapshotCacheWindows; window++ {
		if call := doRPC(t, srv, nil, "203.0.113.22:1", "nhb_getExplorerSnapshot", window); call.Resp.Error != nil {
			t.Fatalf("window %d: %s", window, call.Body)
		}
	}
	srv.snapshotCache.mu.Lock()
	kept := len(srv.snapshotCache.entries)
	srv.snapshotCache.mu.Unlock()
	if kept > explorerSnapshotCacheWindows {
		t.Fatalf("the snapshot cache holds %d windows, the bound is %d", kept, explorerSnapshotCacheWindows)
	}
}

// stallingWriter is a client that has stopped reading: the first write to it
// blocks until it is released.
type stallingWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	status  int
	body    bytes.Buffer
}

func newStallingWriter() *stallingWriter {
	return &stallingWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *stallingWriter) Header() http.Header { return w.header }

func (w *stallingWriter) WriteHeader(code int) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.status = code
}

func (w *stallingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.body.Write(p)
}

// TestSlowReaderDoesNotHoldASlot: writing a response to a client that reads
// slowly can block for as long as the client likes, which must not be time
// spent holding one of the pool's few slots.
func TestSlowReaderDoesNotHoldASlot(t *testing.T) {
	rc := buildRandomChain(t, 111, 200, false)
	srv := gatedTestServer(t, rc, ServerConfig{QueryMaxConcurrent: 1})
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "lending_getMarket", "params": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.40:1000"
	w := newStallingWriter()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the handler never started writing its response")
	}
	// The handler is now writing to a client that is not reading.
	if running, waiting := srv.queryGate.load(); running != 0 || waiting != 0 {
		t.Fatalf("a query holds a slot (%d running, %d waiting) while it writes to a slow client", running, waiting)
	}
	// Another client is served meanwhile.
	if call := doRPC(t, srv, nil, "203.0.113.41:1", "lending_getMarket"); call.Code != http.StatusOK || call.Resp.Error != nil {
		t.Fatalf("a second client was not served while the first one read slowly: HTTP %d %s", call.Code, call.Body)
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the handler never finished")
	}
	var resp RPCResponse
	decodeInto(t, w.body.Bytes(), &resp)
	if resp.Error != nil || resp.Result == nil {
		t.Fatalf("the slow client did not get its response: %s", w.body.String())
	}
}
