package bft

// Regression tests for three residual weaknesses of the round bounds (see
// round_sync.go): a round number with no upper bound, that wrapped to a negative
// one when incremented; a proposal buffer that told two encodings of one
// signature apart; and a vote whose block hash was as large as a message may be.
// Each test drives the real entry points and fails on the engine without the fix.

import (
	"math"
	"math/big"
	"path/filepath"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// twinSignature is the other encoding of a secp256k1 signature that recovers the
// same signer: (r, n-s, v^1).
func twinSignature(sig []byte) []byte {
	n := ethcrypto.S256().Params().N
	s := new(big.Int).SetBytes(sig[32:64])
	b := new(big.Int).Sub(n, s).Bytes()
	out := make([]byte, 65)
	copy(out, sig[:32])
	copy(out[64-len(b):64], b)
	out[64] = sig[64] ^ 1
	return out
}

func claimedRoundOf(e *Engine, signer []byte) (int, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	claim, ok := e.roundClaims[string(signer)]
	return claim.round, ok
}

// One vote for round math.MaxInt64 from the only other validator of two used to be
// adopted: the engine jumped there, and the next round start wrapped to the most
// negative round, where the signed-round floor kept it for the height.
func TestRoundAboveTheCapIsNeitherAdoptedNorCounted(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0)
	for _, round := range []int{math.MaxInt64, math.MaxInt64 - 1, maxRound + 1, -1, math.MinInt64} {
		for _, vt := range []VoteType{Prevote, Precommit} {
			if err := engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], vt, []byte("x"), round, 1)); err == nil {
				t.Errorf("a vote for round %d was accepted", round)
			}
		}
		block := goodBlock(1, tv.addrs[1], 1)
		if err := engine.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, round)); err == nil {
			t.Errorf("a proposal for round %d was accepted", round)
		}
	}
	if _, ok := claimedRoundOf(engine, tv.addrs[1]); ok {
		t.Errorf("a round above the cap was recorded as where the validator is")
	}
	if len(engine.roundSkipCh) != 0 {
		t.Errorf("a round above the cap woke the round loop")
	}
	for i := 0; i < 3; i++ {
		engine.startNewRound()
		if got := roundOf(engine); got != i+1 {
			t.Fatalf("round after start %d = %d, want %d", i+1, got, i+1)
		}
	}
}

// The round number never becomes negative: reached by the timers, the top round is
// where it stays, and it can still be signed in there.
func TestRoundNeverBecomesNegative(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, maxRound-2)
	for i := 0; i < 4; i++ {
		engine.startNewRound()
		want := maxRound - 2 + i + 1
		if want > maxRound {
			want = maxRound
		}
		if got := roundOf(engine); got != want {
			t.Fatalf("round after start %d = %d, want %d", i+1, got, want)
		}
		if _, err := engine.createVote(Prevote, nil, want, 1); err != nil {
			t.Fatalf("vote in round %d: %v", want, err)
		}
	}
}

// hostileVote is one signed vote of validator 1 for round, of the kind an
// attacker can send whenever it likes.
func hostileVote(t *testing.T, tv *testValidators, vt VoteType, round int) *SignedVote {
	return signVoteAs(t, tv.keys[1], tv.addrs[1], vt, []byte("x"), round, 1)
}

// One signed vote for the highest round a vote may name, from a validator holding
// half of the power, used to take the engine to that round, the one round it can
// never leave: it signed a nil prevote there when the round timed out, the vote
// guard then refused a prevote for any block in the same round, and the round
// number could not move on, so the height was over on that validator, and after a
// restart too (the signed-round floor is durable). The vote is still accepted and
// still moves the engine, but not to where it cannot come back from.
func TestOneVoteAtTheTopRoundDoesNotWedgeTheHeight(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0)
	if err := engine.HandleVote(hostileVote(t, tv, Prevote, maxRound)); err != nil {
		t.Fatalf("a vote for the highest round was refused: %v", err)
	}
	engine.startNewRound()
	jumped := roundOf(engine)
	if jumped <= 0 || jumped >= maxRound {
		t.Fatalf("round after the jump = %d, want a round above 0 and below the top round %d", jumped, maxRound)
	}
	// The round times out with no proposal: the honest path signs a nil prevote.
	if _, err := engine.createVote(Prevote, nil, jumped, 1); err != nil {
		t.Fatalf("nil prevote: %v", err)
	}
	// More of the same from the attacker changes nothing: it is already as far
	// ahead as a message can take the engine.
	for _, vt := range []VoteType{Prevote, Precommit} {
		for _, round := range []int{maxRound, maxRound - 1, jumped + 1} {
			_ = engine.HandleVote(hostileVote(t, tv, vt, round))
		}
	}
	block := headerHash(t, goodBlock(1, tv.addrs[1], 1))
	for i := 0; i < 3; i++ {
		engine.startNewRound()
		round := roundOf(engine)
		if round != jumped+i+1 {
			t.Fatalf("round after start %d = %d, want %d: the round did not go on", i+1, round, jumped+i+1)
		}
		if _, err := engine.createVote(Prevote, block, round, 1); err != nil {
			t.Fatalf("prevote for a block in round %d after the jump: %v", round, err)
		}
	}
}

// The same after a restart of the victim: its record of what it signed is durable,
// and the round it starts from must still leave room to sign a block.
func TestOneVoteAtTheTopRoundDoesNotWedgeTheHeightAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0, WithSignStatePath(path))
	if err := engine.HandleVote(hostileVote(t, tv, Prevote, maxRound)); err != nil {
		t.Fatal(err)
	}
	engine.startNewRound()
	jumped := roundOf(engine)
	if _, err := engine.createVote(Prevote, nil, jumped, 1); err != nil {
		t.Fatalf("nil prevote: %v", err)
	}

	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}
	restarted := NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))
	restarted.mu.Lock()
	restarted.currentState = State{Height: 1, Round: 0}
	restarted.mu.Unlock()
	restarted.startNewRound()
	round := roundOf(restarted)
	if round <= jumped || round >= maxRound {
		t.Fatalf("restarted round = %d, want above %d and below the top round", round, jumped)
	}
	block := headerHash(t, goodBlock(1, tv.addrs[1], 1))
	if _, err := restarted.createVote(Prevote, block, round, 1); err != nil {
		t.Fatalf("prevote for a block after the restart: %v", err)
	}
}

// The other half of the recovery: once the attacker is an honest validator again
// it finds the victim by the victim's own round and follows it, and the two can
// vote for the same block in the round after.
func TestValidatorsMeetAgainAfterAJumpToAFarRound(t *testing.T) {
	tv := newTestValidators(t, 2)
	victim, _, _ := engineAt(t, tv, 0)
	if err := victim.HandleVote(hostileVote(t, tv, Prevote, maxRound)); err != nil {
		t.Fatal(err)
	}
	victim.startNewRound()
	far := roundOf(victim)
	if _, err := victim.createVote(Prevote, nil, far, 1); err != nil {
		t.Fatal(err)
	}

	// The attacker's engine, restarted in round 0, hears the victim.
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[1]}
	other := NewEngine(node, tv.keys[1], &captureBroadcaster{})
	other.mu.Lock()
	other.currentState = State{Height: 1, Round: 0}
	other.mu.Unlock()
	if err := other.HandleVote(signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, nil, far, 1)); err != nil {
		t.Fatal(err)
	}
	other.startNewRound()
	if got := roundOf(other); got != far {
		t.Fatalf("the restarted validator is in round %d, the other one in %d", got, far)
	}

	block := headerHash(t, goodBlock(1, tv.addrs[1], 1))
	victim.startNewRound()
	other.startNewRound()
	if roundOf(victim) != far+1 || roundOf(other) != far+1 {
		t.Fatalf("rounds %d and %d, want both %d", roundOf(victim), roundOf(other), far+1)
	}
	for name, e := range map[string]*Engine{"victim": victim, "other": other} {
		if _, err := e.createVote(Prevote, block, far+1, 1); err != nil {
			t.Fatalf("%s: prevote for the block: %v", name, err)
		}
	}
}

// However far above the limit a message is, the validator that sent it is counted
// at the limit and no higher, and a lower claim of a validator is not lost.
func TestRoundClaimsAreCountedOnlyUpToTheJumpLimit(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0)
	if err := engine.HandleVote(hostileVote(t, tv, Prevote, maxRound)); err != nil {
		t.Fatal(err)
	}
	got, ok := claimedRoundOf(engine, tv.addrs[1])
	if !ok || got != maxJumpRound {
		t.Fatalf("claim = %d (%v), want %d", got, ok, maxJumpRound)
	}
	if maxJumpRound >= maxRound || maxJumpRound < 1<<20 {
		t.Fatalf("the jump limit %d must be far above any real round and below the top round %d", maxJumpRound, maxRound)
	}
	other, _, _ := engineAt(t, tv, 0)
	if err := other.HandleVote(hostileVote(t, tv, Prevote, maxJumpRound-5)); err != nil {
		t.Fatal(err)
	}
	if got, _ := claimedRoundOf(other, tv.addrs[1]); got != maxJumpRound-5 {
		t.Fatalf("claim below the limit = %d, want %d", got, maxJumpRound-5)
	}
}

func TestRoundAfterIsMonotoneAndBounded(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 1}, {7, 8}, {maxRound - 1, maxRound}, {maxRound, maxRound}, {maxRound + 1, maxRound},
		{math.MaxInt64, maxRound}, {-1, 0}, {math.MinInt64, 0},
	} {
		if got := roundAfter(tc.in); got != tc.want {
			t.Errorf("roundAfter(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// A record of a signed round that is out of range (a damaged file) cannot push the
// round negative either.
func TestSignedRoundFloorAtTheLimitDoesNotWrap(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0)
	engine.signMu.Lock()
	engine.signed = signState{Height: 1, Round: math.MaxInt64, Prevote: &signedValue{}}
	engine.signMu.Unlock()
	engine.startNewRound()
	if got := roundOf(engine); got != maxRound {
		t.Fatalf("round = %d, want %d", got, maxRound)
	}
}

// selectProposerIn is safe for any round, including with no stake at all, where the
// proposer is picked by the round modulo the number of validators.
func TestSelectProposerForAnyRound(t *testing.T) {
	tv := newTestValidators(t, 3)
	node := shapedNode{signGuardNode: &signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}, stake: map[string]int64{}}
	engine := NewEngine(node, tv.keys[0], &captureBroadcaster{})
	for _, round := range []int{0, 1, 2, 3, 1 << 24, math.MaxInt64, -1, -2, math.MinInt64} {
		if got := engine.selectProposerIn(tv.set, round); len(got) == 0 {
			t.Errorf("no proposer for round %d", round)
		}
	}
	// In range, the choice is what it always was.
	sorted := sortedAddrs(tv)
	for round := 0; round < 7; round++ {
		if got := engine.selectProposerIn(tv.set, round); string(got) != string(sorted[round%3]) {
			t.Errorf("round %d: proposer %x, want %x", round, got, sorted[round%3])
		}
	}
}

func sortedAddrs(tv *testValidators) [][]byte {
	out := append([][]byte(nil), tv.addrs...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if string(out[j]) < string(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Any peer can replay a proposal with its signature re-encoded. The copy is the same
// proposal: it must neither count against the proposer's quota nor push out its
// earlier one. (It is refused outright now, because the twin is not canonical; the
// dedupe below holds for whatever signature bytes a copy carries.)
func TestReEncodedProposalDoesNotEvictTheProposersEarlierOne(t *testing.T) {
	tv := newTestValidators(t, 4)
	tips := map[uint64][]byte{0: lastCommitForSchedule(t, tv, nil, map[int]int{3: 1, 7: 1})}
	engine, _ := newChainEngine(t, tv, tips)
	inRound(engine, 1, 1)
	p3 := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(3, tv.addrs[1], 1), 3)
	p7 := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(7, tv.addrs[1], 2), 7)
	for _, p := range []*SignedProposal{p3, p7} {
		if err := engine.HandleProposal(p); err != nil {
			t.Fatal(err)
		}
	}
	twin := *p7
	sig := *p7.Signature
	sig.Signature = twinSignature(p7.Signature.Signature)
	twin.Signature = &sig
	_ = engine.HandleProposal(&twin)
	if got := bufferedProposalCount(engine); got != 2 {
		t.Fatalf("%d proposals buffered after a re-encoded copy, want 2", got)
	}
	engine.mu.RLock()
	kept := len(engine.bufferedProposal[1][3])
	engine.mu.RUnlock()
	if kept != 1 {
		t.Errorf("the proposer's round-3 proposal was pushed out by a copy of its round-7 one")
	}
}

// The buffer itself keys a proposal on its content, whatever the signature bytes.
func TestBufferedProposalsAreKeyedOnContent(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _ := newChainEngine(t, tv, nil)
	inRound(engine, 1, 1)
	first := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(4, tv.addrs[1], 1), 4)
	other := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(4, tv.addrs[1], 2), 4)
	engine.mu.Lock()
	engine.bufferProposalLocked(first)
	for i := 0; i < 5; i++ {
		copyOf := *first
		copyOf.Signature = &Signature{Scheme: SignatureSchemeSecp256k1, Signature: append([]byte{byte(i)}, first.Signature.Signature[1:]...)}
		engine.bufferProposalLocked(&copyOf)
	}
	engine.mu.Unlock()
	if got := bufferedProposalCount(engine); got != 1 {
		t.Fatalf("%d copies of one proposal buffered, want 1", got)
	}
	engine.mu.Lock()
	engine.bufferProposalLocked(other)
	engine.mu.Unlock()
	if got := bufferedProposalCount(engine); got != 2 {
		t.Fatalf("a different proposal of the same round was not kept: %d buffered", got)
	}
}

// Signatures made by this engine are always accepted; their twins are not.
func TestHighSTwinIsRefusedAndTheHonestSignatureIsNot(t *testing.T) {
	tv := newTestValidators(t, 2)
	engine, _, _ := engineAt(t, tv, 0)
	for i := 0; i < 50; i++ {
		vote := signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, []byte("x"), i%3, 1)
		if err := verifyVoteSignature(vote); err != nil {
			t.Fatalf("an honest vote was refused: %v", err)
		}
		twin := *vote
		sig := *vote.Signature
		sig.Signature = twinSignature(vote.Signature.Signature)
		twin.Signature = &sig
		if err := verifyVoteSignature(&twin); err == nil {
			t.Fatalf("the high-s twin of a vote verified")
		}
		p := newSignedProposal(t, tv.keys[1], tv.addrs[1], goodBlock(1, tv.addrs[1], byte(i)), 0)
		if err := engine.verifySignedProposal(p); err != nil {
			t.Fatalf("an honest proposal was refused: %v", err)
		}
		ptwin := *p
		psig := *p.Signature
		psig.Signature = twinSignature(p.Signature.Signature)
		ptwin.Signature = &psig
		if err := engine.verifySignedProposal(&ptwin); err == nil {
			t.Fatalf("the high-s twin of a proposal verified")
		}
	}
	// What the engine signs itself.
	own, err := engine.createVote(Prevote, []byte("x"), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVoteSignature(own); err != nil {
		t.Fatalf("this engine's own vote was refused: %v", err)
	}
}

// A vote carries the hash of a header or none; the rest of the transport limit is
// not room for one. Three faulty validators sending sixteen votes each with a hash
// of 900 KB used to be kept, or stashed, in full.
func TestOversizedVoteHashIsRefusedBeforeItIsKept(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 2)
	huge := make([]byte, 900<<10)
	for _, i := range []int{1, 2, 3} {
		for round := 0; round < 16; round++ {
			for _, vt := range []VoteType{Prevote, Precommit} {
				if err := engine.HandleVote(signVoteAs(t, tv.keys[i], tv.addrs[i], vt, huge, 3+round%8, 1)); err == nil {
					t.Fatalf("a vote with a %d-byte hash was accepted", len(huge))
				}
			}
		}
		// For the current round too, which is stashed until there is a proposal.
		if err := engine.HandleVote(signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, huge, 2, 1)); err == nil {
			t.Fatalf("a current-round vote with a %d-byte hash was accepted", len(huge))
		}
	}
	engine.mu.RLock()
	buffered := len(engine.bufferedVotes)
	engine.mu.RUnlock()
	if buffered != 0 || len(engine.voteCh) != 0 {
		t.Fatalf("%d heights of votes buffered, %d queued", buffered, len(engine.voteCh))
	}
	if _, ok := claimedRoundOf(engine, tv.addrs[1]); ok {
		t.Errorf("a refused vote was recorded as where its signer is")
	}
}

func TestVoteHashOfTheHeaderLengthAndNilVotesAreAccepted(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 0)
	for _, hash := range [][]byte{nil, {}, []byte("x"), make([]byte, maxVoteHashLen)} {
		if err := engine.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, 3, 1)); err != nil {
			t.Errorf("a vote with a %d-byte hash was refused: %v", len(hash), err)
		}
	}
	if err := engine.HandleVote(signVoteAs(t, tv.keys[2], tv.addrs[2], Prevote, make([]byte, maxVoteHashLen+1), 3, 1)); err == nil {
		t.Errorf("a vote with a hash one byte too long was accepted")
	}
}

// Nothing of a secp256k1 vote is kept that its signature does not cover: the public
// key field is not used for it, and was room for padding.
func TestPaddingOnASecp256k1VoteIsNotKept(t *testing.T) {
	tv := newTestValidators(t, 4)
	engine, _, _ := engineAt(t, tv, 0)
	vote := signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, []byte("x"), 3, 1)
	vote.Signature.PublicKey = make([]byte, 900<<10)
	if err := engine.HandleVote(vote); err != nil {
		t.Fatal(err)
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	kept := engine.bufferedVotes[1][3]
	if len(kept) != 1 {
		t.Fatalf("%d votes kept", len(kept))
	}
	if len(kept[0].Signature.PublicKey) != 0 {
		t.Errorf("a vote was kept with %d bytes of unsigned padding", len(kept[0].Signature.PublicKey))
	}
	if len(vote.Signature.PublicKey) == 0 {
		t.Errorf("the caller's message was changed")
	}
}
