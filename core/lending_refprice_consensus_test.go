package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// newLendingRefPriceConsensusHarness builds two independent Nodes (each its
// own MemDB, mirroring two separate validators) configured identically: same
// buyback signer set/threshold (deliberately reused for lending's own ref
// price -- see applyLendingRefPriceTransaction's doc comment), same fixed
// time source. This exercises the exact code path that halted production
// consensus when a real signed TxTypeBuybackRefPrice was first included in a
// block (see TestBuybackRefPriceBlock_ProposerAndValidatorAgree) -- a
// distinct but structurally identical senderless, envelope-unsigned
// transaction type could plausibly hit the same class of bug, so this test
// exists before TxTypeLendingRefPrice is ever attempted live, not after.
func newLendingRefPriceConsensusHarness(t *testing.T) (proposer, validator *Node, signerKey *crypto.PrivateKey) {
	t.Helper()

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate signer key: %v", err)
	}
	var signerAddr [20]byte
	copy(signerAddr[:], key.PubKey().Address().Bytes())

	fixedTime := time.Unix(1_800_000_000, 0).UTC()

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
		if err := node.ConfigureBuybackForTests(buyback.Config{
			FeeShareBps:     2000,
			DiscountBps:     0,
			SafetyMarginBps: 0,
			SignerThreshold: 1,
			Signers:         [][20]byte{signerAddr},
		}); err != nil {
			t.Fatalf("configure buyback: %v", err)
		}
		node.SetTimeSource(func() time.Time { return fixedTime })
		return node
	}

	proposer = build()
	validator = build()
	return proposer, validator, key
}

// TestLendingRefPriceBlock_ProposerAndValidatorAgree drives the real
// CreateBlock (proposer) and ValidateBlock (receiving validator) production
// code paths for a block containing a real, validly-signed
// TxTypeLendingRefPrice transaction -- proving the two independently
// constructed nodes derive the same state root, the way
// TestBuybackRefPriceBlock_ProposerAndValidatorAgree proves it for
// TxTypeBuybackRefPrice.
func TestLendingRefPriceBlock_ProposerAndValidatorAgree(t *testing.T) {
	proposer, validator, key := newLendingRefPriceConsensusHarness(t)

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

	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(1_800_000_000)
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sig := signLendingRefPrice(t, rp, key)

	if _, err := proposer.SubmitLendingRefPrice(rateNum, rateDenom, ts, [][]byte{sig}); err != nil {
		t.Fatalf("submit lending ref price: %v", err)
	}

	mempool := proposer.GetMempool()
	if len(mempool) != 1 {
		t.Fatalf("expected exactly 1 mempool tx, got %d", len(mempool))
	}

	block2, err := proposer.CreateBlock(mempool)
	if err != nil {
		t.Fatalf("create block 2 (with lending ref price tx): %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("expected the lending ref price tx to survive into the proposed block, got %d txs", len(block2.Transactions))
	}

	// This is the exact check that failed in production for buyback's own
	// ref price: an independent validator re-applying the SAME block's
	// transactions must derive the SAME state root the proposer declared
	// in the header.
	if err := validator.ValidateBlock(block2); err != nil {
		t.Fatalf("validator rejected proposer's block 2: %v", err)
	}

	if err := proposer.CommitBlock(block2); err != nil {
		t.Fatalf("proposer commit block 2: %v", err)
	}
	if err := validator.CommitBlock(block2); err != nil {
		t.Fatalf("validator commit block 2: %v", err)
	}

	proposerStatus, err := proposer.LendingRefPriceStatus()
	if err != nil {
		t.Fatalf("proposer status: %v", err)
	}
	validatorStatus, err := validator.LendingRefPriceStatus()
	if err != nil {
		t.Fatalf("validator status: %v", err)
	}
	if !proposerStatus.HasRefPrice || !validatorStatus.HasRefPrice {
		t.Fatalf("expected both nodes to report a recorded reference price")
	}
	if proposerStatus.RateNum != validatorStatus.RateNum || proposerStatus.RateDenom != validatorStatus.RateDenom {
		t.Fatalf("proposer/validator disagree on recorded rate: %+v vs %+v", proposerStatus, validatorStatus)
	}
}

