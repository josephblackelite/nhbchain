package bft

// TestRestoredLockWithPolkaProofRecoversAndCommits is the permanent
// regression test for a gap that once existed in NHB-AUDIT-C2's snapshot
// design (see lock_snapshot.go and the Engine struct's lockedBlockHash/
// polkaHistory doc comments): an earlier version of writeLockSnapshot
// durably persisted only lockedRound/lockedBlockHash (the PROHIBITION --
// "never again prevote a block that conflicts with this one") but never
// persisted polkaHistory/validBlock (the PERMISSION -- the actual signed
// votes needed to satisfy that prohibition via a future compliant
// re-proposal; see propose()'s revalidProof lookup).
//
// Under that earlier design, if EVERY validator that observed the polka
// (and was therefore locked) crashed and restarted before the height
// reached a precommit quorum -- a coordinated restart, a shared crash bug,
// or a crash-loop hitting the whole validator set -- not one of them
// retained the votes needed to reconstruct a ValidRoundProof for the value
// they were all now durably locked to. Every future proposer, on every
// future round, could then only ever propose a FRESH mempool block
// (propose() falls back to CreateBlock whenever validBlock is nil), which
// conflicted with everyone's restored lock and was rejected by
// lockCompliesLocked -- including by the proposer's own prevote() rejecting
// its own proposal. No proposal could ever again satisfy the lock, so the
// height could never commit: a permanent liveness deadlock, self-inflicted
// by the very persistence layer meant to fix a fork, and the same category
// of incident (two validators that can never again satisfy each other's
// lock) that took production down the first time this system was touched
// -- see polc_test.go's file comment.
//
// The fix extends the persisted snapshot to ALSO carry the locked block
// and the signed prevotes that constituted its Polka (lock_snapshot.go's
// LockedBlock/PolkaVotes fields), and NewEngine now restores all of it --
// after independently re-verifying it, never merely trusting the file --
// into validBlock/validRound/polkaHistory[lockedRound], exactly the fields
// propose() already reads. This test drives two REAL Engine instances over
// REAL network messages and REAL timers (the same shape as
// TestTwoValidatorNetworkConvergesUnderAsymmetricDelay in
// polc_e2e_test.go), simulates both processes crashing right after locking
// (discarding the pre-crash Engine objects -- and with them the in-memory
// validBlock/polkaHistory -- while leaving their on-disk snapshots and
// their unchanged underlying chain height exactly as a real crash would),
// and asserts the successor engines CAN still commit height 1. If the gap
// described above were ever reintroduced, this assertion would fail (or
// the height would never reach 1 within the bound below), which is the
// proof this regression can never reoccur silently.
import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoredLockWithPolkaProofRecoversAndCommits(t *testing.T) {
	f := newTwoValidatorFixture(t)

	nodeA := &simNode{validatorSet: f.validator, self: f.addrs[0]}
	nodeB := &simNode{validatorSet: f.validator, self: f.addrs[1]}

	pathA := filepath.Join(t.TempDir(), "a.json")
	pathB := filepath.Join(t.TempDir(), "b.json")

	// --- Pre-crash: both validators independently observe a real polka for
	// block X at height 1, round 0, and lock onto it, driven through the
	// real addVoteIfRelevant path (the same production write path exercised
	// by TestEngineRestoresLockFromSnapshotAfterSimulatedCrash) so the
	// on-disk snapshots this test relies on are the actual production
	// writes, not a stand-in. Deliberately never precommitted/committed --
	// this is the crash window between "locked" and "this height finalized".
	block := candidateBlock(1, 0, f.addrs[0], 0x11)
	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}

	preA := NewEngine(nodeA, f.keys[0], &recordingBroadcaster{}, WithLockSnapshotPath(pathA))
	preB := NewEngine(nodeB, f.keys[1], &recordingBroadcaster{}, WithLockSnapshotPath(pathB))
	for _, e := range []*Engine{preA, preB} {
		e.mu.Lock()
		e.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0}}
		e.mu.Unlock()
	}
	for _, e := range []*Engine{preA, preB} {
		for i := 0; i < 2; i++ {
			e.addVoteIfRelevant(f.signedPrevote(t, i, blockHash, 0, 1))
		}
	}

	for name, e := range map[string]*Engine{"A": preA, "B": preB} {
		e.mu.RLock()
		lr, lh := e.lockedRound, e.lockedBlockHashLocked()
		e.mu.RUnlock()
		if lr != 0 || string(lh) != string(blockHash) {
			t.Fatalf("pre-crash engine %s did not lock as expected: round=%d hash=%x", name, lr, lh)
		}
	}
	if nodeA.GetHeight() != 0 || nodeB.GetHeight() != 0 {
		t.Fatalf("test setup bug: expected neither validator to have committed before the simulated crash")
	}

	// --- Simulated crash + restart of BOTH processes. The chains
	// (nodeA/nodeB) are untouched -- nothing ever committed -- but the
	// pre-crash Engine objects, and the validBlock/polkaHistory they held
	// only in memory, are gone. Fresh Engines are built against the SAME
	// underlying nodes and the SAME snapshot paths, exactly like a real
	// restart of the same validator process against its own disk state.
	fastTimeouts := TimeoutConfig{
		Proposal:  20 * time.Millisecond,
		Prevote:   20 * time.Millisecond,
		Precommit: 20 * time.Millisecond,
		Commit:    20 * time.Millisecond,
	}
	brA := &simBroadcaster{}
	brB := &simBroadcaster{}
	engineA := NewEngine(nodeA, f.keys[0], brA, WithLockSnapshotPath(pathA), WithTimeouts(fastTimeouts))
	engineB := NewEngine(nodeB, f.keys[1], brB, WithLockSnapshotPath(pathB), WithTimeouts(fastTimeouts))
	brA.peer = engineB
	brB.peer = engineA

	// Confirm the restore actually happened, and -- the crux of this test's
	// premise -- that each restarted engine ALSO recovered the polka proof
	// needed to satisfy its own restored lock, not just the lock itself.
	for name, e := range map[string]*Engine{"A": engineA, "B": engineB} {
		e.mu.RLock()
		lr, lh, vb, vr := e.lockedRound, e.lockedBlockHashLocked(), e.validBlock, e.validRound
		proof := e.polkaHistory[lr].votes
		e.mu.RUnlock()
		if lr != 0 || string(lh) != string(blockHash) {
			t.Fatalf("restarted engine %s did not restore the pre-crash lock: round=%d hash=%x", name, lr, lh)
		}
		if vb == nil {
			t.Fatalf("BUG: restarted engine %s did not recover a validBlock across restart -- it can never satisfy its own restored lock again", name)
		}
		gotHash, hashErr := vb.Header.Hash()
		if hashErr != nil || !bytes.Equal(gotHash, blockHash) {
			t.Fatalf("restarted engine %s recovered the wrong validBlock (hash=%x, err=%v), want %x", name, gotHash, hashErr, blockHash)
		}
		if vr != 0 {
			t.Fatalf("restarted engine %s recovered validRound=%d, want 0", name, vr)
		}
		if len(proof) < 2 {
			t.Fatalf("BUG: restarted engine %s did not recover the polka proof (got %d signed votes, want >= 2) -- it can never prove its restored lock's value to a peer again", name, len(proof))
		}
	}

	// Run both engines' round loops concurrently, exactly like a real
	// two-validator network, bounded so a genuine deadlock fails fast
	// instead of hanging CI.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 60; i++ {
			engineA.runRound()
			if nodeA.GetHeight() >= 1 {
				return
			}
		}
	}()
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		for i := 0; i < 60; i++ {
			engineB.runRound()
			if nodeB.GetHeight() >= 1 {
				return
			}
		}
	}()

	timeout := time.After(5 * time.Second)
	for done != nil || doneB != nil {
		select {
		case <-done:
			done = nil
		case <-doneB:
			doneB = nil
		case <-timeout:
			done, doneB = nil, nil
		}
	}

	if nodeA.GetHeight() < 1 || nodeB.GetHeight() < 1 {
		t.Fatalf("LIVENESS REGRESSION: height 1 never committed after restart (A=%d, B=%d) -- every validator restored a lock on round 0's block via the persisted snapshot, and should also have recovered the polkaHistory proof needed to re-propose a value that satisfies that lock, but the height still failed to commit", nodeA.GetHeight(), nodeB.GetHeight())
	}

	hashesA := nodeA.committedHashes(t)
	hashesB := nodeB.committedHashes(t)
	if len(hashesA) == 0 || len(hashesB) == 0 {
		t.Fatalf("expected both validators to have at least one committed block")
	}
	if !bytes.Equal(hashesA[0], hashesB[0]) {
		t.Fatalf("SAFETY REGRESSION: validators committed DIFFERENT blocks at height 1 (A=%x B=%x) -- a real fork", hashesA[0], hashesB[0])
	}
	if !bytes.Equal(hashesA[0], blockHash) {
		t.Fatalf("expected the committed block to be the pre-crash locked value X (%x), got %x -- a compliant restart must re-propose the locked value, never a fresh one", blockHash, hashesA[0])
	}
}
