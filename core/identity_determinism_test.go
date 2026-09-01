package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestRegisterIdentityIsDeterministicAcrossValidatorClocks is an independent
// regression test for an externally-reported finding (bug bounty submission
// NHB-TRIAGE-H10, not formally submitted for a bounty but present in the
// same triage file as NHB-TRIAGE-C4/C7): Manager.IdentitySetAlias used to
// call time.Now() unconditionally and stamp the result into the alias
// record's CreatedAt/UpdatedAt fields, which are RLP-encoded and written
// into the consensus state trie. applyRegisterIdentity (the only on-chain
// path to it, dispatched from every ordinary, permissionless
// TxTypeRegisterIdentity transaction) called it with no way to override
// that clock. Two validators applying the identical RegisterIdentity
// transaction, in the identical block, even a second apart on their own
// wall clocks (an entirely ordinary amount of real-world skew/propagation
// delay, not a contrived edge case) would persist different bytes for the
// same alias record and therefore compute different state roots -- a
// deterministic-execution failure that would reject the block on every
// validator that disagreed, structurally identical to the already-fixed
// 2026-08-14 rewards_logic.go production incident (commit c25b418).
//
// This test proves the fix through the real, full consensus path -- two
// independent Node instances (independent DB, independent trie, standing in
// for two independent validator processes) each commit the SAME block
// (identical header, identical timestamp field, identical signed
// transaction bytes) with a real wall-clock gap in between, and their
// resulting state roots are compared directly -- exactly the comparison
// core/node.go's ValidateBlock performs in production
// ("state root mismatch").
func TestRegisterIdentityIsDeterministicAcrossValidatorClocks(t *testing.T) {
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	sender := userKey.PubKey().Address().Bytes()

	newValidator := func() *Node {
		t.Helper()
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		node, err := NewNode(db, validatorKey, "", true, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		account := &types.Account{
			BalanceNHB:  big.NewInt(0),
			BalanceZNHB: big.NewInt(0),
			Stake:       big.NewInt(0),
		}
		if err := node.state.setAccount(sender, account); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		return node
	}

	validatorA := newValidator()
	validatorB := newValidator()

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("SameSecondOrNot"),
	}
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	// One shared header, shared by both "validators" -- identical
	// Timestamp field, exactly as a real BFT round would produce (the
	// proposer picks one timestamp, every validator commits that same
	// header). Only each node's own wall clock is allowed to differ.
	// CommitBlock enforces a tolerance window against real wall-clock "now"
	// (independent of this bug), so this must be close to actual now rather
	// than an arbitrary fixed constant -- what matters for this test is that
	// it is the SAME value handed to both validators, not that it's a
	// historical constant.
	fixedBlockTime := time.Now().UTC()
	header := &types.BlockHeader{
		Height:    1,
		Timestamp: fixedBlockTime.Unix(),
		PrevHash:  validatorA.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	blockForA := types.NewBlock(header, []*types.Transaction{tx})
	blockForB := types.NewBlock(header, []*types.Transaction{tx})

	if err := validatorA.CommitBlock(blockForA); err != nil {
		t.Fatalf("validator A commit: %v", err)
	}

	// The real-world condition this bug depended on: a validator whose
	// local wall clock reaches this point a meaningfully different moment
	// than the other. Pre-fix, this alone was enough to diverge the state
	// root; post-fix, both must apply the transaction using the block's own
	// timestamp, not their own clock, so this sleep must have zero effect
	// on the result.
	time.Sleep(1100 * time.Millisecond)

	if err := validatorB.CommitBlock(blockForB); err != nil {
		t.Fatalf("validator B commit: %v", err)
	}

	rootA := validatorA.state.CurrentRoot()
	rootB := validatorB.state.CurrentRoot()
	if rootA != rootB {
		t.Fatalf("SECURITY: two validators committing the identical block a second apart on their own wall clocks computed different state roots (A=%s B=%s) -- this is exactly the condition that halts a real network with a \"state root mismatch\" rejection", rootA.Hex(), rootB.Hex())
	}
}
