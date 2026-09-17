package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
)

// newGovernanceConsensusHarness builds two independent Nodes (each its own
// MemDB, mirroring two separate validators), both configured with an
// identical governance policy and a shared, externally-advanceable fixed
// time source -- mirroring newBuybackConsensusHarness/
// newLendingRefPriceConsensusHarness's own pattern. QuorumBps/
// PassThresholdBps are deliberately 0 so Finalize can transition a proposal
// straight to Passed with zero votes cast: this test's job is to prove the
// propose/finalize/queue/execute transaction pipeline itself reaches
// identical state roots across two validators (the actual bug this fix
// closes -- see core/governance_tx.go's doc comments), not to re-exercise
// POTSO-weighted vote tallying, which native/governance/engine_test.go
// already covers at the engine level.
func newGovernanceConsensusHarness(t *testing.T) (proposer, validator *Node, now *time.Time) {
	t.Helper()

	fixedTime := time.Unix(1_800_000_000, 0).UTC()
	now = &fixedTime

	policy := governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     100,
		AllowedParams:       []string{"fees.baseFee"},
		QuorumBps:           0,
		PassThresholdBps:    0,
	}

	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		validatorKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate node validator key: %v", err)
		}
		node, err := NewNode(db, validatorKey, "", true, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		node.SetGovernancePolicy(policy)
		node.SetTimeSource(func() time.Time { return *now })
		return node
	}

	proposer = build()
	validator = build()
	return proposer, validator, now
}

// mineIdenticalBlock drives one CreateBlock(pending mempool) ->
// ValidateBlock -> CommitBlock cycle across both nodes and fails the test if
// their post-commit state roots ever disagree -- the exact check that
// failed in production for TxTypeBuybackRefPrice.
func mineIdenticalBlock(t *testing.T, proposerNode, validatorNode *Node) {
	t.Helper()
	block, err := proposerNode.CreateBlock(append([]*types.Transaction(nil), proposerNode.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := proposerNode.CommitBlock(block); err != nil {
		t.Fatalf("proposer commit block: %v", err)
	}
	if err := validatorNode.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected proposer's block: %v", err)
	}
	if err := validatorNode.CommitBlock(block); err != nil {
		t.Fatalf("validator commit block: %v", err)
	}
}

func signedGovTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, txType types.TxType, payload interface{}) *types.Transaction {
	t.Helper()
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode governance payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign governance tx: %v", err)
	}
	return tx
}

// TestGovernanceProposalLifecycle_ProposerAndValidatorAgree drives the real
// CreateBlock/ValidateBlock/CommitBlock production code paths for a full
// propose -> finalize -> queue -> execute cycle, entirely via real signed
// transactions (TxTypeGovPropose/Finalize/Queue/Execute) -- proving two
// independently constructed nodes derive the same state root at every step,
// and that the final param.update payload lands identically on both. This
// is the direct regression test for the bug this whole change fixes:
// Node.GovernancePropose/Vote/Finalize/Queue/Execute used to mutate each
// validator's local trie directly, outside of consensus, which would have
// silently diverged the moment more than one validator existed.
func TestGovernanceProposalLifecycle_ProposerAndValidatorAgree(t *testing.T) {
	proposerNode, validatorNode, now := newGovernanceConsensusHarness(t)

	// Every signing key below is used exactly once, with no pre-funding --
	// sp.getAccount lazily zero-initialises a never-seen address (no error,
	// no explicit creation step needed), and every tx here has GasPrice 0
	// and (for propose) Deposit 0, so a zero balance is sufficient. This
	// deliberately avoids any out-of-band account seeding: a direct
	// WithState write on only one node -- or even on both nodes,
	// independently, before block 1 -- still diverges pending state from
	// committed state and gets wiped by resetDriftUnlessSelfProposedLocked
	// the moment that node validates a block it did not itself propose
	// (confirmed empirically while writing this test: seeding accounts
	// this way made even the height-1 empty block fail to validate). See
	// buyback_consensus_test.go's doc comment for the original diagnosis of
	// this pitfall -- genesis-file funding is the only safe workaround, and
	// this test avoids needing it entirely by relying on lazy zero-init.
	proposerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate proposer key: %v", err)
	}

	// Height 1: empty block, so height 2+ has committed headers to build on.
	mineIdenticalBlock(t, proposerNode, validatorNode)

	// Propose.
	proposeTx := signedGovTx(t, proposerKey, 0, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindParamUpdate,
		Payload: `{"fees.baseFee":1000}`,
		Deposit: big.NewInt(0),
	})
	if err := proposerNode.AddTransaction(proposeTx); err != nil {
		t.Fatalf("add propose tx: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposals, _, err := proposerNode.GovernanceListProposals(0, 10)
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d", len(proposals))
	}
	proposalID := proposals[0].ID
	if proposals[0].Status != governance.ProposalStatusVotingPeriod {
		t.Fatalf("expected VotingPeriod status, got %v", proposals[0].Status)
	}

	// Advance past VotingEnd (VotingPeriodSeconds: 100) on both nodes
	// identically, then finalize -- with QuorumBps/PassThresholdBps both 0,
	// zero votes cast is sufficient to pass.
	*now = now.Add(200 * time.Second)

	finalizeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate finalize key: %v", err)
	}

	finalizeTx := signedGovTx(t, finalizeKey, 0, types.TxTypeGovFinalize, govProposalIDPayload{ProposalID: proposalID})
	if err := proposerNode.AddTransaction(finalizeTx); err != nil {
		t.Fatalf("add finalize tx: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposal, ok, err := proposerNode.GovernanceProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("load proposal after finalize: ok=%v err=%v", ok, err)
	}
	if proposal.Status != governance.ProposalStatusPassed {
		t.Fatalf("expected Passed status after finalize, got %v", proposal.Status)
	}

	// Queue.
	queueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate queue key: %v", err)
	}

	queueTx := signedGovTx(t, queueKey, 0, types.TxTypeGovQueue, govProposalIDPayload{ProposalID: proposalID})
	if err := proposerNode.AddTransaction(queueTx); err != nil {
		t.Fatalf("add queue tx: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposal, ok, err = proposerNode.GovernanceProposal(proposalID)
	if err != nil || !ok || !proposal.Queued {
		t.Fatalf("expected proposal queued after queue tx: ok=%v queued=%v err=%v", ok, proposal != nil && proposal.Queued, err)
	}

	// Advance past TimelockEnd (TimelockSeconds: 100) on both nodes, then execute.
	*now = now.Add(200 * time.Second)

	executeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate execute key: %v", err)
	}

	executeTx := signedGovTx(t, executeKey, 0, types.TxTypeGovExecute, govProposalIDPayload{ProposalID: proposalID})
	if err := proposerNode.AddTransaction(executeTx); err != nil {
		t.Fatalf("add execute tx: %v", err)
	}
	mineIdenticalBlock(t, proposerNode, validatorNode)

	proposal, ok, err = proposerNode.GovernanceProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("load proposal after execute: ok=%v err=%v", ok, err)
	}
	if proposal.Status != governance.ProposalStatusExecuted {
		t.Fatalf("expected Executed status, got %v", proposal.Status)
	}

	// Confirm the param.update payload actually landed, identically, in
	// BOTH nodes' own param stores -- not just that the proposal object
	// says "Executed".
	for _, node := range []*Node{proposerNode, validatorNode} {
		var stored []byte
		var found bool
		if err := node.WithState(func(m *nhbstate.Manager) error {
			var err error
			stored, found, err = m.ParamStoreGet("fees.baseFee")
			return err
		}); err != nil {
			t.Fatalf("read fees.baseFee param: %v", err)
		}
		if !found {
			t.Fatalf("expected fees.baseFee to be set")
		}
		if string(stored) != "1000" {
			t.Fatalf("expected fees.baseFee == 1000, got %q", string(stored))
		}
	}
}
