package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// TestQuotedMinimumValidatorStakeProposalDoesNotWedgeTheChain is the DC-02
// round trip through the real pipeline: a param.update proposal whose value is
// a quoted decimal string passes proposal validation and is stored verbatim,
// and afterwards blocks that write accounts still build and validate
// identically on two independent validators. Before the fix every account
// write failed once such a proposal executed, because setAccount reads the
// stored value as a bare integer.
func TestQuotedMinimumValidatorStakeProposalDoesNotWedgeTheChain(t *testing.T) {
	proposerNode, validatorNode, now := newGovernanceConsensusHarness(t)
	policy := governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     100,
		AllowedParams:       []string{"fees.baseFee", governance.ParamKeyMinimumValidatorStake},
	}
	proposerNode.SetGovernancePolicy(policy)
	validatorNode.SetGovernancePolicy(policy)

	const quoted = `"20000000000000000000000"`
	const payload = `{"staking.minimumValidatorStake":` + quoted + `}`

	proposerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate proposer key: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposeTx := signedGovTx(t, proposerKey, 0, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindParamUpdate,
		Payload: payload,
		Deposit: big.NewInt(0),
	})
	if err := proposerNode.AddTransaction(proposeTx); err != nil {
		t.Fatalf("add propose tx: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposals, _, err := proposerNode.GovernanceListProposals(0, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d (err %v)", len(proposals), err)
	}
	proposalID := proposals[0].ID

	*now = now.Add(200 * time.Second)
	step := func(txType types.TxType) {
		t.Helper()
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		tx := signedGovTx(t, key, 0, txType, govProposalIDPayload{ProposalID: proposalID})
		if err := proposerNode.AddTransaction(tx); err != nil {
			t.Fatalf("add tx type %d: %v", txType, err)
		}
		mineIdenticalBlock(t, proposerNode, validatorNode)
	}
	step(types.TxTypeGovFinalize)
	step(types.TxTypeGovQueue)
	*now = now.Add(200 * time.Second)
	step(types.TxTypeGovExecute)

	proposal, ok, err := proposerNode.GovernanceProposal(proposalID)
	if err != nil || !ok || proposal.Status != governance.ProposalStatusExecuted {
		t.Fatalf("expected the proposal to execute, ok=%v err=%v", ok, err)
	}

	// The proposer's exact bytes are what is stored, on both validators, and
	// both read them as the amount they spell.
	want, _ := new(big.Int).SetString("20000000000000000000000", 10)
	for _, node := range []*Node{proposerNode, validatorNode} {
		var stored []byte
		if err := node.WithState(func(m *nhbstate.Manager) error {
			var err error
			stored, _, err = m.ParamStoreGet(governance.ParamKeyMinimumValidatorStake)
			return err
		}); err != nil {
			t.Fatalf("read param: %v", err)
		}
		if string(stored) != quoted {
			t.Fatalf("stored bytes = %q, want the proposer's exact %q", stored, quoted)
		}
		node.stateMu.Lock()
		got, err := node.state.minimumValidatorStake()
		node.stateMu.Unlock()
		if err != nil || got.Cmp(want) != 0 {
			t.Fatalf("minimumValidatorStake = %v, %v; want %s", got, err, want)
		}
	}

	// Every later transaction writes its sender's account. Two more blocks,
	// each carrying such a transaction, must still build and validate.
	for i := 0; i < 2; i++ {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		tx := signedGovTx(t, key, 0, types.TxTypeGovPropose, govProposePayload{
			Kind:    governance.ProposalKindParamUpdate,
			Payload: `{"fees.baseFee":7}`,
			Deposit: big.NewInt(0),
		})
		if err := proposerNode.AddTransaction(tx); err != nil {
			t.Fatalf("add post-update tx %d: %v", i, err)
		}
		block, err := proposerNode.CreateBlock(append([]*types.Transaction(nil), proposerNode.mempool...))
		if err != nil {
			t.Fatalf("create post-update block %d: %v", i, err)
		}
		if len(block.Transactions) != 1 {
			t.Fatalf("post-update block %d carries %d transactions, want 1 (the account write must not fail)", i, len(block.Transactions))
		}
		if err := proposerNode.CommitBlock(block); err != nil {
			t.Fatalf("proposer commit post-update block %d: %v", i, err)
		}
		if err := validatorNode.ValidateBlock(block); err != nil {
			t.Fatalf("validator rejected post-update block %d: %v", i, err)
		}
		if err := validatorNode.CommitBlock(block); err != nil {
			t.Fatalf("validator commit post-update block %d: %v", i, err)
		}
	}
}
