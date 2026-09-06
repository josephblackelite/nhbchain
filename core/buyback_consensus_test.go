package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// newBuybackConsensusHarness builds two independent Nodes (each its own
// MemDB, mirroring two separate validators) configured identically: same
// admin wallet address/starting ZNHB balance (via a shared genesis file --
// see the comment below on why this must come from genesis, not a
// post-construction test helper), same buyback signer set/threshold, same
// short epoch length, same fixed time source. This is the closest a unit
// test can get to reproducing the real two-validator propose/validate
// topology, using the actual CreateBlock/ValidateBlock production code
// paths rather than reimplemented logic.
//
// The admin wallet's funding specifically MUST come from genesis rather
// than ConfigureAdminWalletForTests: that helper writes the funded account
// straight into the trie as an uncommitted mutation, which works fine for
// every single-node test that only ever calls ProcessBlockLifecycle
// directly, but is silently wiped the first time a *second*, independently
// -constructed Node calls ValidateBlock/CommitBlock on a block it did not
// itself propose -- resetDriftUnlessSelfProposedLocked (core/node.go)
// resets any node's pending trie state back to its last COMMITTED root
// before validating a block that isn't its own, and an uncommitted test
// mutation has no committed root backing it. Buyback config and epoch
// length are safe to set post-construction (SetBuybackConfig/
// SetEpochConfig only assign in-memory StateProcessor fields, never touch
// the trie), so only the admin wallet needs the genesis-file treatment.
func newBuybackConsensusHarness(t *testing.T) (proposer, validator *Node, signerKeys []*crypto.PrivateKey, senderKey *crypto.PrivateKey) {
	t.Helper()

	keys := make([]*crypto.PrivateKey, 3)
	addrs := make([][20]byte, 3)
	for i := range keys {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate signer key %d: %v", i, err)
		}
		keys[i] = key
		copy(addrs[i][:], key.PubKey().Address().Bytes())
	}

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	adminAddrStr := adminKey.PubKey().Address().String()

	senderKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddrStr := senderKey.PubKey().Address().String()

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
			adminAddrStr:  {"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()},
			senderAddrStr: {"NHB": "1000000000000000000000", "ZNHB": "0"},
		},
		AdminWallet: adminAddrStr,
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}

	fixedTime := time.Unix(1_800_000_000, 0).UTC()

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
		if err := node.ConfigureEpochLengthForTests(2); err != nil {
			t.Fatalf("configure epoch length: %v", err)
		}
		if err := node.ConfigureBuybackForTests(buyback.Config{
			FeeShareBps:     2000,
			DiscountBps:     0,
			SafetyMarginBps: 0,
			SignerThreshold: 2,
			Signers:         addrs,
		}); err != nil {
			t.Fatalf("configure buyback: %v", err)
		}
		node.SetTimeSource(func() time.Time { return fixedTime })
		return node
	}

	proposer = build()
	validator = build()
	return proposer, validator, keys, senderKey
}

// TestBuybackRefPriceBlock_ProposerAndValidatorAgree is a direct
// reproduction attempt for the production incident where including a real
// signed BuybackRefPrice transaction in a block caused every validator to
// reject every proposal for that height with "state root mismatch" --
// confirmed live on both validators, every BFT round, for ~5 minutes,
// halting the chain. This drives the actual CreateBlock (proposer) and
// ValidateBlock (receiving validator) production code paths -- not the
// isolated AddTransaction-simulation path exercised by
// TestSubmitBuybackRefPrice_ValidBundleAccepted, which never touches
// CreateBlock/ValidateBlock at all and is why this shipped undetected.
func TestBuybackRefPriceBlock_ProposerAndValidatorAgree(t *testing.T) {
	proposer, validator, keys, _ := newBuybackConsensusHarness(t)

	// Height 1: empty block, advance both nodes identically so height 2
	// (the epoch boundary, given epoch length 2) is reachable.
	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	if err := proposer.CommitBlock(block1); err != nil {
		t.Fatalf("proposer commit block 1: %v", err)
	}
	if err := validator.ValidateBlock(block1); err != nil {
		t.Fatalf("validator reject block 1: %v", err)
	}
	if err := validator.CommitBlock(block1); err != nil {
		t.Fatalf("validator commit block 1: %v", err)
	}

	// Height 2 is the epoch boundary (epoch length 2, so epoch 1 spans
	// heights 1-2) -- submit a real, validly-signed reference price for
	// the currently-open epoch, exactly like buybackd does in production.
	epochNum, ok := proposer.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	if epochNum != 1 {
		t.Fatalf("expected height 2 to still be epoch 1 (boundary height), got epoch %d", epochNum)
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(1_800_000_000)
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signRefPrice(t, keys[0], rp),
		signRefPrice(t, keys[1], rp),
	}
	if _, err := proposer.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs); err != nil {
		t.Fatalf("submit ref price: %v", err)
	}

	mempool := proposer.GetMempool()
	if len(mempool) != 1 {
		t.Fatalf("expected exactly 1 mempool tx, got %d", len(mempool))
	}

	block2, err := proposer.CreateBlock(mempool)
	if err != nil {
		t.Fatalf("create block 2 (with buyback ref price tx): %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("expected the buyback tx to survive into the proposed block, got %d txs", len(block2.Transactions))
	}

	// This is the exact check that failed in production: an independent
	// validator re-applying the SAME block's transactions must derive the
	// SAME state root the proposer declared in the header.
	if err := validator.ValidateBlock(block2); err != nil {
		t.Fatalf("validator rejected proposer's block 2: %v", err)
	}

	// The proposer must also be able to commit its own block cleanly.
	if err := proposer.CommitBlock(block2); err != nil {
		t.Fatalf("proposer commit block 2: %v", err)
	}
	if err := validator.CommitBlock(block2); err != nil {
		t.Fatalf("validator commit block 2: %v", err)
	}
}

// TestBuybackRefPriceBlock_MixedWithTransfer_ProposerAndValidatorAgree closes
// the specific gap left open by TestBuybackRefPriceBlock_ProposerAndValidatorAgree:
// task #138 (2026-08-13) halted the chain on a real block, but the fix
// commit (b0e097b) shipped that night without ever confirming the real
// trigger, and the only regression test written for it exercises a block
// containing a buyback ref-price tx in isolation -- never a block mixing it
// with any other transaction type, which is the untested condition the
// original incident (and this suspect) is actually about. This test drives
// the same real CreateBlock/ValidateBlock production path as the isolation
// test, but with a plain signed NHB transfer included in the SAME block as
// the buyback ref-price tx, and additionally compares the two nodes'
// resulting state roots directly (belt-and-braces alongside ValidateBlock's
// own internal check).
func TestBuybackRefPriceBlock_MixedWithTransfer_ProposerAndValidatorAgree(t *testing.T) {
	proposer, validator, keys, senderKey := newBuybackConsensusHarness(t)

	// Height 1: empty block, advance both nodes identically so height 2
	// (the epoch boundary, given epoch length 2) is reachable.
	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	if err := proposer.CommitBlock(block1); err != nil {
		t.Fatalf("proposer commit block 1: %v", err)
	}
	if err := validator.ValidateBlock(block1); err != nil {
		t.Fatalf("validator reject block 1: %v", err)
	}
	if err := validator.CommitBlock(block1); err != nil {
		t.Fatalf("validator commit block 1: %v", err)
	}

	epochNum, ok := proposer.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	if epochNum != 1 {
		t.Fatalf("expected height 2 to still be epoch 1 (boundary height), got epoch %d", epochNum)
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(1_800_000_000)
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signRefPrice(t, keys[0], rp),
		signRefPrice(t, keys[1], rp),
	}
	if _, err := proposer.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs); err != nil {
		t.Fatalf("submit ref price: %v", err)
	}

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	transferTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(1_000),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := transferTx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := proposer.AddTransaction(transferTx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	mempool := proposer.GetMempool()
	if len(mempool) != 2 {
		t.Fatalf("expected exactly 2 mempool txs (buyback ref-price + transfer), got %d", len(mempool))
	}

	block2, err := proposer.CreateBlock(mempool)
	if err != nil {
		t.Fatalf("create block 2 (mixed buyback ref-price + transfer): %v", err)
	}
	if len(block2.Transactions) != 2 {
		t.Fatalf("expected both txs to survive into the proposed block, got %d", len(block2.Transactions))
	}

	// This is the exact check that failed in production: an independent
	// validator re-applying the SAME block's transactions must derive the
	// SAME state root the proposer declared in the header.
	if err := validator.ValidateBlock(block2); err != nil {
		t.Fatalf("validator rejected proposer's mixed block 2: %v", err)
	}

	if err := proposer.CommitBlock(block2); err != nil {
		t.Fatalf("proposer commit block 2: %v", err)
	}
	if err := validator.CommitBlock(block2); err != nil {
		t.Fatalf("validator commit block 2: %v", err)
	}

	rootProposer := proposer.state.CurrentRoot()
	rootValidator := validator.state.CurrentRoot()
	if rootProposer != rootValidator {
		t.Fatalf("SECURITY: proposer and validator diverged on a block mixing a buyback ref-price tx with a plain transfer (proposer=%s validator=%s) -- exactly the state-root-mismatch condition that halted the chain on 2026-08-13", rootProposer.Hex(), rootValidator.Hex())
	}
}

