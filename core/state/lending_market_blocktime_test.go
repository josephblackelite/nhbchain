package state

import (
	"math/big"
	"testing"

	"nhbchain/native/lending"
)

// A block time, once recorded, survives the round trip through the store, and a
// record written before the field existed reads back with none.
func TestLendingMarketBlockTimeRoundTrips(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)
	market := &lending.Market{
		PoolID:              "default",
		TotalNHBSupplied:    big.NewInt(10),
		TotalSupplyShares:   big.NewInt(10),
		TotalNHBBorrowed:    big.NewInt(4),
		SupplyIndex:         big.NewInt(1),
		BorrowIndex:         big.NewInt(1),
		LastUpdateBlock:     9,
		LastUpdateTimestamp: 1_800_000_000,
	}
	if err := m.LendingPutMarket("default", market); err != nil {
		t.Fatalf("put market: %v", err)
	}
	got, ok, err := m.LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("get market: ok=%v err=%v", ok, err)
	}
	if got.LastUpdateTimestamp != 1_800_000_000 || got.LastUpdateBlock != 9 {
		t.Fatalf("round trip returned block %d time %d", got.LastUpdateBlock, got.LastUpdateTimestamp)
	}

	old := legacyStoredLendingMarket{PoolID: "old", TotalNHBSupplied: big.NewInt(1), TotalSupplyShares: big.NewInt(1), TotalNHBBorrowed: big.NewInt(0), SupplyIndex: big.NewInt(1), BorrowIndex: big.NewInt(1), LastUpdateBlock: 5}
	if err := m.KVPut(lendingMarketKey("old"), old); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	read, ok, err := m.LendingGetMarket("old")
	if err != nil || !ok {
		t.Fatalf("read legacy record: ok=%v err=%v", ok, err)
	}
	if read.LastUpdateTimestamp != 0 || read.LastUpdateBlock != 5 {
		t.Fatalf("legacy record read back block %d time %d, want 5 and 0", read.LastUpdateBlock, read.LastUpdateTimestamp)
	}
}
