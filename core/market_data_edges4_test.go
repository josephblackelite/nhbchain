package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// PL-BC-B2: the join of swaps to their block times, and a partial peer-to-peer window that the walk has already left.

func TestFillAmmTradeTimesJoinsEachSwapToItsBlock(t *testing.T) {
	trades := []AmmTradeView{{ID: 3, Height: 300}, {ID: 2, Height: 210}, {ID: 1, Height: 200}}
	times := map[uint64]int64{300: 1_800_000_000, 200: 1_799_000_000} // 210's block cannot be read
	fillAmmTradeTimes(trades, func(h uint64) (int64, bool) { ts, ok := times[h]; return ts, ok })
	if trades[0].Time != 1_800_000_000 || trades[1].Time != 0 || trades[2].Time != 1_799_000_000 {
		t.Fatalf("each swap takes its own block's time and an unreadable block leaves 0: %+v", trades)
	}
}

func TestMarketTickerPartialWindowIsOnlyReportedWhenTheWalkNeverLeftIt(t *testing.T) {
	node := newTestNode(t)
	seller := crypto.MustNewAddress(crypto.NHBPrefix, []byte("seller-address-20-by"))
	buyer := crypto.MustNewAddress(crypto.NHBPrefix, []byte("buyer-address-20-byt"))
	if err := node.WithState(func(manager *nhbstate.Manager) error {
		for i, at := range []int64{100, 200, 300} { // three fills, oldest first
			fill := &market.Fill{Buyer: buyer, Seller: seller, ZNHBAmount: big.NewInt(10), NHBAmount: big.NewInt(10), FeeAmount: big.NewInt(0), CreatedAt: at}
			fill.ID = [32]byte{0xE0, byte(i)}
			if err := manager.AppendMarketFill(fill); err != nil {
				return err
			}
			if err := manager.AppendMarketFillGlobal(fill); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer func(old uint64) { marketTickerScanCap = old }(marketTickerScanCap)
	marketTickerScanCap = 2 // two of the three fills are walked
	run := func(windowStart int64) *MarketTickerView {
		view := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
		if err := node.WithStateView(func(m *nhbstate.Manager) error { return fillMarketTicker(m, view, windowStart) }); err != nil {
			t.Fatal(err)
		}
		return view
	}
	// Every fill is inside the window, so the third one (not walked) may be inside it too: partial.
	if v := run(50); !v.Truncated || v.Fills24h != 2 {
		t.Fatalf("all walked fills are inside the window and one was not walked: %+v", v)
	}
	// The walk reaches the fill at 200, which is before the window starts at 250: the unwalked fill is older still, so the numbers are complete.
	if v := run(250); v.Truncated || v.Fills24h != 1 {
		t.Fatalf("the walk left the window, so nothing newer was missed: %+v", v)
	}
}
