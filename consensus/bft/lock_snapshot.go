package bft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"nhbchain/core/types"
)

// lockSnapshot is the on-disk representation of this validator's
// Proof-of-Lock-Change safety state for a single height.
//
// LockedRound/LockedBlockHash are the PROHIBITION half of Proof-of-Lock-
// Change ("never again prevote a block that conflicts with this one").
// This half alone is enough for lockCompliesLocked to re-enforce safety
// after a crash+restart, and is always restored whenever present.
//
// LockedBlock/PolkaVotes are the PERMISSION half: the actual locked block,
// byte-for-byte (via the same JSON encoding this engine already puts on
// the wire for every proposal/block message -- see p2p.NewBlockMessage),
// and the real signed prevotes that constituted the Polka behind it --
// exactly what propose() looks up via e.polkaHistory[e.validRound] to
// attach as ValidRoundProof on a re-proposal (see Proposal.ValidRoundProof
// and Engine.verifyPolkaProofLocked). An earlier version of this file
// persisted only the prohibition and treated the permission as a
// liveness-only optimization safe to lose on restart. That was wrong: if
// EVERY validator that ever held the permission also crashed (the real
// production topology has exactly two validators, so "every validator
// that observed the polka" and "every validator" can be the same set),
// restoring the prohibition with no path back to the permission means no
// validator can ever again propose a value that satisfies its own
// restored lock -- including the proposer rejecting its own proposal --
// a permanent liveness deadlock. See
// lock_snapshot_deadlock_test.go's TestRestoredLockWithPolkaProofRecoversAndCommits
// for the regression test.
//
// LockedBlock/PolkaVotes are optional and re-verified independently on
// restore before being trusted (LockedBlock's hash must equal
// LockedBlockHash, and PolkaVotes must cryptographically verify as a real
// >=2/3 Polka for this exact height/round/block -- see NewEngine): a
// corrupt or partial file must never let a restart fabricate a lock, or a
// proof of one, that never genuinely happened. A snapshot missing either
// field (e.g. one written before this change, or one where verification
// fails) still restores the prohibition alone, exactly like before this
// permission-carrying data existed.
type lockSnapshot struct {
	Height          uint64        `json:"height"`
	LockedRound     int           `json:"lockedRound"`
	LockedBlockHash []byte        `json:"lockedBlockHash,omitempty"`
	LockedBlock     *types.Block  `json:"lockedBlock,omitempty"`
	PolkaVotes      []*SignedVote `json:"polkaVotes,omitempty"`
}

// writeLockSnapshot atomically overwrites path with snap's JSON encoding.
// A no-op if path is empty (snapshotting is opt-in via WithLockSnapshotPath).
//
// Crash safety comes from writing the full content to a temp file in the
// SAME directory as path, syncing it, and only then rename(2)-ing it over
// path. On POSIX and on Windows (which the Go runtime implements via
// MoveFileEx with MOVEFILE_REPLACE_EXISTING for os.Rename), a rename that
// replaces an existing file is a single filesystem metadata operation: a
// reader opening path at any instant either sees the complete old file or
// the complete new one, never a partial write, PROVIDED the temp file lives
// on the same volume as path -- rename across volumes is not atomic (on
// Windows it can silently degrade to copy+delete; on Linux it fails with
// EXDEV) and this function never risks that because os.CreateTemp is asked
// for filepath.Dir(path). What this does NOT protect against is power loss
// (not process crash): unless the directory entry update itself is fsync'd,
// some filesystems can lose a very recent rename across a hard power cut.
// That is out of scope here -- the existing p2p/peerstore.go JSON-file
// pattern this mirrors makes the same tradeoff -- so this guarantees
// crash/restart safety, not power-loss durability.
func writeLockSnapshot(path string, snap lockSnapshot) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("lock snapshot: prepare dir: %w", err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("lock snapshot: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".polc_lock-*.tmp")
	if err != nil {
		return fmt.Errorf("lock snapshot: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("lock snapshot: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("lock snapshot: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("lock snapshot: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("lock snapshot: rename: %w", err)
	}
	return nil
}

// readLockSnapshot loads path if present. It never returns an error for a
// missing or corrupt file -- a missing/corrupt snapshot is treated exactly
// like "no lock was ever recorded", which is always the safe default (an
// unlocked validator can still only ever prevote what >=2/3 power actually
// prevotes for; it just loses the extra safety margin this feature adds).
// A hard I/O error reading an existing, otherwise-healthy file IS returned,
// since that is worth surfacing rather than silently masking.
func readLockSnapshot(path string) (*lockSnapshot, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock snapshot: read: %w", err)
	}
	var snap lockSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		// Corrupt content (should not happen given atomic-rename writes,
		// but a snapshot from a future/incompatible version of this file
		// is not impossible) -- discard rather than fail startup.
		return nil, nil
	}
	return &snap, nil
}
