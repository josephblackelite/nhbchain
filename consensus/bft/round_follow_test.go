package bft

// Following a validator that is ahead when no quorum can be made without it.
//
// A validator moves to a later round only on the evidence of the voting power seen there
// (supportedRoundLocked): more than a third of it, and a quorum with this validator's own. In a
// set in which every validator that runs is needed for a quorum -- three of four, five of seven --
// that is all of the others, so a validator that a restart has left rounds ahead of the ones that
// are level with each other was never followed: it holds less than a third of the power. Its
// rounds last as long as theirs, so it stayed ahead, and nothing could commit. These tests hold
// the rule that ends it: a validator that is ahead is followed when this validator and the ones
// level with it (seen in its own round, or shortly before it) are not a quorum, and they and the
// ones ahead are, and never otherwise.

import (
	"bytes"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
)

// roundClaimOf is one validator's claim: validator index and round.
type roundClaimOf struct{ validator, round int }

// supportedRound reads what supportedRoundLocked says for the engine.
func supportedRound(e *Engine) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.supportedRoundLocked()
}

// claimed makes the engine see the claims, made at height.
func claimed(e *Engine, tv *testValidators, height uint64, claims ...roundClaimOf) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range claims {
		e.recordRoundClaimLocked(tv.addrs[c.validator], height, c.round)
	}
}

func equalPowers(n int) []int64 {
	powers := make([]int64, n)
	for i := range powers {
		powers[i] = 1
	}
	return powers
}

func TestAValidatorFollowsOneThatIsAheadOnlyWhenNoQuorumCanBeMadeWithoutIt(t *testing.T) {
	const own = 10
	type followCase struct {
		name    string
		powers  []int64
		want    int // 0 for none
		claims  []roundClaimOf
		earlier []roundClaimOf // claims made at the height before
	}
	c := func(name string, powers []int64, want int, claims ...roundClaimOf) followCase {
		return followCase{name: name, powers: powers, want: want, claims: claims}
	}
	four, seven := equalPowers(4), equalPowers(7)
	earlier := c("a level validator seen at an earlier height is not there", four, 14, roundClaimOf{3, own}, roundClaimOf{2, 14})
	earlier.earlier = []roundClaimOf{{1, own}}
	cases := []followCase{
		c("three of four are needed: the one alone ahead is followed", four, 14,
			roundClaimOf{1, own}, roundClaimOf{2, 14}),
		c("the same with the other needed validator not heard from yet", four, 0,
			roundClaimOf{2, 14}),
		c("the same with the fourth validator level: a quorum needs no one ahead", four, 0,
			roundClaimOf{1, own}, roundClaimOf{3, own}, roundClaimOf{2, 14}),
		c("the fourth validator seen at the edge of the window is still there", four, 0,
			roundClaimOf{1, own}, roundClaimOf{3, own - maxFutureRounds}, roundClaimOf{2, 14}),
		c("the fourth validator seen before the window is gone", four, 14,
			roundClaimOf{1, own}, roundClaimOf{3, own - maxFutureRounds - 1}, roundClaimOf{2, 14}),
		c("a validator behind this one counts as level, not as ahead", four, 14,
			roundClaimOf{1, own - 2}, roundClaimOf{2, 14}),
		c("nobody ahead: nothing to follow", four, 0,
			roundClaimOf{1, own}, roundClaimOf{2, own}),
		c("five of seven are needed: the one alone ahead is followed", seven, 13,
			roundClaimOf{1, own}, roundClaimOf{2, own}, roundClaimOf{3, own}, roundClaimOf{4, 13}),
		c("five of seven with one of them not heard from: not yet", seven, 0,
			roundClaimOf{1, own}, roundClaimOf{2, own}, roundClaimOf{4, 13}),
		c("five of seven with a sixth level: it needs no one ahead", seven, 0,
			roundClaimOf{1, own}, roundClaimOf{2, own}, roundClaimOf{3, own}, roundClaimOf{5, own}, roundClaimOf{4, 13}),
		c("of several ahead, the highest round a quorum can be made in", seven, 11,
			roundClaimOf{1, own}, roundClaimOf{2, 11}, roundClaimOf{3, 12}, roundClaimOf{4, 13}),
		c("more than a third ahead is followed as it always was", four, 14,
			roundClaimOf{1, 14}, roundClaimOf{2, 14}),
		c("weights: this validator holds half, one other completes a quorum", []int64{2, 1, 1}, 14,
			roundClaimOf{1, 14}),
		c("weights: the level validator with the weight of a quorum needs no one ahead", []int64{2, 1, 1}, 0,
			roundClaimOf{1, 14}, roundClaimOf{2, own}),
		earlier,
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newWeightedValidators(t, tc.powers...)
			engine, _, _ := engineAt(t, tv, own)
			claimed(engine, tv, 0, tc.earlier...)
			claimed(engine, tv, 1, tc.claims...)
			got, ok := supportedRound(engine)
			if tc.want == 0 && ok {
				t.Fatalf("this validator, in round %d, was told to move to round %d; it should stay", own, got)
			}
			if tc.want != 0 && (!ok || got != tc.want) {
				t.Fatalf("this validator, in round %d, is told (round %d, %v); want a move to round %d", own, got, ok, tc.want)
			}
		})
	}
}

// The last rule of supportedRoundLocked -- the one that follows a validator ahead
// without the more-than-a-third bar -- holds no floor on how little power that
// validator needs: one holding a quarter of the power, alone, is exactly as
// convincing to it as the one real validator a restarted set is legitimately
// missing. What bounds it instead is how many distinct rounds it will move this
// validator through at one height. Here a lone validator (index 1), a quarter of
// the power, claims one round further each time this validator's own round
// catches up to it, with a second (index 2), also level, kept fresh alongside it
// so the shortfall the rule acts on (own and one other short of the three of four
// a quorum needs) never goes away on its own.
func TestALoneValidatorUnderAThirdCannotDragThisEngineThroughUnboundedRounds(t *testing.T) {
	tv := newWeightedValidators(t, 1, 1, 1, 1)
	engine, _, _ := engineAt(t, tv, 1)

	moves := 0
	for i := 0; i < 4*maxFutureRounds; i++ {
		round := roundOf(engine)
		claimed(engine, tv, 1, roundClaimOf{2, round})     // the level peer: always freshly at this validator's own round
		claimed(engine, tv, 1, roundClaimOf{1, round + 1}) // the lone claimant: always one round further
		got, ok := supportedRound(engine)
		if !ok {
			break
		}
		moves++
		engine.mu.Lock()
		engine.currentState.Round = got
		engine.mu.Unlock()
	}
	if moves > maxFutureRounds {
		t.Fatalf("a lone validator holding a quarter of the power moved this engine %d rounds at one height, want at most %d (maxFutureRounds)", moves, maxFutureRounds)
	}
}

// The round start moves the validator there, through the same rule.
func TestAValidatorLevelWithOthersMovesToTheRoundOfTheOneAhead(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 9)
	claimed(engine, tv, 1, roundClaimOf{1, 10}, roundClaimOf{2, 14})
	engine.startNewRound()
	if got := roundOf(engine); got != 14 {
		t.Fatalf("the validator started round %d, want round 14 (the one validator alone ahead, with the other level)", got)
	}
}

// A validator that is not needed is not moved by one message: what a lone validator says cannot
// take the round away from a quorum that is in step (the property the rule above is careful of).
func TestOneValidatorAheadDoesNotMoveAQuorumThatIsInStep(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 10)
	claimed(engine, tv, 1, roundClaimOf{1, 10}, roundClaimOf{3, 10})
	sendVote(t, engine, tv, 2, 12)
	engine.startNewRound()
	if got := roundOf(engine); got != 11 {
		t.Fatalf("the validator is in round %d after one validator's vote for round 12, want 11: a quorum in step needs no one ahead", got)
	}
}

// What a validator level with this one sends is what tells it that it is there: its messages
// are for the round this validator is in, and no claim is made of those (only a message for a
// later round is one), so they have to be counted as they arrive. Here validator 1 votes in
// this validator's own round and validator 2, alone, in a round four later; that is all this
// validator hears, and it moves.
func TestAValidatorHearsTheOnesLevelWithItAndFollowsTheOneAhead(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 10)
	sendVote(t, engine, tv, 1, 10)
	sendVote(t, engine, tv, 2, 14)
	if len(engine.roundSkipCh) != 1 {
		t.Fatalf("the vote of the validator ahead did not wake the round loop although the only other validator in the set that is needed had voted in this round")
	}
	engine.startNewRound()
	if got := roundOf(engine); got != 14 {
		t.Fatalf("the validator started round %d, want round 14", got)
	}
}

// With two validators nothing changes: one peer ahead is followed, as it always was, and one
// that is level or not heard from is not.
func TestTwoValidatorsFollowThePeerAheadAsBefore(t *testing.T) {
	tv := newTestValidators(t, 2)
	for _, tc := range []struct {
		name   string
		claims []roundClaimOf
		want   int
	}{
		{"peer ahead", []roundClaimOf{{1, 14}}, 14},
		{"peer level", []roundClaimOf{{1, 10}}, 0},
		{"peer behind", []roundClaimOf{{1, 6}}, 0},
		{"peer not heard from", nil, 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineAt(t, tv, 10)
			claimed(engine, tv, 1, tc.claims...)
			got, ok := supportedRound(engine)
			if (tc.want == 0) == ok || (ok && got != tc.want) {
				t.Fatalf("supported round is (%d, %v), want %d (0: none)", got, ok, tc.want)
			}
		})
	}
}

// What a validator sends in the round this one is in, or in one it has passed, says that it is
// there, and only that: an older message never lowers the record, and a message for another
// height says nothing of where its signer is at this one.
func TestAMessageForThisRoundOrAnEarlierOneRecordsWhereItsSignerIs(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 10)

	sendVote(t, engine, tv, 1, 10)
	if round, ok := claimedRoundOf(engine, tv.addrs[1]); !ok || round != 10 {
		t.Fatalf("a vote for the round this validator is in was recorded as (%d, %v), want round 10", round, ok)
	}
	sendVote(t, engine, tv, 1, 7)
	if round, _ := claimedRoundOf(engine, tv.addrs[1]); round != 10 {
		t.Fatalf("an older vote lowered the record of its signer to round %d", round)
	}
	if err := engine.HandleProposal(newSignedProposal(t, tv.keys[2], tv.addrs[2], goodBlock(8, tv.addrs[2], 0x01), 8)); err != nil {
		t.Fatalf("proposal for an earlier round: %v", err)
	}
	if round, ok := claimedRoundOf(engine, tv.addrs[2]); !ok || round != 8 {
		t.Fatalf("a proposal for an earlier round was recorded as (%d, %v), want round 8", round, ok)
	}
	if err := engine.HandleVote(signVoteAs(t, tv.keys[3], tv.addrs[3], Prevote, sha256Sum([]byte("some block")), 10, 2)); err != nil {
		t.Fatalf("vote for another height: %v", err)
	}
	if _, ok := claimedRoundOf(engine, tv.addrs[3]); ok {
		t.Fatalf("a vote for another height was recorded as where its signer is")
	}
}

// A round taken from a validator that is ahead is never one this validator has signed in: the
// record of its votes puts it in the round after the last one it signed in, as for any other move.
func TestFollowingAValidatorAheadNeverPutsAValidatorInARoundItSignedIn(t *testing.T) {
	tv := newTestValidators(t, 4)
	path := filepath.Join(t.TempDir(), "sign.json")
	record := signState{Height: 1, Round: 30, Prevote: &signedValue{BlockHash: sha256Sum([]byte("some block"))}}
	if err := writeSignState(path, record); err != nil {
		t.Fatalf("write the vote record: %v", err)
	}
	engine, _, _ := engineAt(t, tv, 0, WithSignStatePath(path))
	claimed(engine, tv, 1, roundClaimOf{1, 1}, roundClaimOf{2, 20})
	engine.startNewRound()
	if got := roundOf(engine); got != 31 {
		t.Fatalf("the validator started round %d, want 31: it signed in round 30, and the one ahead is in round 20", got)
	}
}

// Following a validator that is ahead is a move of rounds and nothing else: the lock stays.
func TestFollowingAValidatorAheadKeepsTheLock(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 9)
	hash := sha256Sum([]byte("locked block"))
	engine.mu.Lock()
	engine.lockedRound, engine.lockedBlockHash = 5, hash
	engine.mu.Unlock()
	claimed(engine, tv, 1, roundClaimOf{1, 10}, roundClaimOf{2, 14})
	engine.startNewRound()
	if got := roundOf(engine); got != 14 {
		t.Fatalf("the validator started round %d, want 14", got)
	}
	engine.mu.RLock()
	round, kept := engine.lockedRound, bytes.Equal(engine.lockedBlockHash, hash)
	engine.mu.RUnlock()
	if round != 5 || !kept {
		t.Fatalf("the lock is (round %d, same block %v) after the move, want the lock of round 5 on the same block", round, kept)
	}
}

// followSpec is the rule of supportedRoundLocked written out plainly, for a validator (index 0) in
// round own, of a set with the given powers, and the rounds the others have been seen in
// (validator index -> round, only the ones seen at this height). A round is supported by the
// validators seen in it or later when they hold more than a third of the power and are a quorum
// with this validator; and, when this validator and the ones seen in its own round or in the
// maxFutureRounds before it are not a quorum, when they are a quorum with those. The highest
// supported round wins.
func followSpec(powers []int64, own int, seen map[int]int) (int, bool) {
	var total int64
	for _, p := range powers {
		total += p
	}
	quorum := func(p int64) bool { return p >= 2*total/3+1 }
	var level int64
	var ahead []int
	for idx, round := range seen {
		switch {
		case round > own:
			ahead = append(ahead, idx)
		case round >= own-maxFutureRounds:
			level += powers[idx]
		}
	}
	rounds := map[int]bool{}
	for _, idx := range ahead {
		rounds[seen[idx]] = true
	}
	var candidates []int
	for round := range rounds {
		candidates = append(candidates, round)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(candidates)))
	power := func(from int) int64 {
		var sum int64
		for _, idx := range ahead {
			if seen[idx] >= from {
				sum += powers[idx]
			}
		}
		return sum
	}
	for _, round := range candidates {
		// the first rule: at least a third of the power is seen in that round or later (this validator's own power is not counted)
		if p := power(round); 3*p >= total {
			return round, true
		}
	}
	if quorum(powers[0] + level) {
		return 0, false
	}
	for _, round := range candidates {
		if quorum(power(round) + powers[0] + level) {
			return round, true
		}
	}
	return 0, false
}

// The rule is what it says on sets of every size and weight, with the others seen ahead of this
// validator, level with it, behind it and not at all, and at an earlier height (which is not
// seen). It never moves a validator to a round it is already past, and it moves it to the
// highest round the evidence supports.
func TestTheFollowRuleIsWhatItSaysOnRandomSets(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	moved, stayed := 0, 0
	for i := 0; i < 400; i++ {
		n := 2 + rnd.Intn(8)
		powers := make([]int64, n)
		for j := range powers {
			powers[j] = 1 + int64(rnd.Intn(4))
		}
		own := 12 + rnd.Intn(4)
		seen := map[int]int{}
		earlier := map[int]int{}
		for idx := 1; idx < n; idx++ {
			switch rnd.Intn(6) {
			case 0: // not heard from
			case 1: // seen at the height before
				earlier[idx] = own + rnd.Intn(5)
			default:
				seen[idx] = own - 11 + rnd.Intn(19) // from before the window to well ahead
			}
		}
		tv := newWeightedValidators(t, powers...)
		engine, _, _ := engineAt(t, tv, own)
		claimedBy := func(height uint64, rounds map[int]int) {
			for idx, round := range rounds {
				claimed(engine, tv, height, roundClaimOf{idx, round})
			}
		}
		claimedBy(0, earlier)
		claimedBy(1, seen)
		wantRound, want := followSpec(powers, own, seen)
		gotRound, got := supportedRound(engine)
		if got != want || (got && gotRound != wantRound) {
			t.Fatalf("powers %v, this validator in round %d, the others seen in rounds %v: supportedRoundLocked says (%d, %v), the rule says (%d, %v)",
				powers, own, seen, gotRound, got, wantRound, want)
		}
		if got && gotRound <= own {
			t.Fatalf("powers %v: the validator is moved to round %d from round %d", powers, gotRound, own)
		}
		if got {
			moved++
		} else {
			stayed++
		}
	}
	if moved < 50 || stayed < 50 {
		t.Fatalf("the sets drawn moved the validator %d times and left it %d times: too few of either to say anything", moved, stayed)
	}
}
