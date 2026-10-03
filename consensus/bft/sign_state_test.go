package bft

// Unit tests of the double-sign guard itself (sign_state.go): the rules of
// claimVote, the durable record, and how the engine uses it. The behaviour the
// guard exists for -- what a validator actually puts on the wire -- is tested in
// double_sign_test.go.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func newGuardEngine(t *testing.T, opts ...Option) (*Engine, *testValidators) {
	t.Helper()
	tv := newTestValidators(t, 2)
	engine, _, _ := newVoter(t, tv, opts...)
	return engine, tv
}

func TestClaimVoteRules(t *testing.T) {
	engine, _ := newGuardEngine(t)
	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))

	steps := []struct {
		name   string
		vt     VoteType
		hash   []byte
		round  int
		height uint64
		refuse bool
	}{
		{"the first prevote of the round", Prevote, hashA, 3, 5, false},
		{"the same prevote again is harmless", Prevote, hashA, 3, 5, false},
		{"another block in the same slot is refused", Prevote, hashB, 3, 5, true},
		{"nil in a slot that holds a block is refused", Prevote, nil, 3, 5, true},
		{"the precommit of the round is a slot of its own", Precommit, hashB, 3, 5, false},
		{"another block in the precommit slot is refused", Precommit, hashA, 3, 5, true},
		{"an earlier round is refused", Prevote, hashA, 2, 5, true},
		{"an earlier round is refused for a vote type never signed in it", Precommit, hashA, 1, 5, true},
		{"an earlier height is refused at any round", Prevote, hashA, 9, 4, true},
		{"the next round is open", Prevote, nil, 4, 5, false},
		{"nil twice in a slot is harmless", Prevote, nil, 4, 5, false},
		{"a block in a slot that holds nil is refused", Prevote, hashA, 4, 5, true},
		{"the next round closes the rounds before it", Precommit, hashA, 3, 5, true},
		{"the next height is open at any round", Prevote, hashA, 0, 6, false},
		{"the next height closes the height before it", Prevote, hashA, 4, 5, true},
	}
	for _, s := range steps {
		err := engine.claimVote(s.vt, s.hash, s.round, s.height)
		switch {
		case s.refuse && !errors.Is(err, errConflictingVote):
			t.Fatalf("%s: expected the claim to be refused, got %v", s.name, err)
		case !s.refuse && err != nil:
			t.Fatalf("%s: expected the claim to be allowed, got %v", s.name, err)
		}
	}
	if got := engine.signed; got.Height != 6 || got.Round != 0 || got.Prevote == nil || got.Precommit != nil {
		t.Fatalf("the record must be the last (height, round) signed in: %+v", got)
	}
}

func TestClaimVoteRefusesAnUnknownVoteType(t *testing.T) {
	engine, _ := newGuardEngine(t)
	if err := engine.claimVote(VoteType(9), nil, 1, 1); err == nil {
		t.Fatalf("a vote of unknown type must not be signed")
	}
	if !engine.signed.empty() {
		t.Fatalf("a refused claim must leave no record: %+v", engine.signed)
	}
}

// createVote is the only place a vote is signed: a refused claim yields no vote,
// and the refusal is the one callers recognise.
func TestCreateVoteSignsNothingThatConflicts(t *testing.T) {
	engine, tv := newGuardEngine(t)
	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))

	first, err := engine.createVote(Prevote, hashA, 2, 1)
	if err != nil || first == nil {
		t.Fatalf("first vote: %v", err)
	}
	again, err := engine.createVote(Prevote, hashA, 2, 1)
	if err != nil {
		t.Fatalf("the same vote again: %v", err)
	}
	if !bytes.Equal(first.Signature.Signature, again.Signature.Signature) {
		t.Fatalf("the same vote must be signed identically, so repeating it can never be an equivocation")
	}
	vote, err := engine.createVote(Prevote, hashB, 2, 1)
	if vote != nil || !errors.Is(err, errConflictingVote) {
		t.Fatalf("a conflicting vote must be refused, got %+v, %v", vote, err)
	}
	if err := engine.verifySignedVote(first); err != nil || !bytes.Equal(first.Validator, tv.addrs[0]) {
		t.Fatalf("the vote must verify as this validator's: %v", err)
	}
}

// The record survives what clears the "already voted" flags: the reset after a
// failed commit, and a new round's own reset. The reset after a failed commit
// used to make a validator that had prevoted X in the round free to prevote Y in
// it.
func TestTheRecordSurvivesAProposalReset(t *testing.T) {
	engine, tv := newGuardEngine(t)
	blockX := candidateBlock(1, 1, tv.addrs[1], 0x01)
	blockY := candidateBlock(1, 1, tv.addrs[1], 0x02)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 1}
	engine.mu.Unlock()
	engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: blockX, Round: 1, ValidRound: -1}, Proposer: tv.addrs[1]})
	engine.prevote()

	engine.mu.Lock()
	engine.resetProposalStateLocked()
	engine.mu.Unlock()
	if engine.prevoteSent {
		t.Fatalf("test setup: the reset is meant to clear prevoteSent")
	}
	if err := engine.claimVote(Prevote, headerHash(t, blockY), 1, 1); !errors.Is(err, errConflictingVote) {
		t.Fatalf("a vote for another block in the same round must still be refused after the reset, got %v", err)
	}
	if err := engine.claimVote(Prevote, headerHash(t, blockX), 1, 1); err != nil {
		t.Fatalf("the vote already signed may be repeated: %v", err)
	}
}

// Claims for one slot race: exactly one value wins, whatever the schedule.
func TestConflictingClaimsRaceToExactlyOneWinner(t *testing.T) {
	engine, _ := newGuardEngine(t)
	const claimants = 32
	var wg sync.WaitGroup
	results := make([]error, claimants)
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = engine.claimVote(Precommit, sha256Sum([]byte(fmt.Sprintf("block-%d", i))), 7, 2)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, errConflictingVote):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one of %d conflicting claims may succeed, %d did", claimants, winners)
	}
}

func TestSignStateRoundTripAndTolerance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "bft_sign_state.json")
	want := signState{
		Height:    12,
		Round:     3,
		Prevote:   &signedValue{BlockHash: []byte{0xAB, 0xCD}},
		Precommit: &signedValue{}, // a vote for nil is a vote, not the absence of one
	}
	for i := 0; i < 3; i++ {
		if err := writeSignState(path, want); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, err := readSignState(path)
	if err != nil || got == nil {
		t.Fatalf("read: %+v, %v", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip: got %+v, want %+v", *got, want)
	}
	if got.Precommit == nil {
		t.Fatalf("a signed vote for nil must read back as signed")
	}
	// No stray temp files are left beside the record.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected only the record in its directory, got %v (%v)", entries, err)
	}

	missing, err := readSignState(filepath.Join(dir, "absent.json"))
	if err != nil || missing != nil {
		t.Fatalf("a missing record is no record: %+v, %v", missing, err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := readSignState(corrupt); err == nil || st != nil {
		t.Fatalf("a corrupt record must be reported, not silently read as empty: %+v, %v", st, err)
	}
	if err := writeSignState("", want); err != nil {
		t.Fatalf("a blank path is a no-op: %v", err)
	}
	if st, err := readSignState(""); err != nil || st != nil {
		t.Fatalf("a blank path reads as no record: %+v, %v", st, err)
	}
}

// The engine keeps the record of the height it is about to contest, and of any
// later one (the chain database is behind what the validator already signed), and
// drops only a record for a height that has committed. A record it cannot read
// never stops it starting, but it stops it signing.
func TestNewEngineRestoresTheRecordOfTheHeightItContests(t *testing.T) {
	tv := newTestValidators(t, 2)
	newEngine := func(path string) *Engine {
		node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5} // contests height 6
		return NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))
	}
	record := func(height uint64, round int) signState {
		return signState{Height: height, Round: round, Prevote: &signedValue{BlockHash: []byte{1}}}
	}
	for _, tc := range []struct {
		name     string
		height   uint64
		restored bool
	}{
		{"the height being contested", 6, true},
		{"a much later height (the chain database is far behind the record)", 196000, true},
		{"the next height", 7, true},
		{"a height already passed", 5, false},
		{"a much older height", 1, false},
	} {
		path := filepath.Join(t.TempDir(), "bft_sign_state.json")
		if err := writeSignState(path, record(tc.height, 4)); err != nil {
			t.Fatal(err)
		}
		engine := newEngine(path)
		if got := !engine.signed.empty(); got != tc.restored {
			t.Fatalf("%s: restored = %v, want %v", tc.name, got, tc.restored)
		}
		if tc.restored && (engine.signed.Height != tc.height || engine.signed.Round != 4) {
			t.Fatalf("%s: restored %+v", tc.name, engine.signed)
		}
	}

	corrupt := filepath.Join(t.TempDir(), "bft_sign_state.json")
	if err := os.WriteFile(corrupt, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if engine := newEngine(corrupt); engine == nil || !engine.signed.empty() {
		t.Fatalf("an unreadable record must not stop the engine or leave a half-restored one")
	}
	if engine := newEngine(""); engine == nil || !engine.signed.empty() {
		t.Fatalf("no path, no record")
	}
}

// A restart starts at the first round of the height; the record says the
// validator voted in a later one. The engine skips to the round after it, and
// leaves every other case alone.
func TestStartNewRoundSkipsRoundsAlreadySignedIn(t *testing.T) {
	engine, _ := newGuardEngine(t)
	round := func() int {
		engine.mu.RLock()
		defer engine.mu.RUnlock()
		return engine.currentState.Round
	}

	engine.startNewRound()
	engine.startNewRound()
	if round() != 2 {
		t.Fatalf("with nothing signed, each round follows the last: got %d", round())
	}

	engine.signMu.Lock()
	engine.signed = signState{Height: 1, Round: 7, Prevote: &signedValue{BlockHash: []byte{1}}}
	engine.signMu.Unlock()
	engine.startNewRound()
	if round() != 8 {
		t.Fatalf("the round must skip past the last one signed in (7): got %d", round())
	}
	engine.startNewRound()
	if round() != 9 {
		t.Fatalf("once past it, rounds follow one another again: got %d", round())
	}

	// A record for another height says nothing about this one.
	engine.signMu.Lock()
	engine.signed = signState{Height: 4, Round: 30, Prevote: &signedValue{BlockHash: []byte{1}}}
	engine.signMu.Unlock()
	engine.startNewRound()
	if round() != 10 {
		t.Fatalf("a record for another height must not move the round: got %d", round())
	}
}

// A vote whose record cannot be written is not signed: a signature that no
// durable record backs is what a restart would forget. Nothing is remembered in
// memory either, so the vote can be claimed again when the disk works, and the
// callers treat the failure as an attempt to retry rather than a conflict.
func TestAVoteIsNotSignedWhenItsRecordCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "bft_sign_state.json")
	tv := newTestValidators(t, 2)
	engine, _, br := newVoter(t, tv, WithSignStatePath(path))
	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))

	err := engine.claimVote(Prevote, hashA, 1, 1)
	if !errors.Is(err, errSignStateNotDurable) || errors.Is(err, errConflictingVote) {
		t.Fatalf("a vote whose record cannot be written must be refused as such, got %v", err)
	}
	if !engine.signed.empty() {
		t.Fatalf("a refused claim must leave no record in memory: %+v", engine.signed)
	}
	if vote, err := engine.createVote(Prevote, hashA, 1, 1); vote != nil || err == nil {
		t.Fatalf("no vote may be signed without its record, got %+v, %v", vote, err)
	}

	// Through the engine: nothing reaches the wire, and the round may try again.
	block := candidateBlock(1, 1, tv.addrs[1], 0x01)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 1}
	engine.mu.Unlock()
	engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: 1, ValidRound: -1}, Proposer: tv.addrs[1]})
	engine.prevote()
	if votes := br.votes(tv.addrs[0]); len(votes) != 0 {
		t.Fatalf("a vote was broadcast although its record could not be written: %s", describeVotes(votes))
	}
	engine.mu.RLock()
	sent := engine.prevoteSent
	engine.mu.RUnlock()
	if sent {
		t.Fatalf("a failed attempt must leave the prevote open to a retry")
	}

	// The disk recovers: the same slot can now be filled, with any value, because
	// nothing was ever signed in it; and from then on the slot is held.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := engine.claimVote(Prevote, hashB, 1, 1); err != nil {
		t.Fatalf("once the record can be written the vote may be signed: %v", err)
	}
	if err := engine.claimVote(Prevote, hashA, 1, 1); !errors.Is(err, errConflictingVote) {
		t.Fatalf("the slot must now be held, got %v", err)
	}
	if st, err := readSignState(path); err != nil || st == nil || st.Prevote == nil || !bytes.Equal(st.Prevote.BlockHash, hashB) {
		t.Fatalf("the record must hold the vote that was signed: %+v, %v", st, err)
	}
}

// The chain database is not synced after every block, so after a host crash it can
// be behind a record that did reach the disk. The restarted validator must not
// vote in the heights the record covers, and must not drop the record because the
// chain is behind it: a different vote for a slot it already signed in is a
// slashable pair, and those heights may have been finished by the network.
func TestARecordAheadOfTheChainStillHoldsTheValidatorBack(t *testing.T) {
	const (
		recordHeight = 10
		votedRound   = 1
		missing      = 3 // blocks the chain database lost
	)
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	tv := newTestValidators(t, 2)
	blockX := candidateBlock(recordHeight, votedRound, tv.addrs[1], 0x01)
	blockY := candidateBlock(recordHeight, votedRound, tv.addrs[1], 0x02)
	hashX, hashY := headerHash(t, blockX), headerHash(t, blockY)

	// Before the crash: the chain is at height 9 and the validator prevotes X at
	// height 10, round 1.
	nodeBefore := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: recordHeight - 1}
	before := NewEngine(nodeBefore, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))
	first, err := before.createVote(Prevote, hashX, votedRound, recordHeight)
	if err != nil || first == nil {
		t.Fatalf("the first vote: %v", err)
	}

	// After it: the record is on disk, the chain database is three blocks short.
	nodeAfter := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: recordHeight - 1 - missing}
	br := &captureBroadcaster{}
	restarted := NewEngine(nodeAfter, tv.keys[0], br, WithSignStatePath(path))
	if restarted.signed.empty() || restarted.signed.Height != recordHeight || restarted.signed.Round != votedRound {
		t.Fatalf("the record ahead of the chain must be kept, the engine holds %+v", restarted.signed)
	}

	// While the chain is behind the record nothing is signed, at any round of the
	// heights it has yet to reach.
	for _, height := range []uint64{recordHeight - missing, recordHeight - 1} {
		for _, round := range []int{0, 1, 5} {
			if vote, err := restarted.createVote(Prevote, hashY, round, height); vote != nil || !errors.Is(err, errConflictingVote) {
				t.Fatalf("a validator behind its own record signed for height %d round %d: %+v, %v", height, round, vote, err)
			}
		}
	}

	// The chain catches up (block sync) and the engine moves to the height of the
	// record: the slot it already signed in is still held ...
	nodeAfter.mu.Lock()
	nodeAfter.height = recordHeight - 1
	nodeAfter.mu.Unlock()
	restarted.startNewRound()
	restarted.mu.RLock()
	height, round := restarted.currentState.Height, restarted.currentState.Round
	restarted.mu.RUnlock()
	if height != recordHeight {
		t.Fatalf("the engine must be at height %d after the chain caught up, it is at %d", recordHeight, height)
	}
	if vote, err := restarted.createVote(Prevote, hashY, votedRound, recordHeight); vote != nil || !errors.Is(err, errConflictingVote) {
		t.Fatalf("a different vote for the slot of the record was signed: %+v, %v", vote, err)
	}
	// ... the engine has gone straight to the first round after it ...
	if round <= votedRound {
		t.Fatalf("the engine must start a round after the one it signed in, it is in round %d", round)
	}
	// ... and there it votes normally: the record holds the validator back only
	// until the chain reaches it.
	later, err := restarted.createVote(Prevote, hashY, round, recordHeight)
	if err != nil || later == nil {
		t.Fatalf("once the chain has reached the record the validator must vote again: %v", err)
	}
	requireNoSlashableProof(t, []*SignedVote{first, later}, tv.addrs[0])
	if votes := br.votes(tv.addrs[0]); len(votes) != 0 {
		t.Fatalf("nothing was meant to be broadcast: %s", describeVotes(votes))
	}
}

// A record that is present but cannot be read says nothing about what the
// validator signed. It must not be taken for no record: the engine starts but
// signs nothing, however the file came to be unreadable, until the operator
// repairs it or moves it aside on purpose.
func TestAnUnreadableRecordStopsTheValidatorSigning(t *testing.T) {
	tv := newTestValidators(t, 2)
	hash := sha256Sum([]byte("a"))
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"an empty file", []byte{}},
		{"a truncated record", []byte(`{"height":1,"round":2,"prevote":{"blockHa`)},
		{"garbage", []byte("garbage")},
	} {
		path := filepath.Join(t.TempDir(), "bft_sign_state.json")
		if err := os.WriteFile(path, tc.content, 0o600); err != nil {
			t.Fatal(err)
		}
		engine, _, br := newVoter(t, tv, WithSignStatePath(path))
		if engine == nil {
			t.Fatalf("%s: an unreadable record must not stop the engine from starting", tc.name)
		}

		if vote, err := engine.createVote(Prevote, hash, 1, 1); vote != nil || !errors.Is(err, errSignStateUnreadable) {
			t.Fatalf("%s: nothing may be signed on an unreadable record, got %+v, %v", tc.name, vote, err)
		}
		if vote, err := engine.createVote(Precommit, nil, 3, 4); vote != nil || !errors.Is(err, errSignStateUnreadable) {
			t.Fatalf("%s: nothing may be signed at any other slot either, got %+v, %v", tc.name, vote, err)
		}
		block := candidateBlock(1, 1, tv.addrs[1], 0x01)
		engine.mu.Lock()
		engine.currentState = State{Height: 1, Round: 1}
		engine.mu.Unlock()
		engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: 1, ValidRound: -1}, Proposer: tv.addrs[1]})
		engine.prevote()
		if votes := br.votes(tv.addrs[0]); len(votes) != 0 {
			t.Fatalf("%s: a vote was broadcast on an unreadable record: %s", tc.name, describeVotes(votes))
		}
		// The file is left as it was, for the operator to look at.
		if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, tc.content) {
			t.Fatalf("%s: the unreadable record must not be overwritten: %q, %v", tc.name, raw, err)
		}

		// Moved aside on purpose, and the node restarted: the validator signs again.
		if err := os.Rename(path, path+".moved"); err != nil {
			t.Fatal(err)
		}
		restarted, _, _ := newVoter(t, tv, WithSignStatePath(path))
		if vote, err := restarted.createVote(Prevote, hash, 1, 1); err != nil || vote == nil {
			t.Fatalf("%s: with the record moved aside the validator must sign again: %v", tc.name, err)
		}
	}

	// No file at all is a validator that has not signed anything: it signs.
	fresh, _, _ := newVoter(t, tv, WithSignStatePath(filepath.Join(t.TempDir(), "bft_sign_state.json")))
	if vote, err := fresh.createVote(Prevote, hash, 1, 1); err != nil || vote == nil {
		t.Fatalf("a missing record is no record, the validator must sign: %v", err)
	}
}

func TestWithSignStatePathToleratesANilEngine(t *testing.T) {
	WithSignStatePath("x")(nil) // must not panic
}
