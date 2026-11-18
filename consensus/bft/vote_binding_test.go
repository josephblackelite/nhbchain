package bft

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-31: from the vote binding height a vote is signed, and checked, over the
// network's genesis hash.

var (
	bindGenesisA = bytes.Repeat([]byte{0xA1}, 32)
	bindGenesisB = bytes.Repeat([]byte{0xB2}, 32)
)

func signBoundVoteAs(t *testing.T, key *crypto.PrivateKey, addr []byte, genesis []byte, vt VoteType, blockHash []byte, round int, height uint64) *SignedVote {
	t.Helper()
	vote := &Vote{BlockHash: blockHash, Round: round, Type: vt, Height: height}
	digest := types.BoundVoteDigest(genesis, vote.bytes())
	sig, err := ethSign(digest[:], key)
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	return &SignedVote{Vote: vote, Validator: addr, Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

func TestVoteBinding_AnEngineSignsByHeight(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisA, 100))
	hash := bytes.Repeat([]byte{0x01}, 32)

	below, err := engine.createVote(Prevote, hash, 0, 99)
	if err != nil {
		t.Fatalf("create vote below the height: %v", err)
	}
	if err := verifyVoteSignature(below); err != nil {
		t.Fatalf("a vote below the binding height must carry the legacy signature: %v", err)
	}
	if err := engine.verifySignedVote(below); err != nil {
		t.Fatalf("the engine must accept its own vote below the height: %v", err)
	}

	at, err := engine.createVote(Prevote, hash, 0, 100)
	if err != nil {
		t.Fatalf("create vote at the height: %v", err)
	}
	if err := engine.verifySignedVote(at); err != nil {
		t.Fatalf("the engine must accept its own vote at the height: %v", err)
	}
	if err := verifyVoteSignature(at); err == nil {
		t.Fatalf("a vote at the binding height verified under the legacy digest: it is not bound")
	}
}

func TestVoteBinding_AVoteOfAnotherNetworkIsRefused(t *testing.T) {
	tv := newTestValidators(t, 1)
	a, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisA, 100))
	b, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisB, 100))
	hash := bytes.Repeat([]byte{0x02}, 32)
	vote, err := a.createVote(Precommit, hash, 0, 150)
	if err != nil {
		t.Fatalf("create vote: %v", err)
	}
	if err := a.verifySignedVote(vote); err != nil {
		t.Fatalf("network A must accept its own vote: %v", err)
	}
	if err := b.verifySignedVote(vote); err == nil {
		t.Fatalf("network B accepted a vote signed over network A's genesis hash")
	}
}

func TestVoteBinding_ALegacyVoteAtOrAboveTheHeightIsRefused(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisA, 100))
	hash := bytes.Repeat([]byte{0x03}, 32)
	if err := engine.verifySignedVote(signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hash, 0, 100)); err == nil {
		t.Fatalf("a legacy vote at the binding height was accepted")
	}
	if err := engine.verifySignedVote(signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hash, 0, 99)); err != nil {
		t.Fatalf("a legacy vote below the binding height must still be accepted: %v", err)
	}
}

func TestVoteBinding_ABoundVoteBelowTheHeightIsRefused(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisA, 100))
	hash := bytes.Repeat([]byte{0x04}, 32)
	if err := engine.verifySignedVote(signBoundVoteAs(t, tv.keys[0], tv.addrs[0], bindGenesisA, Prevote, hash, 0, 99)); err == nil {
		t.Fatalf("a vote signed over the genesis hash below the binding height was accepted")
	}
}

func TestVoteBinding_OffKeepsTheLegacyDigestAtEveryHeight(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, _, _ := newVoter(t, tv) // no option: the shipped default
	hash := bytes.Repeat([]byte{0x05}, 32)
	for _, h := range []uint64{1, 100, 1_000_000} {
		v, err := engine.createVote(Prevote, hash, int(h%7), h)
		if err != nil {
			t.Fatalf("create vote at %d: %v", h, err)
		}
		if err := verifyVoteSignature(v); err != nil {
			t.Fatalf("with the binding off a vote at %d must carry the legacy signature: %v", h, err)
		}
	}
	// A binding with a genesis but no height is off as well.
	zero, _, _ := newVoter(t, tv, WithVoteBinding(bindGenesisA, 0))
	v, err := zero.createVote(Prevote, hash, 0, 500)
	if err != nil {
		t.Fatalf("create vote: %v", err)
	}
	if err := verifyVoteSignature(v); err != nil {
		t.Fatalf("a zero binding height must keep the legacy digest: %v", err)
	}
}

func TestVoteBinding_ActiveWithoutAGenesisHashSignsNothing(t *testing.T) {
	tv := newTestValidators(t, 1)
	engine, _, _ := newVoter(t, tv, WithVoteBinding(nil, 100))
	if _, err := engine.createVote(Prevote, bytes.Repeat([]byte{0x06}, 32), 0, 100); err == nil {
		t.Fatalf("a vote was signed with the binding active and no genesis hash")
	}
	if _, err := engine.createVote(Prevote, bytes.Repeat([]byte{0x06}, 32), 0, 99); err != nil {
		t.Fatalf("a vote below the height needs no genesis hash: %v", err)
	}
}

func votesAt(t *testing.T, tv *testValidators, hash []byte, round int, height uint64, genesis []byte) []*SignedVote {
	t.Helper()
	out := make([]*SignedVote, 0, len(tv.keys))
	for i := range tv.keys {
		if genesis == nil {
			out = append(out, signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hash, round, height))
		} else {
			out = append(out, signBoundVoteAs(t, tv.keys[i], tv.addrs[i], genesis, Prevote, hash, round, height))
		}
	}
	return out
}

// A polka proof is counted under the same rule as a single vote.
func TestVoteBinding_PolkaProofsAreCountedUnderTheSameRule(t *testing.T) {
	tv := newTestValidators(t, 4)
	hash := bytes.Repeat([]byte{0x07}, 32)
	const height = 150
	binding := types.VoteBinding{Genesis: bindGenesisA, FromHeight: 100}
	total := big.NewInt(4)

	bound := votesAt(t, tv, hash, 2, height, bindGenesisA)
	legacy := votesAt(t, tv, hash, 2, height, nil)
	foreign := votesAt(t, tv, hash, 2, height, bindGenesisB)
	if !verifyPolkaProofBound(bound, 2, hash, height, tv.set, total, binding) {
		t.Fatalf("a proof of bound votes must be a polka")
	}
	if verifyPolkaProofBound(legacy, 2, hash, height, tv.set, total, binding) {
		t.Fatalf("a proof of legacy votes at the binding height was a polka")
	}
	if verifyPolkaProofBound(foreign, 2, hash, height, tv.set, total, binding) {
		t.Fatalf("a proof of votes signed for another network was a polka")
	}
	mixed := append(append([]*SignedVote(nil), bound[:2]...), legacy[2:]...)
	if counted, _ := countedPolkaVotesBound(mixed, 2, hash, height, tv.set, binding); len(counted) != 2 {
		t.Fatalf("only the two bound votes may count, got %d", len(counted))
	}
	// The unbound entry points keep the legacy rule.
	if !verifyPolkaProof(legacy, 2, hash, height, tv.set, total) {
		t.Fatalf("a proof of legacy votes must be a polka under the legacy rule")
	}
	// Below the height a proof is judged by the legacy digest only.
	if !verifyPolkaProofBound(votesAt(t, tv, hash, 2, 99, nil), 2, hash, 99, tv.set, total, binding) {
		t.Fatalf("a legacy proof below the binding height must be a polka")
	}
}

// The vote the BFT engine signs and the one core/types verifies in a quorum certificate
// are separate definitions of the same payload; the bound digest must agree between them.
func TestVoteBinding_ParityWithTheQuorumCertificateDigest(t *testing.T) {
	binding := types.VoteBinding{Genesis: bindGenesisA, FromHeight: 100}
	hash := bytes.Repeat([]byte{0x08}, 32)
	for _, height := range []uint64{50, 100, 101} {
		engineVote := &Vote{BlockHash: hash, Round: 3, Type: Precommit, Height: height}
		certVote := &types.PrecommitVote{BlockHash: hash, Round: 3, Type: types.VoteTypePrecommit, Height: height}
		got, err := binding.Digest(height, engineVote.bytes())
		if err != nil {
			t.Fatal(err)
		}
		want, err := certVote.DigestBound(binding)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("height %d: the engine and the quorum certificate disagree on the digest", height)
		}
	}
}

func boundPolkaOf(t *testing.T, tv *testValidators, block *types.Block, round int, genesis []byte) []*SignedVote {
	t.Helper()
	hash := headerHash(t, block)
	var votes []*SignedVote
	for i := range tv.keys {
		votes = append(votes, signBoundVoteAs(t, tv.keys[i], tv.addrs[i], genesis, Prevote, hash, round, 1))
	}
	return votes
}

// The polka a proposal cites is learnt only when its votes are bound, from the binding height.
func TestVoteBinding_AProposalsPolkaIsLearntOnlyWhenItsVotesAreBound(t *testing.T) {
	tv := newTestValidators(t, 4)
	block := blockWithTxs(1, tv.addrs[0], 0x41, &types.Transaction{Nonce: 1})

	cases := []struct {
		name  string
		proof []*SignedVote
		want  bool
	}{
		{"legacy votes", polkaOf(t, tv, block, 4), false},
		{"votes of another network", boundPolkaOf(t, tv, block, 4, bindGenesisB), false},
		{"bound votes", boundPolkaOf(t, tv, block, 4, bindGenesisA), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineAt(t, tv, 8, WithVoteBinding(bindGenesisA, 1))
			if err := engine.HandleProposal(polkaMessage(t, tv, 1, block, 9, 4, tc.proof)); err != nil {
				t.Logf("proposal: %v", err)
			}
			held, round, _ := learnt(engine)
			if got := held != nil && round == 4; got != tc.want {
				t.Fatalf("polka of %s learnt = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The engine's own polka check (used for a proposal's claim and for a restored lock) holds a proof
// to the binding too.
func TestVoteBinding_TheEnginesPolkaCheckHoldsAProofToTheBinding(t *testing.T) {
	tv := newTestValidators(t, 4)
	block := blockWithTxs(1, tv.addrs[0], 0x42, &types.Transaction{Nonce: 1})
	hash := headerHash(t, block)
	engine, _, _ := engineAt(t, tv, 2, WithVoteBinding(bindGenesisA, 1))
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if !engine.verifyPolkaProofLocked(boundPolkaOf(t, tv, block, 2, bindGenesisA), 2, hash, 1) {
		t.Fatalf("a proof of bound votes must be a polka")
	}
	if engine.verifyPolkaProofLocked(polkaOf(t, tv, block, 2), 2, hash, 1) {
		t.Fatalf("a proof of legacy votes at the binding height was a polka")
	}
	if engine.verifyPolkaProofLocked(boundPolkaOf(t, tv, block, 2, bindGenesisB), 2, hash, 1) {
		t.Fatalf("a proof of votes signed for another network was a polka")
	}
}
