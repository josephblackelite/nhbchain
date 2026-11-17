package bft

// Status is what the liveness report reads from the engine. It takes the engine's
// lock for reading, and commit holds that lock for as long as the node takes to
// commit a block, so a report that called it waited as long as a slow or stuck
// commit did, and a stall was reported only once the stall was over. StatusWithin
// is the same read with a bound on the wait.

import (
	"sync"
	"testing"
	"time"
)

type statusAnswer struct {
	height uint64
	round  int
	locked int
	ok     bool
	took   time.Duration
}

// askStatusWithin calls StatusWithin(wait) on its own goroutine and fails the
// test, after calling unblock so that nothing is left waiting, if it has not
// answered after limit.
func askStatusWithin(t *testing.T, e *Engine, wait, limit time.Duration, unblock func()) statusAnswer {
	t.Helper()
	got := make(chan statusAnswer, 1)
	go func() {
		begin := time.Now()
		height, round, locked, ok := e.StatusWithin(wait)
		got <- statusAnswer{height, round, locked, ok, time.Since(begin)}
	}()
	select {
	case a := <-got:
		return a
	case <-time.After(limit):
		unblock()
		t.Fatalf("StatusWithin(%v) was still waiting for the engine after %v", wait, limit)
		return statusAnswer{}
	}
}

func TestStatusWithinGivesUpWhileACommitHoldsTheEngineAndAnswersAfterIt(t *testing.T) {
	const round = 1
	tv := newTestValidators(t, 3)
	tips := tipsForProposers(t, tv, round, 2, 1)
	node := &gatedCommitNode{
		chainTipNode: chainTipNode{&signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, tips},
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	engine := NewEngine(node, tv.keys[0], &captureBroadcaster{})
	inRound(engine, 1, round)
	decideHeight(t, tv, engine, 1, round)

	committed := make(chan bool, 1)
	go func() { committed <- engine.commit() }()
	<-node.started // commit holds the engine's lock, inside the node's commit

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(node.release) }) }
	t.Cleanup(release)

	const wait = 40 * time.Millisecond
	a := askStatusWithin(t, engine, wait, 10*time.Second, release)
	if a.ok {
		t.Fatalf("StatusWithin answered while the engine's lock was held by a commit")
	}
	if a.took < wait {
		t.Fatalf("StatusWithin gave up after %v, before the %v it was given", a.took, wait)
	}

	release()
	if !<-committed {
		t.Fatalf("test setup: height 1 did not commit")
	}
	a = askStatusWithin(t, engine, time.Second, 10*time.Second, release)
	if !a.ok {
		t.Fatalf("StatusWithin did not answer once the commit had finished")
	}
	if a.height != 2 {
		t.Fatalf("StatusWithin height = %d after committing height 1, want 2", a.height)
	}
	height, gotRound, locked := engine.Status()
	if a.height != height || a.round != gotRound || a.locked != locked {
		t.Fatalf("StatusWithin = (%d, %d, %d), Status = (%d, %d, %d)", a.height, a.round, a.locked, height, gotRound, locked)
	}
}

func TestStatusWithinAnswersAtOnceWhenTheEngineIsFree(t *testing.T) {
	tv := newTestValidators(t, 3)
	engine, _ := newChainEngine(t, tv, nil)
	inRound(engine, 7, 3)

	for _, wait := range []time.Duration{time.Second, 0, -time.Second} {
		a := askStatusWithin(t, engine, wait, 10*time.Second, func() {})
		if !a.ok || a.height != 7 || a.round != 3 {
			t.Fatalf("StatusWithin(%v) = (height %d, round %d, ok %v), want (7, 3, true)", wait, a.height, a.round, a.ok)
		}
	}

	var nilEngine *Engine
	if _, _, locked, ok := nilEngine.StatusWithin(time.Second); !ok || locked != -1 {
		t.Fatalf("a nil engine reports ok=%v locked=%d, want ok=true locked=-1 like Status", ok, locked)
	}
}

// A writer waiting behind a reader holds back every later reader, so the plain
// read lock would wait for the writer, and the writer for the reader.
// StatusWithin must not join that queue: it gives up on its own bound.
func TestStatusWithinDoesNotQueueBehindAWaitingWriter(t *testing.T) {
	tv := newTestValidators(t, 3)
	engine, _ := newChainEngine(t, tv, nil)
	inRound(engine, 1, 1)

	engine.mu.RLock() // a long read
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(engine.mu.RUnlock) }
	t.Cleanup(unlock)

	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		engine.mu.Lock() // waits for the read above
		engine.mu.Unlock()
	}()
	// Give the writer time to queue. It cannot get in first: the read lock is
	// held until unlock is called below.
	time.Sleep(50 * time.Millisecond)

	a := askStatusWithin(t, engine, 40*time.Millisecond, 10*time.Second, unlock)
	if a.ok {
		t.Fatalf("StatusWithin answered past a writer that was waiting for the lock")
	}

	unlock()
	writer.Wait()
	a = askStatusWithin(t, engine, time.Second, 10*time.Second, unlock)
	if !a.ok {
		t.Fatalf("StatusWithin did not answer once the lock was free")
	}
}
