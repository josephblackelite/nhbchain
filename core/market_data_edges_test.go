package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// PL-BC-B2: the edges a first deliberate-breakage run showed untested (window boundary, unknown block times, exact
// ladder sizes and impact, the cap and floor boundaries, listings that hold nothing, the scan cap, the index flag).

func TestAmmTickerWindowBoundaryIsExact(t *testing.T) {
	f, _, now := tickerFixture(t)
	// 86,400 seconds written out, not the constant under test: a test that reads the window from the code it tests cannot see the window change.
	times := map[uint64]int64{200: now - 86_400 - 1, 210: now - 86_400, 300: now - 10}
	view, err := f.sp.ammTicker(300, now, func(h uint64) (int64, bool) { ts, ok := times[h]; return ts, ok })
	if err != nil {
		t.Fatal(err)
	}
	if view.Trades24h != 2 {
		t.Fatalf("a swap exactly 24 hours old is inside the window and one a second older is not: counted %d, want 2", view.Trades24h)
	}
}

func TestAmmTickerUnknownBlockTimeEndsTheWalkEvenAtClockZero(t *testing.T) {
	f, _, _ := tickerFixture(t)
	view, err := f.sp.ammTicker(0, 0, func(uint64) (int64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	if view.Trades24h != 0 || view.VolumeZnhb24h.Sign() != 0 {
		t.Fatalf("a swap whose block time is unknown must never be counted, even when 'now' is 0: %d trades", view.Trades24h)
	}
}

func TestAmmTradePriceNeedsPositiveAmountsAndIsNhbPerZnhb(t *testing.T) {
	for _, c := range []struct {
		dir     uint8
		in, out int64
	}{{AmmDirZnhbToNhb, 0, 5}, {AmmDirZnhbToNhb, 5, 0}, {AmmDirNhbToZnhb, 0, 5}, {AmmDirNhbToZnhb, 5, 0}, {AmmDirZnhbToNhb, -1, 5}, {AmmDirNhbToZnhb, 5, -1}} {
		if p := ammTradePrice(c.dir, big.NewInt(c.in), big.NewInt(c.out)); p != nil {
			t.Fatalf("dir %d in %d out %d has no price, got %s", c.dir, c.in, c.out, p.FloatString(6))
		}
	}
	if p := ammTradePrice(AmmDirZnhbToNhb, big.NewInt(100), big.NewInt(5)); p == nil || p.Cmp(big.NewRat(1, 20)) != 0 {
		t.Fatalf("100 ZNHB in, 5 NHB out is 0.05 NHB per ZNHB, got %v", p)
	}
	if p := ammTradePrice(AmmDirNhbToZnhb, big.NewInt(5), big.NewInt(100)); p == nil || p.Cmp(big.NewRat(1, 20)) != 0 {
		t.Fatalf("5 NHB in, 100 ZNHB out is 0.05 NHB per ZNHB, got %v", p)
	}
	if ratString(nil) != "" {
		t.Fatal("no price is the empty string")
	}
}

func TestAmmTickerLadderSizesPricesAndImpactAreExact(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	view, err := f.sp.ammTicker(200, marketDataTestTime, func(uint64) (int64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := f.sp.AmmPool()
	spot := new(big.Rat).SetFrac(pool.ReserveNhbWei, pool.ReserveZnhbWei)
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	if len(view.Ladder) != 8 {
		t.Fatalf("ladder rows %d", len(view.Ladder))
	}
	for i, row := range view.Ladder {
		wantDir := AmmDirZnhbToNhb
		if i >= 4 {
			wantDir = AmmDirNhbToZnhb
		}
		wantIn := new(big.Int).Mul(big.NewInt([]int64{1, 10, 100, 1000}[i%4]), unit)
		if row.Direction != wantDir || row.AmountIn.Cmp(wantIn) != 0 {
			t.Fatalf("row %d is direction %d size %s, want direction %d size %s", i, row.Direction, row.AmountIn, wantDir, wantIn)
		}
		quote, err := f.sp.AmmQuote(row.Direction, row.AmountIn)
		if err != nil {
			t.Fatal(err)
		}
		avg := new(big.Rat).SetFrac(quote, row.AmountIn) // NHB per ZNHB when selling ZNHB
		if row.Direction == AmmDirNhbToZnhb {
			avg = new(big.Rat).SetFrac(row.AmountIn, quote)
		}
		if row.AvgPrice != avg.FloatString(18) {
			t.Fatalf("row %d average price %s, want %s", i, row.AvgPrice, avg.FloatString(18))
		}
		gap := new(big.Rat).Sub(spot, avg)
		gap.Abs(gap)
		bps := new(big.Rat).Quo(new(big.Rat).Mul(gap, big.NewRat(10_000, 1)), spot)
		want := new(big.Int).Quo(bps.Num(), bps.Denom()).Uint64()
		if row.ImpactBps != want {
			t.Fatalf("row %d impact %d bps, want %d", i, row.ImpactBps, want)
		}
	}
	// A 0.30 % fee alone is 30 basis points, so the smallest swap of each kind sits at or just above 30.
	if view.Ladder[0].ImpactBps < 30 || view.Ladder[0].ImpactBps > 31 || view.Ladder[4].ImpactBps < 30 || view.Ladder[4].ImpactBps > 31 {
		t.Fatalf("the smallest swaps should cost about the 30 bps fee: %d and %d", view.Ladder[0].ImpactBps, view.Ladder[4].ImpactBps)
	}
}

func TestAmmTickerExecutableAtTheCapAndFloorBoundaries(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool() // 1,000,000 ZNHB against 50,000 NHB
	f.atHeight(200)
	timeAt := func(uint64) (int64, bool) { return 0, false }
	rowFor := func(view *AmmTickerView, dir uint8, whole int64) AmmLadderRow {
		for _, r := range view.Ladder {
			if r.Direction == dir && r.AmountIn.Cmp(wei(whole)) == 0 {
				return r
			}
		}
		t.Fatalf("no ladder row for direction %d size %d", dir, whole)
		return AmmLadderRow{}
	}

	// A cap of 0.10 % is exactly 1,000 ZNHB and 50 NHB: a swap exactly at the cap is accepted, one above is not.
	f.sp.amm.MaxSwapBps = 10
	view, err := f.sp.ammTicker(200, marketDataTestTime, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if !rowFor(view, AmmDirZnhbToNhb, 1000).Executable || !rowFor(view, AmmDirZnhbToNhb, 100).Executable {
		t.Fatal("1,000 ZNHB is exactly the cap and must be executable")
	}
	if rowFor(view, AmmDirNhbToZnhb, 100).Executable || !rowFor(view, AmmDirNhbToZnhb, 10).Executable {
		t.Fatal("against a 50 NHB cap, 100 NHB is refused and 10 NHB is accepted")
	}

	// The reserve floor: a swap may leave the NHB reserve exactly at its floor, never below it.
	f.sp.amm.MaxSwapBps = 500
	pool, _ := f.sp.AmmPool()
	quote, _ := f.sp.AmmQuote(AmmDirZnhbToNhb, wei(1))
	f.sp.amm.MinReserveNHB = new(big.Int).Sub(pool.ReserveNhbWei, quote)
	view, _ = f.sp.ammTicker(200, marketDataTestTime, timeAt)
	if !rowFor(view, AmmDirZnhbToNhb, 1).Executable {
		t.Fatal("a swap that leaves the reserve exactly at its floor is accepted")
	}
	f.sp.amm.MinReserveNHB = new(big.Int).Add(f.sp.amm.MinReserveNHB, big.NewInt(1))
	view, _ = f.sp.ammTicker(200, marketDataTestTime, timeAt)
	if rowFor(view, AmmDirZnhbToNhb, 1).Executable {
		t.Fatal("a swap that would leave the reserve one wei under its floor is refused")
	}
}

func TestMarketTickerIgnoresListingsThatHoldNothing(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	seller := crypto.MustNewAddress(crypto.NHBPrefix, []byte("seller-address-20-by"))
	put := func(tag byte, num, den, remaining int64) {
		l := &market.Listing{Seller: seller, RateNumerator: big.NewInt(num), RateDenominator: big.NewInt(den),
			TotalAmount: big.NewInt(1000), RemainingAmount: big.NewInt(remaining), AllowPartial: true, Status: market.ListingOpen, CreatedAt: 1, UpdatedAt: 1}
		l.ID = [32]byte{tag}
		if err := manager.PutMarketListing(l); err != nil {
			t.Fatal(err)
		}
		if err := manager.AppendOpenMarketListing(l.ID); err != nil {
			t.Fatal(err)
		}
	}
	put(1, 2, 1, 0)   // nothing left
	put(2, 0, 1, 50)  // a rate of zero
	put(3, 2, 0, 50)  // a denominator of zero
	put(4, 2, 1, 100) // the only real one: price 0.5
	view := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if err := fillMarketTicker(manager, view, 0); err != nil {
		t.Fatal(err)
	}
	if view.OpenListings != 1 || view.OpenZnhbWei.Cmp(big.NewInt(100)) != 0 || ratFromString(t, view.BestAsk).Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("only the listing that holds ZNHB at a real rate counts: %+v", view)
	}
}

func TestMarketTickerScanCapAndWindow(t *testing.T) {
	sp := p2pScenario(t, AmmSettings{ActivationHeight: 5, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	manager := nhbstate.NewManager(sp.Trie)
	defer func(old uint64) { marketTickerScanCap = old }(marketTickerScanCap)
	run := func(cap uint64, windowStart int64) *MarketTickerView {
		marketTickerScanCap = cap
		v := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
		if err := fillMarketTicker(manager, v, windowStart); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := run(1, 0); !v.Truncated || v.Fills24h != 1 || v.FillCount != 2 {
		t.Fatalf("a cap of 1 with two fills in the window is partial: %+v", v)
	}
	if v := run(2, 0); v.Truncated || v.Fills24h != 2 {
		t.Fatalf("a cap equal to the number of fills covers them all: %+v", v)
	}
	// A window that starts between the two fills: the older one is outside, so a full walk is complete.
	if v := run(2, marketDataTestTime+30); v.Truncated || v.Fills24h != 1 {
		t.Fatalf("the older fill is outside the window: %+v", v)
	}
	if v := run(marketTickerScanCapDefault, 0); v.Truncated {
		t.Fatalf("the default cap covers these fills: %+v", v)
	}
}

func TestNodeMarketTickerReportsWhetherTheFillIndexIsBeingWritten(t *testing.T) {
	node := newTestNode(t)
	off, err := node.MarketTicker()
	if err != nil {
		t.Fatal(err)
	}
	if off.IndexActive || off.IndexStartHeight != 0 || off.FillCount != 0 {
		t.Fatalf("with the pool off the index is not active: %+v", off)
	}
	configured := func(height uint64) *MarketTickerView {
		node.SetBridgeAndAmm(BridgeSettings{}, AmmSettings{ActivationHeight: height, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
		v, err := node.MarketTicker()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	next := off.AsOfHeight + 1 // the height of the next block, which is what the index would be written at
	if v := configured(next); !v.IndexActive || v.IndexStartHeight != next {
		t.Fatalf("an activation height equal to the next block's height is active: %+v", v)
	}
	if v := configured(next + 1); v.IndexActive || v.IndexStartHeight != next+1 {
		t.Fatalf("an activation height one block further is not active yet: %+v", v)
	}
}
