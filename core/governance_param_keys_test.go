package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
)

// ambiguousParamPayload names fees.baseFee twice, under keys that differ only
// in a trailing space, with different values.
const ambiguousParamPayload = `{"fees.baseFee":25,"fees.baseFee ":30}`

func TestCloneGovernancePolicyKeepsAmbiguousKeyActivation(t *testing.T) {
	policy := governance.ProposalPolicy{
		MinDepositWei:                    big.NewInt(1),
		RejectAmbiguousParamKeysFromUnix: 1_900_000_000,
	}
	if got := cloneGovernancePolicy(policy).RejectAmbiguousParamKeysFromUnix; got != 1_900_000_000 {
		t.Fatalf("clone dropped the activation time: %d", got)
	}
	if got := cloneGovernancePolicy(governance.ProposalPolicy{}).RejectAmbiguousParamKeysFromUnix; got != 0 {
		t.Fatalf("expected the check to stay off by default, got %d", got)
	}
}

func TestNodeGovernancePolicyKeepsAmbiguousKeyActivation(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	node.SetGovernancePolicy(governance.ProposalPolicy{RejectAmbiguousParamKeysFromUnix: 1_900_000_000})
	if got := node.governancePolicy().RejectAmbiguousParamKeysFromUnix; got != 1_900_000_000 {
		t.Fatalf("node policy dropped the activation time: %d", got)
	}
	if got := node.state.govPolicy.RejectAmbiguousParamKeysFromUnix; got != 1_900_000_000 {
		t.Fatalf("state processor policy dropped the activation time: %d", got)
	}
}

// proposalCountAfterAmbiguousProposal mines one block holding a propose
// transaction whose payload repeats a parameter under whitespace-variant keys
// with different values, on two independent nodes, and returns how many
// proposals each node ends up with.
func proposalCountAfterAmbiguousProposal(t *testing.T, activation uint64) (proposerCount, validatorCount int) {
	t.Helper()
	proposerNode, validatorNode, _ := newGovernanceConsensusHarness(t)
	for _, node := range []*Node{proposerNode, validatorNode} {
		node.SetGovernancePolicy(governance.ProposalPolicy{
			MinDepositWei:                    big.NewInt(0),
			VotingPeriodSeconds:              100,
			TimelockSeconds:                  100,
			AllowedParams:                    []string{"fees.baseFee"},
			RejectAmbiguousParamKeysFromUnix: activation,
		})
	}

	proposerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate proposer key: %v", err)
	}
	// Height 1: empty block, so the proposal lands on committed headers.
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposeTx := signedGovTx(t, proposerKey, 0, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindParamUpdate,
		Payload: ambiguousParamPayload,
		Deposit: big.NewInt(0),
	})
	// Whether the transaction is refused at the pool or dropped while the block
	// is built, no proposal may exist afterwards on a node with the check on.
	_ = proposerNode.AddTransaction(proposeTx)
	mineIdenticalBlock(t, proposerNode, validatorNode)

	count := func(node *Node) int {
		proposals, _, err := node.GovernanceListProposals(0, 10)
		if err != nil {
			t.Fatalf("list proposals: %v", err)
		}
		return len(proposals)
	}
	return count(proposerNode), count(validatorNode)
}

func TestGovernanceProposeRefusesAmbiguousKeysOnceActive(t *testing.T) {
	// The harness block time is 1_800_000_000; an activation time at or below
	// it means the check is on for the block holding the proposal.
	proposerCount, validatorCount := proposalCountAfterAmbiguousProposal(t, 1_799_999_000)
	if proposerCount != 0 || validatorCount != 0 {
		t.Fatalf("expected no proposal with the check on, got proposer=%d validator=%d", proposerCount, validatorCount)
	}
}

func TestGovernanceProposeAdmitsAmbiguousKeysBeforeActivation(t *testing.T) {
	// With no activation time, or one still in the future, the proposal is
	// admitted as before, identically on both nodes.
	for _, activation := range []uint64{0, uint64(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix())} {
		proposerCount, validatorCount := proposalCountAfterAmbiguousProposal(t, activation)
		if proposerCount != 1 || validatorCount != 1 {
			t.Fatalf("activation=%d: expected the proposal admitted on both nodes, got proposer=%d validator=%d", activation, proposerCount, validatorCount)
		}
	}
}
