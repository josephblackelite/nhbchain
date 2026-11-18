package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// PL-BC-B2: two more edges: a fill exactly at the start of the window, and the limit rules of the recent-fills read.

func TestMarketTickerFillAtTheWindowStartIsInside(t *testing.T) {
	sp := p2pScenario(t, AmmSettings{ActivationHeight: 5, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	manager := nhbstate.NewManager(sp.Trie)
	view := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	// The first fill was made at marketDataTestTime; a window that starts at that second still holds it.
	if err := fillMarketTicker(manager, view, marketDataTestTime); err != nil {
		t.Fatal(err)
	}
	if view.Fills24h != 2 {
		t.Fatalf("a fill made exactly at the start of the window is inside it: counted %d, want 2", view.Fills24h)
	}
	view = &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if err := fillMarketTicker(manager, view, marketDataTestTime+1); err != nil {
		t.Fatal(err)
	}
	if view.Fills24h != 1 {
		t.Fatalf("a fill one second before the window starts is outside it: counted %d, want 1", view.Fills24h)
	}
}

func TestNodeMarketRecentFillsLimits(t *testing.T) {
	node := newTestNode(t)
	seller := crypto.MustNewAddress(crypto.NHBPrefix, []byte("seller-address-20-by"))
	buyer := crypto.MustNewAddress(crypto.NHBPrefix, []byte("buyer-address-20-byt"))
	if err := node.WithState(func(manager *nhbstate.Manager) error {
		for i := 1; i <= 205; i++ {
			fill := &market.Fill{Buyer: buyer, Seller: seller, ZNHBAmount: big.NewInt(int64(i)), NHBAmount: big.NewInt(int64(i)), FeeAmount: big.NewInt(0), CreatedAt: int64(i)}
			fill.ID = [32]byte{byte(i >> 8), byte(i)}
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
	for _, c := range []struct{ ask, want int }{{0, 50}, {-7, 50}, {3, 3}, {200, 200}, {201, 200}, {1000, 200}} {
		fills, err := node.MarketRecentFills(c.ask)
		if err != nil {
			t.Fatal(err)
		}
		if len(fills) != c.want {
			t.Fatalf("a request for %d fills returned %d, want %d", c.ask, len(fills), c.want)
		}
	}
	fills, _ := node.MarketRecentFills(3)
	if fills[0].ZNHBAmount.Cmp(big.NewInt(205)) != 0 || fills[2].ZNHBAmount.Cmp(big.NewInt(203)) != 0 {
		t.Fatalf("the newest fill comes first: %s .. %s", fills[0].ZNHBAmount, fills[2].ZNHBAmount)
	}
}
