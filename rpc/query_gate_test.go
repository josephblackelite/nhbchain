package rpc

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustBusy(t *testing.T, err error, reason string) *queryBusyError {
	t.Helper()
	var busy *queryBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected a busy error (%s), got %v", reason, err)
	}
	if busy.Reason != reason {
		t.Fatalf("expected reason %q, got %q", reason, busy.Reason)
	}
	if busy.RetryAfter < queryRetryAfterMin || busy.RetryAfter > queryRetryAfterMax {
		t.Fatalf("retry hint %s is outside [%s, %s]", busy.RetryAfter, queryRetryAfterMin, queryRetryAfterMax)
	}
	return busy
}

func waitForWaiting(t *testing.T, g *queryGate, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, waiting := g.load(); waiting == want {
			return
		}
		if time.Now().After(deadline) {
			_, waiting := g.load()
			t.Fatalf("waited for %d queued queries, have %d", want, waiting)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertGateIdle(t *testing.T, g *queryGate) {
	t.Helper()
	// Queries release their slot on their own goroutines; give them a moment.
	deadline := time.Now().Add(time.Second)
	for {
		running, waiting := g.load()
		g.mu.Lock()
		clients := len(g.inflight)
		g.mu.Unlock()
		if running == 0 && waiting == 0 && clients == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	running, waiting := g.load()
	g.mu.Lock()
	clients := len(g.inflight)
	g.mu.Unlock()
	if running != 0 || waiting != 0 || clients != 0 {
		t.Fatalf("gate is not idle: %d running, %d waiting, %d clients tracked", running, waiting, clients)
	}
}

func TestQueryGateNeverExceedsTheGlobalLimit(t *testing.T) {
	g := newQueryGate(2, 1, 4, 200*time.Millisecond)
	var running, peak, admitted, refused int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		client := fmt.Sprintf("client-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.acquire(context.Background(), client, false)
			if err != nil {
				atomic.AddInt32(&refused, 1)
				return
			}
			atomic.AddInt32(&admitted, 1)
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			release()
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("%d queries held a slot at once; the limit is 2", peak)
	}
	if peak < 2 || admitted < 2 {
		t.Fatalf("the pool never used its capacity: peak %d, admitted %d", peak, admitted)
	}
	assertGateIdle(t, g)
}

func TestQueryGateLimitsEachClient(t *testing.T) {
	g := newQueryGate(4, 1, 8, time.Second)
	ctx := context.Background()
	first, err := g.acquire(ctx, "a", false)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	start := time.Now()
	_, err = g.acquire(ctx, "a", false)
	mustBusy(t, err, queryBusyClient)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a client over its limit was refused after %s, not at once", took)
	}
	other, err := g.acquire(ctx, "b", false)
	if err != nil {
		t.Fatalf("another client should get a slot: %v", err)
	}
	first()
	again, err := g.acquire(ctx, "a", false)
	if err != nil {
		t.Fatalf("a client whose query finished should get a slot again: %v", err)
	}
	again()
	other()
	assertGateIdle(t, g)
}

func TestQueryGateSharedKeysMayFillTheQueue(t *testing.T) {
	// A key many callers share (see limiterKeyFor) is not one client: it may
	// take the pool and the queue, but no more than that.
	g := newQueryGate(1, 1, 2, 300*time.Millisecond)
	ctx := context.Background()
	hold, err := g.acquire(ctx, "shared", true)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			release, err := g.acquire(ctx, "shared", true)
			if err == nil {
				release()
			}
			results <- err
		}()
	}
	waitForWaiting(t, g, 2)
	_, err = g.acquire(ctx, "shared", true)
	mustBusy(t, err, queryBusyQueue)
	hold()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("a queued query of the shared key should have run: %v", err)
		}
	}
	assertGateIdle(t, g)
}

func TestQueryGateQueueIsBoundedAndWaitsAreShort(t *testing.T) {
	g := newQueryGate(1, 1, 2, 80*time.Millisecond)
	ctx := context.Background()
	hold, err := g.acquire(ctx, "holder", false)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	waited := make(chan error, 2)
	started := time.Now()
	for i := 0; i < 2; i++ {
		client := fmt.Sprintf("waiter-%d", i)
		go func() {
			_, err := g.acquire(ctx, client, false)
			waited <- err
		}()
	}
	waitForWaiting(t, g, 2)

	// The queue is full: the next arrival is refused at once, without waiting.
	refusedAt := time.Now()
	_, err = g.acquire(ctx, "late", false)
	mustBusy(t, err, queryBusyQueue)
	if took := time.Since(refusedAt); took > 250*time.Millisecond {
		t.Fatalf("a query that found the queue full took %s to be refused", took)
	}

	// The two that queued give up after the wait, with a retry hint.
	for i := 0; i < 2; i++ {
		mustBusy(t, <-waited, queryBusyTimeout)
	}
	if took := time.Since(started); took < 60*time.Millisecond || took > 3*time.Second {
		t.Fatalf("queued queries gave up after %s, want about 80ms", took)
	}
	hold()
	assertGateIdle(t, g)
}

func TestQueryGateGrantsSlotsInArrivalOrder(t *testing.T) {
	g := newQueryGate(1, 1, 4, 2*time.Second)
	ctx := context.Background()
	hold, err := g.acquire(ctx, "holder", false)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	order := make(chan string, 3)
	release := make(chan struct{})
	for i, name := range []string{"first", "second", "third"} {
		name := name
		go func() {
			done, err := g.acquire(ctx, name, false)
			if err != nil {
				order <- "error:" + name
				return
			}
			order <- name
			<-release
			done()
		}()
		waitForWaiting(t, g, i+1)
	}
	hold()
	for _, want := range []string{"first", "second", "third"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("slot went to %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no query was granted a slot; wanted %q", want)
		}
		release <- struct{}{}
	}
	assertGateIdle(t, g)
}

func TestQueryGateReleasesWhenAWaiterIsCancelled(t *testing.T) {
	g := newQueryGate(1, 1, 4, 5*time.Second)
	hold, err := g.acquire(context.Background(), "holder", false)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, "waiter", false)
		result <- err
	}()
	waitForWaiting(t, g, 1)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled waiter should get the context error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("a cancelled waiter is still waiting")
	}
	if _, waiting := g.load(); waiting != 0 {
		t.Fatalf("the cancelled waiter is still queued")
	}
	// It must not have taken a slot or a client entry with it.
	hold()
	assertGateIdle(t, g)
	again, err := g.acquire(context.Background(), "waiter", false)
	if err != nil {
		t.Fatalf("the cancelled client should be able to try again: %v", err)
	}
	again()
}

func TestQueryGateReleaseIsIdempotent(t *testing.T) {
	g := newQueryGate(1, 1, 1, 20*time.Millisecond)
	release, err := g.acquire(context.Background(), "a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	release()
	first, err := g.acquire(context.Background(), "b", false)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	// A double release must not have opened a second slot.
	if _, err := g.acquire(context.Background(), "c", false); err == nil {
		t.Fatalf("a second slot appeared after a double release")
	}
	first()
	assertGateIdle(t, g)
}

func TestQueryGateSurvivesAStorm(t *testing.T) {
	g := newQueryGate(2, 2, 6, 5*time.Millisecond)
	var wg sync.WaitGroup
	var running, peak int32
	for w := 0; w < 32; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 200; i++ {
				ctx := context.Background()
				var cancel context.CancelFunc
				if rng.Intn(4) == 0 {
					ctx, cancel = context.WithTimeout(ctx, time.Duration(rng.Intn(3))*time.Millisecond)
				}
				release, err := g.acquire(ctx, fmt.Sprintf("c%d", rng.Intn(6)), rng.Intn(5) == 0)
				if err == nil {
					n := atomic.AddInt32(&running, 1)
					for {
						p := atomic.LoadInt32(&peak)
						if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
							break
						}
					}
					time.Sleep(time.Duration(rng.Intn(200)) * time.Microsecond)
					atomic.AddInt32(&running, -1)
					release()
				}
				if cancel != nil {
					cancel()
				}
			}
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak concurrency %d exceeded the limit of 2", peak)
	}
	assertGateIdle(t, g)
}

func TestQueryGateDefaultsAndConcurrencyRule(t *testing.T) {
	g := newQueryGate(0, 0, 0, 0)
	if g.global < 1 || g.global > 2 || g.perClient != 1 || g.depth != defaultQueryQueueDepth || g.wait != defaultQueryQueueWait {
		t.Fatalf("unexpected defaults: %+v", g)
	}
	if n := defaultQueryConcurrency(); n < 1 || n > 2 {
		t.Fatalf("default concurrency %d is outside [1, 2]", n)
	}
}

func TestQueryTicketChargesBlocksAndTakesASlotPastTheFreeBudget(t *testing.T) {
	g := newQueryGate(1, 1, 1, 20*time.Millisecond)
	ticket := &queryTicket{gate: g, client: "x"}
	ctx := context.WithValue(context.Background(), queryTicketKey{}, ticket)
	for i := 0; i < queryFreeBlockBudget; i++ {
		if err := ticket.chargeBlocks(ctx, 1); err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
	}
	if running, _ := g.load(); running != 0 {
		t.Fatalf("a query within the free budget holds a slot")
	}
	if err := ticket.chargeBlocks(ctx, 1); err != nil {
		t.Fatalf("crossing the budget: %v", err)
	}
	if running, _ := g.load(); running != 1 {
		t.Fatalf("a query past the free budget should hold a slot, %d held", running)
	}
	// Once it holds one, more blocks cost nothing more.
	if err := ticket.chargeBlocks(ctx, 1000); err != nil {
		t.Fatalf("charging while holding: %v", err)
	}
	ticket.finish()
	assertGateIdle(t, g)
}

func TestQueryTicketRefusedWhenThePoolIsFull(t *testing.T) {
	g := newQueryGate(1, 1, 1, 10*time.Millisecond)
	hold, err := g.acquire(context.Background(), "other", false)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	defer hold()
	ticket := &queryTicket{gate: g, client: "x"}
	ctx := context.WithValue(context.Background(), queryTicketKey{}, ticket)
	if err := ticket.chargeBlocks(ctx, queryFreeBlockBudget+1); err == nil {
		t.Fatalf("expected the query to be refused")
	} else {
		mustBusy(t, err, queryBusyTimeout)
	}
	if err := ticket.require(ctx); err == nil {
		t.Fatalf("expected require to be refused too")
	}
}

func TestNilQueryTicketIsFreeButStillHonoursTheContext(t *testing.T) {
	var ticket *queryTicket
	if err := ticket.chargeBlocks(context.Background(), 1_000_000); err != nil {
		t.Fatalf("a nil ticket is never charged: %v", err)
	}
	if err := ticket.require(context.Background()); err != nil {
		t.Fatalf("a nil ticket needs no slot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ticket.chargeBlocks(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("a finished context must stop even an unticketed scan, got %v", err)
	}
	ticket.finish()
}

func TestWriteQueryFailureShapes(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{})

	rec := httptest.NewRecorder()
	if !srv.writeQueryFailure(rec, 7, "nhb_getTransaction", &queryBusyError{Reason: queryBusyQueue, RetryAfter: 2500 * time.Millisecond}) {
		t.Fatalf("a busy error must be handled")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("busy: HTTP %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After %q, want 3 (2.5s rounded up)", got)
	}
	var resp RPCResponse
	decodeInto(t, rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeRateLimited {
		t.Fatalf("busy: error %+v, want the rate-limit code %d", resp.Error, codeRateLimited)
	}
	data, _ := resp.Error.Data.(map[string]interface{})
	if data["reason"] != queryBusyQueue || data["retryAfterMs"] != float64(2500) {
		t.Fatalf("busy: data %v", resp.Error.Data)
	}

	rec = httptest.NewRecorder()
	if !srv.writeQueryFailure(rec, 7, "nhb_getTransaction", fmt.Errorf("scan: %w", context.DeadlineExceeded)) {
		t.Fatalf("a deadline must be handled")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("deadline: HTTP %d", rec.Code)
	}
	resp = RPCResponse{}
	decodeInto(t, rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeQueryTimeout {
		t.Fatalf("deadline: error %+v, want code %d", resp.Error, codeQueryTimeout)
	}

	rec = httptest.NewRecorder()
	if !srv.writeQueryFailure(rec, 7, "nhb_getTransaction", context.Canceled) {
		t.Fatalf("a cancellation must be handled")
	}
	if rec.Code != 499 {
		t.Fatalf("cancelled: HTTP %d, want 499", rec.Code)
	}

	if srv.writeQueryFailure(httptest.NewRecorder(), 7, "nhb_getTransaction", errors.New("boom")) {
		t.Fatalf("an unrelated error must be left to the caller")
	}
}
