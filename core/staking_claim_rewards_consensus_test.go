package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestStakeClaimRewardsBlock_ProposerAndValidatorAgree drives the real
// CreateBlock/ValidateBlock/CommitBlock production code paths across two
// blocks containing real, validly-signed transactions -- a TxTypeStake
// delegation followed, a payout period later, by a TxTypeStakeClaimRewards
// claim -- proving two independently constructed nodes derive the same
// state root at every step. This is the direct regression test for the bug
// this fix closes: rpc/stake_handlers.go's handleStakeClaimRewards used to
// call s.node.StakeClaimRewards(addr) directly under n.stateMu.Lock(),
// mutating only the handling validator's own local trie completely outside
// CreateBlock/ValidateBlock/CommitBlock -- invisible to any other validator
// and guaranteed to eventually diverge state roots. The claim transaction
// deliberately lands with a zero StakeShares balance (a first-ever
// delegation never populates it -- see the comment below) and no other
// action having advanced the staking global index in between, so it mints
// zero new ZNHB and needs no treasury funding -- the assertions instead
// confirm the claim's own bookkeeping (StakeLastPayoutTs advancing, the
// nonce being consumed) landed identically on both nodes, which is exactly
// the part the old direct-write RPC path could never guarantee.
func TestStakeClaimRewardsBlock_ProposerAndValidatorAgree(t *testing.T) {
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	ownerAddr := toAddress(ownerKey)
	ownerAddrStr := ownerKey.PubKey().Address().String()

	// The owner's ZNHB balance must come from genesis, not a direct
	// post-construction trie write -- a mutation applied identically to both
	// nodes still diverges pending state from committed state, and gets
	// silently wiped the first time a node validates a block it did not
	// itself propose (see potso_stake_consensus_test.go's identical comment
	// for the original diagnosis).
	genesisValidatorKeyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key A: %v", err)
	}
	genesisValidatorKeyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key B: %v", err)
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: genesisValidatorKeyA.PubKey().Address().String(), Power: 11440},
			{Address: genesisValidatorKeyB.PubKey().Address().String(), Power: 11336},
		},
		Alloc: map[string]map[string]string{
			ownerAddrStr: {"NHB": "0", "ZNHB": "2000"},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}

	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		validatorKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate node validator key: %v", err)
		}
		node, err := NewNode(db, validatorKey, genesisPath, false, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		return node
	}
	proposer := build()
	validator := build()

	// --- Block 1: self-delegate via a real TxTypeStake transaction. ---
	block1Time := time.Unix(1_750_000_000, 0).UTC()
	proposer.SetTimeSource(func() time.Time { return block1Time })
	t.Cleanup(func() { proposer.SetTimeSource(nil) })

	stakeTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    0,
		Value:    big.NewInt(1_000),
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := stakeTx.Sign(ownerKey.PrivateKey); err != nil {
		t.Fatalf("sign stake tx: %v", err)
	}
	if err := proposer.AddTransaction(stakeTx); err != nil {
		t.Fatalf("add stake tx: %v", err)
	}

	block1, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	if len(block1.Transactions) != 1 {
		t.Fatalf("expected the stake tx to survive into block 1, got %d txs", len(block1.Transactions))
	}
	if err := validator.ValidateBlock(block1); err != nil {
		t.Fatalf("validator rejected block 1: %v", err)
	}
	if err := proposer.CommitBlock(block1); err != nil {
		t.Fatalf("proposer commit block 1: %v", err)
	}
	if err := validator.CommitBlock(block1); err != nil {
		t.Fatalf("validator commit block 1: %v", err)
	}

	postStakeAccount, err := proposer.GetAccount(ownerAddr[:])
	if err != nil {
		t.Fatalf("load post-stake account: %v", err)
	}
	// Note: a first-ever delegation does NOT populate StakeShares -- that
	// field only accrues on a later accrual event, proportional to the
	// index movement since the account's last touch (accrueStakeAccount /
	// accrueStakeAccountWithBasis in core/state_transition.go), and there is
	// none yet the very block a position is opened. account.Stake (the
	// principal) is what confirms the delegation itself landed.
	if postStakeAccount.Stake == nil || postStakeAccount.Stake.Sign() <= 0 {
		t.Fatalf("expected positive stake principal after delegation, got %v", postStakeAccount.Stake)
	}
	nextNonce := postStakeAccount.Nonce

	// --- Block 2, one payout period later: claim rewards via a real
	// TxTypeStakeClaimRewards transaction. ---
	block2Time := block1Time.Add(31 * 24 * time.Hour)
	proposer.SetTimeSource(func() time.Time { return block2Time })

	claimTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStakeClaimRewards,
		Nonce:    nextNonce,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := claimTx.Sign(ownerKey.PrivateKey); err != nil {
		t.Fatalf("sign claim tx: %v", err)
	}
	if err := proposer.AddTransaction(claimTx); err != nil {
		t.Fatalf("add claim tx: %v", err)
	}

	block2, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("expected the claim tx to survive into block 2, got %d txs", len(block2.Transactions))
	}
	if err := validator.ValidateBlock(block2); err != nil {
		t.Fatalf("validator rejected block 2: %v", err)
	}
	if err := proposer.CommitBlock(block2); err != nil {
		t.Fatalf("proposer commit block 2: %v", err)
	}
	if err := validator.CommitBlock(block2); err != nil {
		t.Fatalf("validator commit block 2: %v", err)
	}

	// ValidateBlock/CommitBlock above already fail loudly on any state-root
	// mismatch (see the other consensus regression tests added this
	// session) -- this final check confirms the claim itself actually landed
	// identically on both nodes: the sender's nonce was consumed (proving
	// the transaction cannot be replayed) and StakeLastPayoutTs advanced to
	// the claim's block timestamp.
	for _, node := range []*Node{proposer, validator} {
		account, err := node.GetAccount(ownerAddr[:])
		if err != nil {
			t.Fatalf("load post-claim account: %v", err)
		}
		if account.Nonce != nextNonce+1 {
			t.Fatalf("nonce = %d, want %d", account.Nonce, nextNonce+1)
		}
		if account.StakeLastPayoutTs != uint64(block2Time.Unix()) {
			t.Fatalf("StakeLastPayoutTs = %d, want %d", account.StakeLastPayoutTs, uint64(block2Time.Unix()))
		}
	}
}
