package rpc

// Admission control for the public read queries that are not cheap.
//
// Most read methods answer in microseconds. A few scan blocks, or hold the
// node's exclusive state lock while they work, and their cost grows with the
// chain: a lookup for a hash that is in no block used to read 50,000 blocks
// and a public lending read replayed the whole chain inside the state lock. On
// a small validator host that competes directly with block production, and one
// client sending requests as fast as the per-address limit allows is enough to
// starve it.
//
// queryGate is a small pool that the heavy part of those queries must hold a
// slot in: a global limit (kept below the CPU count, so block production,
// validation and commit always have a core of their own), a limit per client,
// and a short bounded queue. A query that cannot get a slot in time is refused
// straight away with the rate-limit error code and a retry hint, and never
// touches the chain. Queries that are answered from the indexes and caches do
// not need a slot at all: a query is charged for the blocks it has to read,
// and takes a slot only once that passes a small free budget, or at the point
// where it must take the state lock. Every gated query also runs under a
// deadline and stops reading as soon as its client disconnects.
//
// None of this touches consensus: it only decides whether, and how soon, a
// read is served.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"time"

	"nhbchain/observability"
)

const (
	// defaultQueryMaxPerClient is how many heavy queries one client may have in
	// flight (running or waiting) at once.
	defaultQueryMaxPerClient = 1
	// defaultQueryQueueDepth is how many heavy queries may wait for a slot.
	defaultQueryQueueDepth = 8
	// defaultQueryQueueWait is how long a heavy query waits for a slot before it
	// is refused: long enough to ride out the short holds (a receipt's
	// simulation), too short to sit behind a scan.
	defaultQueryQueueWait = 100 * time.Millisecond
	// defaultQueryTimeout bounds one gated query, from admission to answer.
	defaultQueryTimeout = 10 * time.Second

	// queryFreeBlockBudget is how many blocks a query may read before it has to
	// hold a slot: enough for a lookup answered near the tip, far below a scan.
	// It is small because a query the pool then refuses has spent this much
	// before it was refused.
	queryFreeBlockBudget = 8

	queryRetryAfterMin = time.Second
	queryRetryAfterMax = 30 * time.Second

	// codeQueryTimeout is returned when a gated query ran out of time. It sits
	// next to codeRateLimited, which is what a query refused for lack of
	// capacity gets.
	codeQueryTimeout = -32021
)

// Why a query was refused, as it appears in the error's data and in metrics.
const (
	queryBusyClient  = "client_limit"
	queryBusyQueue   = "queue_full"
	queryBusyTimeout = "wait_timeout"
)

// defaultQueryConcurrency is the global limit when none is configured: half
// the CPUs, at least one and at most two. On a two-core machine this is one,
// which leaves the other core to consensus.
func defaultQueryConcurrency() int {
	n := runtime.GOMAXPROCS(0) / 2
	if n < 1 {
		n = 1
	}
	if n > 2 {
		n = 2
	}
	return n
}

// isGatedQueryMethod lists the public read methods that can run heavy work and
// therefore run under a deadline and may take a slot in the pool (see the file
// comment). Every other method is bounded by a small constant.
func isGatedQueryMethod(method string) bool {
	switch method {
	case "nhb_getTransaction", "nhb_getTransactionReceipt", "nhb_searchExplorer",
		"nhb_getTransactionHistory", "nhb_getAddressActivity", "nhb_getExplorerSnapshot",
		"nhb_getLatestTransactions", "nhb_txWindowStats",
		"lending_getMarket", "lend_getPools", "lending_getUserAccount",
		"market_listOpenListings", "market_getMyListings", "market_getMyFills",
		"swap_voucher_list", "swap_voucher_export":
		return true
	}
	return false
}

// queryBusyError is the outcome for a query the pool refused.
type queryBusyError struct {
	Reason     string
	RetryAfter time.Duration
}

func (e *queryBusyError) Error() string {
	return fmt.Sprintf("query capacity exceeded (%s); retry in %s", e.Reason, e.RetryAfter.Round(time.Millisecond))
}

type gateWaiter struct {
	ready   chan struct{}
	granted bool
}

type queryGate struct {
	global    int
	perClient int
	depth     int
	wait      time.Duration

	mu       sync.Mutex
	running  int
	inflight map[string]int // per client, running and waiting
	queue    []*gateWaiter
	avgHold  time.Duration // moving average of how long a slot is held
}

func newQueryGate(global, perClient, depth int, wait time.Duration) *queryGate {
	if global <= 0 {
		global = defaultQueryConcurrency()
	}
	if perClient <= 0 {
		perClient = defaultQueryMaxPerClient
	}
	if depth <= 0 {
		depth = defaultQueryQueueDepth
	}
	if wait <= 0 {
		wait = defaultQueryQueueWait
	}
	return &queryGate{
		global:    global,
		perClient: perClient,
		depth:     depth,
		wait:      wait,
		inflight:  make(map[string]int),
		avgHold:   250 * time.Millisecond,
	}
}

// retryAfterLocked estimates when a slot is likely to be free: the time the
// work ahead of a new arrival takes, at the recently observed hold time.
func (g *queryGate) retryAfterLocked() time.Duration {
	ahead := g.running + len(g.queue)
	d := g.avgHold * time.Duration(ahead/g.global+1)
	if d < queryRetryAfterMin {
		d = queryRetryAfterMin
	}
	if d > queryRetryAfterMax {
		d = queryRetryAfterMax
	}
	return d
}

func (g *queryGate) busyLocked(reason string) *queryBusyError {
	return &queryBusyError{Reason: reason, RetryAfter: g.retryAfterLocked()}
}

// clientLimit is how many queries one client key may have in flight. A key that
// many callers share (see limiterKeyFor: local services and traffic that reached
// a listed proxy with no client address) is not one client, so it may fill the
// queue as well as the pool but no more.
func (g *queryGate) clientLimit(shared bool) int {
	if shared {
		return g.perClient + g.depth + g.global
	}
	return g.perClient
}

// acquire takes a slot for client, waiting a short while when the pool is full.
// The returned function gives the slot back; it is safe to call more than once.
func (g *queryGate) acquire(ctx context.Context, client string, shared bool) (func(), error) {
	g.mu.Lock()
	if g.inflight[client] >= g.clientLimit(shared) {
		err := g.busyLocked(queryBusyClient)
		g.mu.Unlock()
		return nil, err
	}
	if g.running < g.global && len(g.queue) == 0 {
		g.running++
		g.inflight[client]++
		g.mu.Unlock()
		return g.releaser(client), nil
	}
	if len(g.queue) >= g.depth {
		err := g.busyLocked(queryBusyQueue)
		g.mu.Unlock()
		return nil, err
	}
	w := &gateWaiter{ready: make(chan struct{})}
	g.queue = append(g.queue, w)
	g.inflight[client]++
	g.mu.Unlock()

	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case <-w.ready:
		return g.releaser(client), nil
	case <-timer.C:
	case <-ctx.Done():
	}

	g.mu.Lock()
	if w.granted {
		// A slot was handed over just as the wait ended; take it.
		g.mu.Unlock()
		return g.releaser(client), nil
	}
	for i, queued := range g.queue {
		if queued == w {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			break
		}
	}
	g.dropClientLocked(client)
	err := g.busyLocked(queryBusyTimeout)
	g.mu.Unlock()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return nil, err
}

func (g *queryGate) dropClientLocked(client string) {
	if g.inflight[client] <= 1 {
		delete(g.inflight, client)
		return
	}
	g.inflight[client]--
}

func (g *queryGate) releaser(client string) func() {
	start := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			held := time.Since(start)
			g.mu.Lock()
			g.running--
			g.dropClientLocked(client)
			g.avgHold += (held - g.avgHold) / 4
			for g.running < g.global && len(g.queue) > 0 {
				next := g.queue[0]
				g.queue = g.queue[1:]
				next.granted = true
				g.running++
				close(next.ready)
			}
			g.mu.Unlock()
		})
	}
}

// load reports how many queries hold a slot and how many are waiting.
func (g *queryGate) load() (running, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running, len(g.queue)
}

type queryTicketKey struct{}

// queryTicket is one request's claim on the pool. It is created for a gated
// method, carried in the request context, and gives its slot back when the
// request ends. A nil ticket (a request that is not gated, or a caller that has
// no request, such as the background refresh) is never charged and never held.
type queryTicket struct {
	gate    *queryGate
	client  string
	shared  bool
	release func()
	blocks  int
}

func ticketFrom(ctx context.Context) *queryTicket {
	t, _ := ctx.Value(queryTicketKey{}).(*queryTicket)
	return t
}

// require takes a slot now. Use it before work that is heavy however it is
// answered, such as holding the state lock.
func (t *queryTicket) require(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || t.release != nil {
		return nil
	}
	release, err := t.gate.acquire(ctx, t.client, t.shared)
	if err != nil {
		return err
	}
	t.release = release
	return nil
}

// chargeBlocks accounts for n more blocks read, stops the query if its context
// is done, and takes a slot once the free budget is spent.
func (t *queryTicket) chargeBlocks(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	t.blocks += n
	if t.release == nil && t.blocks > queryFreeBlockBudget {
		return t.require(ctx)
	}
	return nil
}

func (t *queryTicket) finish() {
	if t != nil && t.release != nil {
		t.release()
		t.release = nil
	}
}

// bufferedResponse holds a response until the request's slot in the pool has
// been given back, and then writes it out. Writing to a client that reads
// slowly can block for as long as it likes (the server has no write timeout
// unless one is configured), and that must not be time spent holding a slot.
type bufferedResponse struct {
	dst    http.ResponseWriter
	status int
	wrote  bool
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.dst.Header() }

func (b *bufferedResponse) WriteHeader(code int) {
	if !b.wrote {
		b.status, b.wrote = code, true
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if !b.wrote {
		b.status, b.wrote = http.StatusOK, true
	}
	return b.body.Write(p)
}

func (b *bufferedResponse) flush() {
	if !b.wrote {
		return
	}
	if b.status != http.StatusOK {
		b.dst.WriteHeader(b.status)
	}
	if b.body.Len() > 0 {
		_, _ = b.dst.Write(b.body.Bytes())
	}
}

// admitHeavy takes a slot for a handler whose work is heavy however it is
// answered. It reports whether the handler may go on; when it may not, the
// response has been written.
func (s *Server) admitHeavy(w http.ResponseWriter, r *http.Request, req *RPCRequest) bool {
	if err := ticketFrom(r.Context()).require(r.Context()); err != nil {
		if !s.writeQueryFailure(w, req.ID, req.Method, err) {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "query admission failed", nil)
		}
		return false
	}
	return true
}

// writeQueryFailure answers a query that the pool or its deadline stopped and
// reports whether err was such an outcome; any other error is left to the
// caller. A refused query gets HTTP 429 with the rate-limit code and a
// Retry-After hint, a query that ran out of time gets HTTP 503, and a query
// whose client went away gets nothing that anyone will read.
func (s *Server) writeQueryFailure(w http.ResponseWriter, id interface{}, method string, err error) bool {
	var busy *queryBusyError
	switch {
	case errors.As(err, &busy):
		secs := int((busy.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		if rec, ok := w.(*rpcResponseRecorder); ok {
			rec.throttle = "query_capacity"
		}
		module, _ := moduleAndMethod(method)
		observability.RPC().RecordLimiterHit("query_"+busy.Reason, module, method)
		writeError(w, http.StatusTooManyRequests, id, codeRateLimited,
			"RPC query capacity exceeded; retry later",
			map[string]interface{}{"reason": busy.Reason, "retryAfterMs": busy.RetryAfter.Milliseconds()})
		return true
	case errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, id, codeQueryTimeout, "RPC query exceeded its time budget", nil)
		return true
	case errors.Is(err, context.Canceled):
		w.WriteHeader(499)
		return true
	}
	return false
}
