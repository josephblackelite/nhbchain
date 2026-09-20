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

// The engine keeps the record of the height it is about to contest and drops any
// other: an earlier height has committed, and a later one is what a relaunched
// chain leaves behind (trusting it would make the validator refuse every vote
// until the new chain reached that height). A record it cannot read never stops
// it starting.
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
		{"a later height (left behind by a relaunched chain)", 196000, false},
		{"the next height", 7, false},
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

// A record that cannot be written is reported loudly but never stops the vote:
// the in-memory record still protects the running process.
func TestAFailedRecordWriteStillGuardsTheProcess(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, _ := newGuardEngine(t, WithSignStatePath(filepath.Join(blocker, "bft_sign_state.json")))
	hashA, hashB := sha256Sum([]byte("a")), sha256Sum([]byte("b"))

	if err := engine.claimVote(Prevote, hashA, 1, 1); err != nil {
		t.Fatalf("a record that cannot be written must not stop the vote: %v", err)
	}
	if err := engine.claimVote(Prevote, hashB, 1, 1); !errors.Is(err, errConflictingVote) {
		t.Fatalf("the process must still refuse a conflicting vote, got %v", err)
	}
}

func TestWithSignStatePathToleratesANilEngine(t *testing.T) {
	WithSignStatePath("x")(nil) // must not panic
}
