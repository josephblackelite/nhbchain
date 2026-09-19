package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
)

// TestCreateBlockStaleLendingRefPriceIsPrunedNotAborting is the lending
// counterpart of TestCreateBlockStaleEpochBuybackRefPriceIsPrunedNotAborting.
// Lending reference prices carry no epoch; their monotonic guard is the
// timestamp (a submission must be strictly newer than the last accepted
// one). Two submissions can both pass admission while neither is committed;
// once the newer one lands, the older one can never apply again, but it used
// to fail with a bare error that fell through to proposalDispositionAbort --
// aborting every block build and never leaving the mempool, exactly like the
// stale buyback epoch.
func TestCreateBlockStaleLendingRefPriceIsPrunedNotAborting(t *testing.T) {
	proposer, validator, key := newLendingRefPriceConsensusHarness(t)

	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	submit := func(ts uint64) {
		t.Helper()
		rp := &lendingoracle.ReferencePrice{
			Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
			Timestamp: time.Unix(int64(ts), 0).UTC(),
		}
		if _, err := proposer.SubmitLendingRefPrice(rateNum, rateDenom, ts, [][]byte{signLendingRefPrice(t, rp, key)}); err != nil {
			t.Fatalf("submit lending ref price (ts=%d): %v", ts, err)
		}
	}
	// Both are admitted: admission simulates against committed state, which
	// has no lending reference price yet.
	submit(1_800_000_000)
	submit(1_800_000_001)

	proposer.mempoolMu.Lock()
	if len(proposer.mempool) != 2 {
		proposer.mempoolMu.Unlock()
		t.Fatalf("expected both submissions pending, got %d", len(proposer.mempool))
	}
	newer := proposer.mempool[1]
	proposer.mempoolMu.Unlock()

	// The block is produced with only the NEWER one (the other was not yet
	// seen by the proposer, or the proposal was already built without it).
	block2, err := proposer.CreateBlock([]*types.Transaction{newer})
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("expected the newer ref price in block 2, got %d txs", len(block2.Transactions))
	}
	commitBlockOnBoth(t, proposer, validator, block2)

	if !mempoolContainsType(proposer, types.TxTypeLendingRefPrice) {
		t.Fatalf("test setup: the older ref price must still be pending")
	}

	pending := proposer.GetMempool()
	if len(pending) != 1 {
		t.Fatalf("expected exactly the older ref price pending, got %d txs", len(pending))
	}
	block3, err := proposer.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal because of a superseded lending ref price: %v", err)
	}
	if len(block3.Transactions) != 0 {
		t.Fatalf("expected an empty block 3, got %d txs", len(block3.Transactions))
	}
	if mempoolContainsType(proposer, types.TxTypeLendingRefPrice) {
		t.Fatalf("the superseded lending ref price can never apply again and must be pruned from the mempool")
	}
	commitBlockOnBoth(t, proposer, validator, block3)

	if next := proposer.GetMempool(); len(next) != 0 {
		t.Fatalf("the pruned transaction must not be offered again, got %d txs", len(next))
	}
	status, err := proposer.LendingRefPriceStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.HasRefPrice || status.Timestamp != 1_800_000_001 {
		t.Fatalf("expected the newer price (ts 1800000001) on file, got %+v", status)
	}
}
