package bft

// DC-01 regression tests: the engine used to accept exactly 2/3 of the voting
// power as a quorum (threshold = floor((2T+2)/3) with a >= comparison), so on
// any validator set whose total power is a multiple of 3 two conflicting
// quorums could overlap in a single faulty validator. The threshold is now
// types.QuorumThreshold (floor(2T/3)+1) at every site.

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// newValidatorFixture builds an n-validator set with the given powers.
func newValidatorFixture(t *testing.T, powers ...int64) ([]*crypto.PrivateKey, [][]byte, map[string]*big.Int) {
	t.Helper()
	keys := make([]*crypto.PrivateKey, 0, len(powers))
	addrs := make([][]byte, 0, len(powers))
	set := make(map[string]*big.Int, len(powers))
	for i, p := range powers {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate validator key %d: %v", i, err)
		}
		addr := key.PubKey().Address().Bytes()
		keys = append(keys, key)
		addrs = append(addrs, addr)
		set[string(addr)] = big.NewInt(p)
	}
	return keys, addrs, set
}

func TestHasTwoThirdsPowerLockedRejectsExactlyTwoThirds(t *testing.T) {
	tests := []struct {
		name      string
		total     int64
		power     int64
		expectMet bool
	}{
		{"three equal validators, two signed", 3, 2, false},
		{"three equal validators, all signed", 3, 3, true},
		{"six equal validators, four signed", 6, 4, false},
		{"six equal validators, five signed", 6, 5, true},
		{"nine equal validators, six signed", 9, 6, false},
		{"nine equal validators, seven signed", 9, 7, true},
		{"single validator", 1, 1, true},
		{"two equal validators, one signed", 2, 1, false},
		{"two equal validators, both signed", 2, 2, true},
		{"total not a multiple of three is unchanged, one short", 4, 2, false},
		{"total not a multiple of three is unchanged", 4, 3, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			engine := &Engine{
				totalVotingPower: big.NewInt(tt.total),
				receivedPower: map[VoteType]*big.Int{
					Prevote:   big.NewInt(tt.power),
					Precommit: big.NewInt(tt.power),
				},
			}
			if got := engine.hasTwoThirdsPowerLocked(Prevote); got != tt.expectMet {
				t.Fatalf("prevote: expected quorum=%t for total %d and power %d, got %t", tt.expectMet, tt.total, tt.power, got)
			}
			if got := engine.hasTwoThirdsPowerLocked(Precommit); got != tt.expectMet {
				t.Fatalf("precommit: expected quorum=%t for total %d and power %d, got %t", tt.expectMet, tt.total, tt.power, got)
			}
		})
	}
}

// TestCommitRequiresEveryValidatorOfThreeEqualValidators drives the real vote
// path (addVoteIfRelevant -> commit) with three equal validators: two
// precommits are exactly 2/3 of the power and must neither report a quorum
// nor allow a commit; the third precommit does.
func TestCommitRequiresEveryValidatorOfThreeEqualValidators(t *testing.T) {
	keys, addrs, weights := newValidatorFixture(t, 1, 1, 1)

	node := &trackingNode{validatorSet: weights}
	engine := NewEngine(node, keys[0], &recordingBroadcaster{})

	block := types.NewBlock(&types.BlockHeader{Height: 1, Validator: addrs[0]}, nil)
	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}

	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 0}
	engine.activeProposal = &SignedProposal{Proposal: &Proposal{Block: block, Round: 0}}
	engine.resetVoteTrackingLocked()
	engine.mu.Unlock()

	for i := 0; i < 2; i++ {
		_, reachedPrevote, reachedPrecommit := engine.addVoteIfRelevant(&SignedVote{
			Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Prevote, Height: 1},
			Validator: addrs[i],
		})
		if reachedPrevote || reachedPrecommit {
			t.Fatalf("SECURITY: %d of 3 equal validators (at most exactly 2/3) prevoted and reported a quorum", i+1)
		}
	}
	for i := 0; i < 2; i++ {
		_, _, reachedPrecommit := engine.addVoteIfRelevant(&SignedVote{
			Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
			Validator: addrs[i],
		})
		if reachedPrecommit {
			t.Fatalf("SECURITY: %d of 3 equal validators precommitted and reported a quorum", i+1)
		}
	}
	if engine.commit() {
		t.Fatalf("SECURITY: commit succeeded with exactly 2/3 of the voting power")
	}
	if len(node.committed) != 0 {
		t.Fatalf("SECURITY: a block was committed with exactly 2/3 of the voting power")
	}

	_, _, reachedPrecommit := engine.addVoteIfRelevant(&SignedVote{
		Vote:      &Vote{BlockHash: blockHash, Round: 0, Type: Precommit, Height: 1},
		Validator: addrs[2],
	})
	if !reachedPrecommit {
		t.Fatalf("expected the third precommit to reach quorum")
	}
	if !engine.commit() {
		t.Fatalf("expected commit to succeed once all three validators precommitted")
	}
	if len(node.committed) != 1 {
		t.Fatalf("expected exactly one committed block, got %d", len(node.committed))
	}
}

// TestVerifyPolkaProofLockedRejectsExactlyTwoThirds covers the second site the
// threshold was computed at: the Proof-of-Lock-Change check of a re-proposal.
func TestVerifyPolkaProofLockedRejectsExactlyTwoThirds(t *testing.T) {
	tests := []struct {
		name    string
		powers  []int64
		signers []int
		want    bool
	}{
		{"three equal validators, two signed", []int64{1, 1, 1}, []int{0, 1}, false},
		{"three equal validators, all signed", []int64{1, 1, 1}, []int{0, 1, 2}, true},
		{"six equal validators, four signed", []int64{1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3}, false},
		{"six equal validators, five signed", []int64{1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3, 4}, true},
		{"nine equal validators, six signed", []int64{1, 1, 1, 1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3, 4, 5}, false},
		{"nine equal validators, seven signed", []int64{1, 1, 1, 1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3, 4, 5, 6}, true},
		{"weighted set exactly two thirds", []int64{4, 1, 1}, []int{0}, false},
		{"weighted set above two thirds", []int64{4, 1, 1}, []int{0, 2}, true},
		{"four equal validators keep the 3-of-4 bar", []int64{1, 1, 1, 1}, []int{0, 1, 2}, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			keys, addrs, set := newValidatorFixture(t, tt.powers...)
			engine := NewEngine(&trackingNode{validatorSet: set}, keys[0], &recordingBroadcaster{})
			total := new(big.Int)
			for _, p := range set {
				total.Add(total, p)
			}
			engine.mu.Lock()
			engine.totalVotingPower = total
			engine.validatorSet = set
			engine.mu.Unlock()

			block := candidateBlock(1, 5, addrs[0], 0x55)
			hash, _ := block.Header.Hash()
			proof := make([]*SignedVote, 0, len(tt.signers))
			for _, idx := range tt.signers {
				proof = append(proof, signPrevote(t, keys[idx], addrs[idx], hash, 2, 1))
			}

			engine.mu.RLock()
			got := engine.verifyPolkaProofLocked(proof, 2, hash, 1)
			engine.mu.RUnlock()
			if got != tt.want {
				t.Fatalf("verifyPolkaProofLocked = %t, want %t", got, tt.want)
			}
		})
	}
}
