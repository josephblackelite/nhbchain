package bft

// NHB-AUDIT-C2 regression tests.
//
// NHB-AUDIT-C1 (see polc_test.go) made the in-memory Proof-of-Lock-Change
// lock (lockedRound/lockedBlockHash) durable for the life of a running
// process, but a process CRASH (not just a round timeout) still lost it:
// restart re-entered NewEngine with lockedRound reset to -1, so a validator
// that had locked onto a block moments before crashing came back up fully
// unlocked and free to prevote a conflicting block at the same height --
// exactly the hazard lockCompliesLocked exists to prevent, just reached via
// a different code path (process restart instead of a round timeout).
//
// These tests exercise the real production functions (writeLockSnapshot,
// readLockSnapshot, NewEngine's restore step, addVoteIfRelevant's write,
// resetLockStateLocked's clear) directly, the same way polc_test.go does.
import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestLockSnapshotRoundTrip proves a written snapshot reads back exactly,
// and that repeated writes of identical content (no new lock activity) are
// idempotent -- the file's content after N writes of the same value is
// byte-for-byte the same as after 1.
func TestLockSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polc_lock.json")
	snap := lockSnapshot{Height: 7, LockedRound: 2, LockedBlockHash: []byte{0xAB, 0xCD}}

	for i := 0; i < 3; i++ {
		if err := writeLockSnapshot(path, snap); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	got, err := readLockSnapshot(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil {
		t.Fatalf("expected a snapshot, got nil")
	}
	if got.Height != snap.Height || got.LockedRound != snap.LockedRound || !bytes.Equal(got.LockedBlockHash, snap.LockedBlockHash) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, snap)
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file directly: %v", err)
	}
	if err := writeLockSnapshot(path, snap); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file directly (2nd): %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("expected repeated writes of identical content to be idempotent byte-for-byte")
	}
}

// TestReadLockSnapshotMissingOrCorruptIsNeverFatal proves a missing file and
// a corrupt/truncated file both come back as "no snapshot" (nil, nil) rather
// than an error -- startup must never fail, and must never half-apply a
// partial value, just because the snapshot file is absent or unreadable.
func TestReadLockSnapshotMissingOrCorruptIsNeverFatal(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "does_not_exist.json")
	snap, err := readLockSnapshot(missing)
	if err != nil || snap != nil {
		t.Fatalf("missing file: expected (nil, nil), got (%+v, %v)", snap, err)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	snap, err = readLockSnapshot(corrupt)
	if err != nil || snap != nil {
		t.Fatalf("corrupt file: expected (nil, nil), got (%+v, %v)", snap, err)
	}

	if err := writeLockSnapshot("", lockSnapshot{Height: 1, LockedRound: 0}); err != nil {
		t.Fatalf("blank path write should be a no-op, got: %v", err)
	}
	snap, err = readLockSnapshot("")
	if err != nil || snap != nil {
		t.Fatalf("blank path read: expected (nil, nil), got (%+v, %v)", snap, err)
	}
}

// TestEngineRestoresLockFromSnapshotAfterSimulatedCrash reproduces the
// actual hazard: engine A locks onto a block (a real Polka, via
// addVoteIfRelevant, the same as any live round), then a brand-new Engine
// is constructed against the SAME node (still reporting the pre-commit
// height, exactly as it would after a crash with nothing yet durably
// committed) and the same snapshot path -- simulating a process restart
// mid-height. The restored engine must reject a conflicting fresh proposal
// exactly as the pre-crash engine would have.
func TestEngineRestoresLockFromSnapshotAfterSimulatedCrash(t *testing.T) {
	f := newFourValidatorFixture(t)
	snapshotPath := filepath.Join(t.TempDir(), "polc_lock.json")
	node := &trackingNode{validatorSet: f.validator, height: 0}

	engineA := NewEngine(node, f.keys[0], &recordingBroadcaster{}, WithLockSnapshotPath(snapshotPath))

	block := candidateBlock(1, 0, f.addrs[0], 0x11)
	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	engineA.mu.Lock()
	engineA.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0}}
	engineA.mu.Unlock()

	// Three of four validators (>=2/3) prevote the block -- a real Polka,
	// driven through the real addVoteIfRelevant path so the snapshot write
	// this test relies on is the actual production write, not a stand-in.
	for i := 0; i < 3; i++ {
		vote := f.signedPrevote(t, i, blockHash, 0, 1)
		engineA.addVoteIfRelevant(vote)
	}

	engineA.mu.RLock()
	lockedRound, lockedHash := engineA.lockedRound, append([]byte(nil), engineA.lockedBlockHash...)
	engineA.mu.RUnlock()
	if lockedRound != 0 || !bytes.Equal(lockedHash, blockHash) {
		t.Fatalf("expected engineA to lock onto the block at round 0, got round=%d hash=%x", lockedRound, lockedHash)
	}

	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("expected a snapshot file to exist after locking: %v", err)
	}

	// Simulate a crash+restart: a fresh Engine against the same (still
	// pre-commit) node state and the same snapshot path.
	engineB := NewEngine(node, f.keys[1], &recordingBroadcaster{}, WithLockSnapshotPath(snapshotPath))

	engineB.mu.RLock()
	restoredRound, restoredHash := engineB.lockedRound, engineB.lockedBlockHash
	engineB.mu.RUnlock()
	if restoredRound != 0 || !bytes.Equal(restoredHash, blockHash) {
		t.Fatalf("expected restart to restore the lock (round=0, hash=%x), got round=%d hash=%x", blockHash, restoredRound, restoredHash)
	}

	// And the restored lock must actually be enforced: a fresh (non
	// re-proposal) proposal for a DIFFERENT block must fail
	// lockCompliesLocked exactly as it would have pre-crash.
	otherBlock := candidateBlock(1, 1, f.addrs[1], 0x22)
	otherHash, _ := otherBlock.Header.Hash()
	engineB.mu.RLock()
	complies := engineB.lockCompliesLocked(&Proposal{Block: otherBlock, Round: 1, ValidRound: -1}, otherHash, 1)
	engineB.mu.RUnlock()
	if complies {
		t.Fatalf("BUG: restarted engine allowed a fresh proposal to override a restored lock")
	}
}

// TestEngineDoesNotReapplyStaleSnapshotAfterHeightAdvance is the direct
// test for point (c) of the snapshot design: once resetLockStateLocked has
// overwritten the snapshot for the NEW height (whether via commit() or via
// startNewRound()'s resync branch), a later restart must never reapply the
// old height's lock, even though the file on disk briefly described it.
func TestEngineDoesNotReapplyStaleSnapshotAfterHeightAdvance(t *testing.T) {
	f := newFourValidatorFixture(t)
	snapshotPath := filepath.Join(t.TempDir(), "polc_lock.json")

	// A stale snapshot left over from height 5 (this validator had locked
	// at round 2 right before height 5 committed, but a restart never
	// happened until now -- after the node's real chain height already
	// moved to 5).
	staleHash := sha256Sum([]byte("stale-height-5-block"))
	if err := writeLockSnapshot(snapshotPath, lockSnapshot{Height: 5, LockedRound: 2, LockedBlockHash: staleHash}); err != nil {
		t.Fatalf("seed stale snapshot: %v", err)
	}

	node := &trackingNode{validatorSet: f.validator, height: 5}
	engine := NewEngine(node, f.keys[0], &recordingBroadcaster{}, WithLockSnapshotPath(snapshotPath))

	if engine.currentState.Height != 6 {
		t.Fatalf("expected engine to start at height 6, got %d", engine.currentState.Height)
	}
	if engine.lockedRound != -1 || engine.lockedBlockHash != nil {
		t.Fatalf("BUG: stale height-5 snapshot was wrongly reapplied at height 6 (lockedRound=%d, hash=%x)", engine.lockedRound, engine.lockedBlockHash)
	}
}
