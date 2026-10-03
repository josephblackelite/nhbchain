package bft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Double-sign protection.
//
// Two votes of one validator for the same height, round and vote type but for
// different blocks are an equivocation, and an equivocation is exactly what a
// slashing report proves: anyone who saw both votes can turn them into evidence
// that takes the validator's stake. So an HONEST validator must never sign two
// such votes -- however the network, a byzantine peer or its own restart
// arranges its inputs. Before this guard nothing in the engine remembered what
// had been signed, and three ordinary paths signed a second, different vote in
// a round the validator had already voted in:
//
//   - a second proposal for the round that failed validation drew a prevote for
//     nil, after the validator had prevoted the first proposal's block (one
//     byzantine validator sending a good proposal and then a bad one was enough),
//     and a bad proposal followed by a good one drew a block prevote after nil;
//   - a block that failed to commit drew a prevote for nil after this validator's
//     own prevote and precommit for that block, and the reset that follows cleared
//     the "already voted" flags, so a new proposal in the same round could be
//     prevoted and precommitted again;
//   - a restart forgot every vote, so a proposer that restarted between voting
//     and committing built a different block for the same round and signed it.
//
// The guard sits at the one place that signs a vote (createVote) and holds two
// rules that no caller can bypass:
//
//  1. Within the (height, round) of the last vote this validator signed, a vote
//     type is signed for at most one block hash (or for nil): a request for the
//     same value is repeated harmlessly, a request for a different one is refused.
//  2. Nothing is signed for an earlier round, or an earlier height, than the last
//     vote signed. The engine never goes back, so this only ever refuses a stale
//     caller or a restart that has lost its place.
//
// The record is written to disk BEFORE the vote is signed (write-ahead), so a
// crash can only cost a round -- the restarted validator declines to vote again in
// a round it may already have voted in -- and can never lose a vote that reached
// the network. startNewRound moves a restarted engine past every round it has
// signed in, so the restart does not have to walk dead rounds to get there.
//
// Three conditions would leave a validator with no memory of what it signed, and
// the guard answers each the same way: the validator stays silent rather than
// guess.
//
//   - A vote whose record cannot be written is not signed (errSignStateNotDurable).
//     A signature that no record backs is exactly what a restart would forget.
//   - A record for a height beyond the chain this process starts from is kept, not
//     dropped. The chain database is not synced after every block, so after a host
//     crash it can be behind a record that did reach the disk; that record covers
//     heights the network may have finished, and the validator must not vote in
//     them again. Rule 2 holds it back until block sync brings the chain up to the
//     record. A chain that was relaunched under the same keys must start without
//     the old record (docs/runbooks/migrations.md).
//   - A record that is present but cannot be read or decoded stops the validator
//     signing anything (errSignStateUnreadable) until the file is repaired, or moved
//     aside on purpose, and the node is restarted.
//
// The state is a single small record (the last round signed in), not a history:
// it is enough because rule 2 makes every earlier round unreachable.

// errConflictingVote is returned by createVote when signing the requested vote
// would put a second, different value in a slot this validator has already
// filled, or would sign for a round or height it has already moved past. The
// engine never retries after it: the vote is not sent.
var errConflictingVote = errors.New("refusing to sign a vote that conflicts with one this validator already signed")

// errSignStateNotDurable is returned by createVote when the record of the vote
// could not be written. Nothing is signed and nothing is remembered: callers treat
// it as a failed attempt, so the vote is tried again once the disk works.
var errSignStateNotDurable = errors.New("refusing to sign a vote whose record cannot be written")

// errSignStateUnreadable is returned by createVote for as long as the engine runs
// on a vote record that was present at start but could not be read.
var errSignStateUnreadable = errors.New("refusing to sign a vote: the vote record could not be read at start")

// signedValue is the block hash this validator signed for one vote type. An
// empty hash is a vote for nil.
type signedValue struct {
	BlockHash []byte `json:"blockHash,omitempty"`
}

// signState is what this validator signed in the highest (height, round) it has
// signed anything in.
type signState struct {
	Height    uint64       `json:"height"`
	Round     int          `json:"round"`
	Prevote   *signedValue `json:"prevote,omitempty"`
	Precommit *signedValue `json:"precommit,omitempty"`
}

// empty reports whether nothing has been signed yet.
func (s *signState) empty() bool {
	return s == nil || (s.Prevote == nil && s.Precommit == nil)
}

// WithSignStatePath makes the double-sign record durable at path, so it survives
// a restart (see the comment at the top of this file). NewEngine restores it when
// it describes the height the engine is about to contest or a later one, and
// every vote this validator signs is recorded there before it is signed. A blank
// path (the default) keeps the record in memory only, which still protects a
// running process but not a restart.
func WithSignStatePath(path string) Option {
	return func(e *Engine) {
		if e == nil {
			return
		}
		e.signStatePath = path
	}
}

// claimVote is the double-sign guard: createVote calls it before it signs. It
// returns nil when the vote may be signed -- and records it, durably, before
// returning -- and an error when it may not: one wrapping errConflictingVote for a
// vote that conflicts with the record, errSignStateNotDurable when the record
// cannot be written, errSignStateUnreadable when the record on disk could not be
// read at start.
//
// It takes only signMu, never mu, because createVote is reached with mu held
// (broadcastPrevoteNilLocked) and without it (prevote, precommit).
func (e *Engine) claimVote(t VoteType, blockHash []byte, round int, height uint64) error {
	if t != Prevote && t != Precommit {
		return fmt.Errorf("unknown vote type %d", t)
	}
	e.signMu.Lock()
	defer e.signMu.Unlock()

	if e.signBlocked != nil {
		return e.signBlocked
	}

	cur := e.signed
	next := signState{Height: height, Round: round}
	if !cur.empty() {
		if height < cur.Height || (height == cur.Height && round < cur.Round) {
			return e.refuseVote(t, blockHash, round, height,
				fmt.Sprintf("this validator already signed at height %d round %d", cur.Height, cur.Round))
		}
		if height == cur.Height && round == cur.Round {
			prior := cur.Prevote
			if t == Precommit {
				prior = cur.Precommit
			}
			if prior != nil {
				if bytes.Equal(prior.BlockHash, blockHash) {
					// The same vote again (a retry, a re-broadcast): the signature is
					// deterministic, so this can only ever repeat what was already sent.
					return nil
				}
				return e.refuseVote(t, blockHash, round, height,
					fmt.Sprintf("this validator already signed %s for %s in this round", t, describeHash(prior.BlockHash)))
			}
			// The round already holds the other vote type; keep it.
			next = cur
		}
	}

	value := &signedValue{BlockHash: append([]byte(nil), blockHash...)}
	if t == Prevote {
		next.Prevote = value
	} else {
		next.Precommit = value
	}

	// Write-ahead: the record is durable before the signature exists. When it cannot
	// be written nothing is signed and the in-memory record is left as it was, so
	// the vote can be claimed again once the disk works. A vote that no durable
	// record backs would be forgotten by a restart, and the restarted validator
	// could then sign a different one in the same slot.
	if err := writeSignState(e.signStatePath, next); err != nil {
		slog.Error("SAFETY: could not persist the vote record; the vote was not signed",
			slog.String("event", "sign_state_write_failed"),
			slog.Uint64("height", height),
			slog.Int("round", round),
			slog.String("error", boundedErrorText(err)))
		return fmt.Errorf("%w: %s", errSignStateNotDurable, boundedErrorText(err))
	}
	e.signed = next
	return nil
}

// refuseVote logs a refused vote and returns the error createVote hands back.
func (e *Engine) refuseVote(t VoteType, blockHash []byte, round int, height uint64, why string) error {
	slog.Warn("SAFETY: refused to sign a conflicting vote",
		slog.String("event", "double_sign_refused"),
		slog.Uint64("height", height),
		slog.Int("round", round),
		slog.String("vote", t.String()),
		slog.String("value", describeHash(blockHash)),
		slog.String("reason", why))
	return fmt.Errorf("%w: %s (asked for %s at height %d round %d)", errConflictingVote, why, t, height, round)
}

// describeHash renders a block hash for a log line; the empty hash is nil.
func describeHash(hash []byte) string {
	if len(hash) == 0 {
		return "nil"
	}
	return fmt.Sprintf("block %x", hash)
}

// signedRoundFloor returns the round this validator last signed in at height, if
// it has signed anything at that height. startNewRound never starts a round at or
// below it.
func (e *Engine) signedRoundFloor(height uint64) (int, bool) {
	e.signMu.Lock()
	defer e.signMu.Unlock()
	if e.signed.empty() || e.signed.Height != height {
		return 0, false
	}
	return e.signed.Round, true
}

// restoreSignState loads the durable record, if the engine has a path for one, and
// keeps it when it is for the height the engine is about to contest or a later
// one. A record for an earlier height describes a height that has since committed
// and is stale. One for a later height means the chain this process starts from is
// behind what this validator already signed -- its database lost the newest blocks
// -- so the record is what stops it voting again in heights the network may have
// finished; the engine stays silent until block sync reaches the record's height.
// (A chain relaunched under the same keys must not carry the old record over, see
// docs/runbooks/migrations.md.) A record that is present but cannot be read does
// not stop the engine from starting, but it stops it signing: see claimVote.
func (e *Engine) restoreSignState() {
	if e.signStatePath == "" {
		return
	}
	st, err := readSignState(e.signStatePath)
	if err != nil {
		slog.Error("SAFETY: the vote record could not be read; this validator will not sign any vote until the file is repaired or moved aside and the node is restarted",
			slog.String("event", "sign_state_read_failed"),
			slog.String("path", e.signStatePath),
			slog.String("error", boundedErrorText(err)))
		e.signMu.Lock()
		e.signBlocked = fmt.Errorf("%w (%s)", errSignStateUnreadable, e.signStatePath)
		e.signMu.Unlock()
		return
	}
	if st.empty() || st.Height < e.currentState.Height {
		return
	}
	if st.Height > e.currentState.Height {
		slog.Error("SAFETY: the vote record is for a height beyond the chain; this validator will not vote until its chain has reached that height",
			slog.String("event", "sign_state_ahead_of_chain"),
			slog.String("path", e.signStatePath),
			slog.Uint64("record_height", st.Height),
			slog.Uint64("chain_height", e.currentState.Height-1))
	}
	e.signMu.Lock()
	e.signed = *st
	e.signMu.Unlock()
	fmt.Printf("vote sign state: restored the record of height %d round %d; this validator will not sign again in that round or before it\n", st.Height, st.Round)
}

// writeSignState atomically replaces path with the record: the content goes to a
// temp file in the same directory, is synced, and is renamed over path, so a
// reader -- the next start -- sees the whole old record or the whole new one. The
// directory is synced afterwards (best effort: some platforms cannot) so the
// rename itself survives a power cut. A blank path is a no-op.
func writeSignState(path string, st signState) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("sign state: marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sign state: prepare dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".bft_sign_state-*.tmp")
	if err != nil {
		return fmt.Errorf("sign state: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sign state: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sign state: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("sign state: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("sign state: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// readSignState loads path. A missing file is no record (nil, nil). A file that
// cannot be read or decoded is an error, so the caller can say so loudly rather
// than start with no memory of what it signed without anyone noticing.
func readSignState(path string) (*signState, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sign state: read: %w", err)
	}
	var st signState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("sign state: decode: %w", err)
	}
	return &st, nil
}
