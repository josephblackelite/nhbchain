package bft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Durability for the weak-round-jump bound.
//
// admitWeakRoundJumpLocked (round_sync.go) grants at most maxFutureRounds
// distinct rounds per height to the last, no-floor rule of supportedRoundLocked,
// so that a validator under a third of the power cannot drag this one through an
// unbounded number of rounds by repeating its claim. That bound -- weakJumpHeight
// and weakJumpRounds on Engine -- was plain, non-persisted memory: a restart of
// the very validator it protects (a crash, an upgrade, or an operator bouncing a
// node that looks stuck at a wedged height -- exactly the event this whole
// feature exists to survive) reset it to zero while the height itself had not
// changed, so the same claimant could win up to maxFutureRounds more weak jumps
// on every restart, unboundedly: the identical unbounded-drag liveness hole this
// feature exists to close, gated by restart count instead of by message count.
//
// This file makes the bound durable the same way sign_state.go makes the
// double-sign record durable: a small JSON record, written before the grant it
// records takes effect (so a crash between the write and the grant only ever
// costs this validator a jump it could have had, never one it should not), and
// restored on the next start only when it describes the height about to be
// contested. Nothing here is checked against a signature or a quorum -- it is
// this validator's own bookkeeping of its own past decisions, not a claim about
// what anyone else did -- so, exactly like the sign state, a missing or corrupt
// record is never fatal and is treated as no bound spent yet: the worst that
// does is let a weak claimant through for up to maxFutureRounds more rounds than
// intended, the same exposure this engine already accepted for the lifetime of
// one process before this file existed.

// weakJumpSnapshot is the on-disk record of the rounds admitWeakRoundJumpLocked
// has already granted at Height. Rounds holds one entry per round granted; its
// order does not matter (admitWeakRoundJumpLocked only ever tests membership) and
// is sorted before writing only so the file is stable to read by eye.
type weakJumpSnapshot struct {
	Height uint64 `json:"height"`
	Rounds []int  `json:"rounds,omitempty"`
}

// WithWeakJumpSnapshotPath makes the per-height weak-round-jump bound
// (admitWeakRoundJumpLocked) durable at path, so it survives a restart the same
// way the lock snapshot and the sign state do (see the file comment above).
// NewEngine restores it when it describes the height the engine is about to
// contest, and every round admitWeakRoundJumpLocked grants is recorded there
// before it returns true. A blank path (the default when this option is never
// applied) leaves the bound in memory only, matching prior behavior exactly.
func WithWeakJumpSnapshotPath(path string) Option {
	return func(e *Engine) {
		if e == nil {
			return
		}
		e.weakJumpSnapshotPath = path
	}
}

// restoreWeakJumpState loads the durable record, if the engine has a path for
// one, and keeps it only when it is for the height the engine is about to
// contest -- the same rule lock_snapshot.go and sign_state.go use. A record for
// an earlier height describes a height that has since committed and is stale;
// one for a later height cannot come from this chain's own history. Either
// mismatch is discarded exactly like "no record". Problems reading it never stop
// the engine: it is reported loudly and the bound simply starts at zero, no
// worse than every restart before this file existed.
func (e *Engine) restoreWeakJumpState() {
	if e.weakJumpSnapshotPath == "" {
		return
	}
	snap, err := readWeakJumpSnapshot(e.weakJumpSnapshotPath)
	if err != nil {
		fmt.Printf("failed to read weak jump snapshot: %v\n", err)
		return
	}
	if snap == nil || snap.Height != e.currentState.Height || len(snap.Rounds) == 0 {
		return
	}
	rounds := make(map[int]bool, len(snap.Rounds))
	for _, r := range snap.Rounds {
		rounds[r] = true
	}
	e.weakJumpHeight = snap.Height
	e.weakJumpRounds = rounds
	fmt.Printf("weak jump budget: restored %d granted round(s) (of %d allowed) at height %d\n", len(rounds), maxFutureRounds, snap.Height)
}

// writeWeakJumpSnapshotLocked overwrites the durable record with the rounds
// currently granted at weakJumpHeight. Must be called with e.mu held (its only
// caller, admitWeakRoundJumpLocked, already holds it). A write failure is
// logged, never fatal: the in-memory map still bounds this process, the same
// policy the lock snapshot and the sign state use for their own writes.
func (e *Engine) writeWeakJumpSnapshotLocked() {
	if e.weakJumpSnapshotPath == "" {
		return
	}
	rounds := make([]int, 0, len(e.weakJumpRounds))
	for r := range e.weakJumpRounds {
		rounds = append(rounds, r)
	}
	sort.Ints(rounds)
	if err := writeWeakJumpSnapshot(e.weakJumpSnapshotPath, weakJumpSnapshot{Height: e.weakJumpHeight, Rounds: rounds}); err != nil {
		fmt.Printf("failed to persist weak jump snapshot: %v\n", err)
	}
}

// writeWeakJumpSnapshot atomically overwrites path with snap's JSON encoding:
// the content goes to a temp file in the same directory, is synced, and is
// renamed over path, so a reader -- the next start -- sees the whole old record
// or the whole new one, never a partial write (see lock_snapshot.go's
// writeLockSnapshot for why the same-directory rename is what makes that atomic
// on both POSIX and Windows). A blank path is a no-op.
func writeWeakJumpSnapshot(path string, snap weakJumpSnapshot) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("weak jump snapshot: prepare dir: %w", err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("weak jump snapshot: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".bft_weak_jump-*.tmp")
	if err != nil {
		return fmt.Errorf("weak jump snapshot: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("weak jump snapshot: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("weak jump snapshot: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("weak jump snapshot: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("weak jump snapshot: rename: %w", err)
	}
	return nil
}

// readWeakJumpSnapshot loads path if present. It never returns an error for a
// missing or corrupt file -- treated exactly like "no bound spent yet", which is
// always the safe default here (see the file comment above for why losing this
// record is a liveness exposure this engine already accepts for a full process
// lifetime, not a safety one). A hard I/O error reading an existing, otherwise
// healthy file IS returned, since that is worth surfacing rather than silently
// masking.
func readWeakJumpSnapshot(path string) (*weakJumpSnapshot, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("weak jump snapshot: read: %w", err)
	}
	var snap weakJumpSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		// Corrupt content (should not happen given atomic-rename writes, but a
		// snapshot from a future/incompatible version of this file is not
		// impossible) -- discard rather than fail startup.
		return nil, nil
	}
	return &snap, nil
}
