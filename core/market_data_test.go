package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-BC-B2: the market data the wallet's market page reads. The fill sequence exists only from the pool's activation
// height; the pool ticker and the peer-to-peer ticker report exact numbers from state and never write anything.

const marketDataTestTime = int64(1_800_000_000)

// p2pScenario runs two listings and two fills through the real apply path and returns the processor. Prices (NHB per
// ZNHB): listing A 1.0, listing B 0.5. Fill 1 buys 150 ZNHB of A, fill 2 buys 100 ZNHB of B.
func p2pScenario(t *testing.T, pool AmmSettings) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetAmm(pool)
	sp.BeginBlock(10, time.Unix(marketDataTestTime, 0).UTC())

	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	sellerAddr, buyerAddr := sellerKey.PubKey().Address().Bytes(), buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(5_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	buyerFunds := new(big.Int).Mul(big.NewInt(1000), big.NewInt(1_000000000000000000))
	if err := sp.setAccount(buyerAddr, &types.Account{BalanceNHB: buyerFunds, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}

	apply := func(tx *types.Transaction, key *crypto.PrivateKey) {
		t.Helper()
		if err := tx.Sign(key.PrivateKey); err != nil {
			t.Fatal(err)
		}
		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	apply(marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true), sellerKey)  // A: price 1.0
	apply(marketCreateListingTx(t, 1, big.NewInt(1000), big.NewInt(2), big.NewInt(1), true), sellerKey) // B: price 0.5
	listings, err := nhbstate.NewManager(sp.Trie).ListOpenMarketListings()
	if err != nil || len(listings) != 2 {
		t.Fatalf("expected two open listings, got %d (%v)", len(listings), err)
	}
	var idA, idB [32]byte
	for _, l := range listings {
		if l.RateNumerator.Cmp(big.NewInt(1)) == 0 {
			idA = l.ID
		} else {
			idB = l.ID
		}
	}
	apply(marketFillListingTx(t, 0, idA, big.NewInt(150)), buyerKey)
	sp.BeginBlock(11, time.Unix(marketDataTestTime+60, 0).UTC())
	apply(marketFillListingTx(t, 1, idB, big.NewInt(100)), buyerKey)
	return sp
}

func TestMarketFillIndexIsWrittenOnlyFromPoolActivation(t *testing.T) {
	off := p2pScenario(t, AmmSettings{})
	manager := nhbstate.NewManager(off.Trie)
	if seq, err := manager.MarketFillSequence(); err != nil || seq != 0 {
		t.Fatalf("with the pool off the fill sequence must stay empty, got %d (%v)", seq, err)
	}
	if found, err := manager.KVGet([]byte("market/fillSeq"), nil); err != nil || found {
		t.Fatalf("with the pool off the sequence key must not exist (found=%v, %v)", found, err)
	}
	if found, err := manager.KVGet([]byte("market/fillAt/1"), nil); err != nil || found {
		t.Fatalf("with the pool off no fill may be indexed (found=%v, %v)", found, err)
	}

	// Configured but not yet reached: still nothing.
	pending := p2pScenario(t, AmmSettings{ActivationHeight: 1000, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	if seq, _ := nhbstate.NewManager(pending.Trie).MarketFillSequence(); seq != 0 {
		t.Fatalf("before the activation height the sequence must stay empty, got %d", seq)
	}

	on := p2pScenario(t, AmmSettings{ActivationHeight: 5, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	m := nhbstate.NewManager(on.Trie)
	if seq, err := m.MarketFillSequence(); err != nil || seq != 2 {
		t.Fatalf("from the activation height both fills are indexed, got %d (%v)", seq, err)
	}
	fills, err := m.ListRecentMarketFills(10)
	if err != nil || len(fills) != 2 {
		t.Fatalf("recent fills: %d (%v)", len(fills), err)
	}
	if fills[0].ZNHBAmount.Cmp(big.NewInt(100)) != 0 || fills[1].ZNHBAmount.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("recent fills must be newest first (100 then 150 ZNHB), got %s then %s", fills[0].ZNHBAmount, fills[1].ZNHBAmount)
	}
	if limited, _ := m.ListRecentMarketFills(1); len(limited) != 1 || limited[0].ZNHBAmount.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("a limit of 1 returns only the newest fill")
	}
	if none, _ := m.ListRecentMarketFills(0); len(none) != 0 {
		t.Fatalf("a limit of 0 returns nothing")
	}
	// The existing per-buyer index is untouched by the new one.
	for _, f := range fills {
		var rawBuyer [20]byte
		copy(rawBuyer[:], f.Buyer.Bytes())
		byBuyer, err := m.ListMarketFillsByBuyer(rawBuyer)
		if err != nil || len(byBuyer) != 2 {
			t.Fatalf("the buyer index must still hold both fills, got %d (%v)", len(byBuyer), err)
		}
	}
}

func ratFromString(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("not a number: %q", s)
	}
	return r
}

func TestMarketTickerNumbers(t *testing.T) {
	sp := p2pScenario(t, AmmSettings{ActivationHeight: 5, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	manager := nhbstate.NewManager(sp.Trie)
	asOf := marketDataTestTime + 120

	view := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if err := fillMarketTicker(manager, view, asOf-ammTickerWindowSeconds); err != nil {
		t.Fatal(err)
	}
	if view.OpenListings != 2 || view.OpenZnhbWei.Cmp(big.NewInt(250+900)) != 0 {
		t.Fatalf("open listings %d, open ZNHB %s; want 2 and 1150", view.OpenListings, view.OpenZnhbWei)
	}
	if ratFromString(t, view.BestAsk).Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("best ask %s, want 0.5", view.BestAsk)
	}
	wantVWAP := new(big.Rat).SetFrac(big.NewInt(250+450), big.NewInt(1150)) // (250*1 + 900*0.5) / 1150
	if ratFromString(t, view.VWAPAsk).Cmp(wantVWAP) != 0 && view.VWAPAsk != wantVWAP.FloatString(18) {
		t.Fatalf("VWAP ask %s, want %s", view.VWAPAsk, wantVWAP.FloatString(18))
	}
	if view.FillCount != 2 || view.Fills24h != 2 || view.Truncated {
		t.Fatalf("fills %d, in window %d, truncated %v; want 2, 2, false", view.FillCount, view.Fills24h, view.Truncated)
	}
	if ratFromString(t, view.LastPrice).Cmp(big.NewRat(1, 2)) != 0 || view.LastFillTime != marketDataTestTime+60 || view.LastFillHeight != 11 {
		t.Fatalf("last fill %s at height %d time %d, want 0.5 at 11, %d", view.LastPrice, view.LastFillHeight, view.LastFillTime, marketDataTestTime+60)
	}
	if ratFromString(t, view.Open24h).Cmp(big.NewRat(1, 1)) != 0 || ratFromString(t, view.High24h).Cmp(big.NewRat(1, 1)) != 0 || ratFromString(t, view.Low24h).Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("open %s high %s low %s, want 1, 1, 0.5", view.Open24h, view.High24h, view.Low24h)
	}
	// Seller's NHB only (the buyer's flat fee is not part of the price): 150 at 1.0 and 100 at 0.5 = 150 + 50.
	if view.VolumeZnhb24h.Cmp(big.NewInt(250)) != 0 || view.VolumeNhb24h.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("volume %s ZNHB / %s NHB, want 250 / 200", view.VolumeZnhb24h, view.VolumeNhb24h)
	}

	// A window that starts after both fills: no 24 h numbers, but the last price is still the newest fill's.
	late := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if err := fillMarketTicker(manager, late, marketDataTestTime+1000); err != nil {
		t.Fatal(err)
	}
	if late.Fills24h != 0 || late.VolumeZnhb24h.Sign() != 0 || late.High24h != "" || late.Open24h != "" || late.Truncated {
		t.Fatalf("a window with no fills must report none: %+v", late)
	}
	if ratFromString(t, late.LastPrice).Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("the last price outlives the window, got %q", late.LastPrice)
	}

	// Nothing was written by reading.
	if seq, _ := manager.MarketFillSequence(); seq != 2 {
		t.Fatalf("reading changed the sequence: %d", seq)
	}
}

func TestMarketTickerWithAnEmptyBookAndNoFills(t *testing.T) {
	sp := newStakingStateProcessor(t)
	view := &MarketTickerView{OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if err := fillMarketTicker(nhbstate.NewManager(sp.Trie), view, 0); err != nil {
		t.Fatal(err)
	}
	if view.OpenListings != 0 || view.BestAsk != "" || view.VWAPAsk != "" || view.FillCount != 0 || view.LastPrice != "" || view.Truncated {
		t.Fatalf("an empty market must report nothing, got %+v", view)
	}
}

// tickerFixture is a pool with three swaps at known block times: height 200 is outside the 24 hour window, 210 and 300
// are inside it.
func tickerFixture(t *testing.T) (*bcFixture, func(uint64) (int64, bool), int64) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(10_000), big.NewInt(0)); err != nil {
		t.Fatal(err)
	}
	f.atHeight(210)
	if err := f.swap("bob", AmmDirNhbToZnhb, wei(100), big.NewInt(0)); err != nil {
		t.Fatal(err)
	}
	f.atHeight(300)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); err != nil {
		t.Fatal(err)
	}
	now := marketDataTestTime
	times := map[uint64]int64{200: now - 100_000, 210: now - 1_000, 300: now - 10}
	return f, func(h uint64) (int64, bool) { ts, ok := times[h]; return ts, ok }, now
}

func TestAmmTickerPriceRangeVolumeAndLadder(t *testing.T) {
	f, timeAt, now := tickerFixture(t)
	trades, err := f.sp.AmmTrades(10)
	if err != nil || len(trades) != 3 {
		t.Fatalf("trades: %d (%v)", len(trades), err)
	}
	p1 := ammTradePrice(trades[2].Direction, trades[2].AmountIn, trades[2].AmountOut) // height 200, outside the window
	p2 := ammTradePrice(trades[1].Direction, trades[1].AmountIn, trades[1].AmountOut) // 210
	p3 := ammTradePrice(trades[0].Direction, trades[0].AmountIn, trades[0].AmountOut) // 300
	if p1 == nil || p2 == nil || p3 == nil {
		t.Fatal("every swap must have a price")
	}

	view, err := f.sp.ammTicker(300, now, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastTradePrice != p3.FloatString(18) || view.LastTradeHeight != 300 || view.LastTradeTime != now-10 {
		t.Fatalf("last trade %s at %d/%d, want %s at 300/%d", view.LastTradePrice, view.LastTradeHeight, view.LastTradeTime, p3.FloatString(18), now-10)
	}
	if view.Trades24h != 2 || view.Truncated {
		t.Fatalf("trades in the window %d (truncated %v), want 2", view.Trades24h, view.Truncated)
	}
	if view.Open24h != p2.FloatString(18) {
		t.Fatalf("24 h open %s, want the oldest swap inside the window %s", view.Open24h, p2.FloatString(18))
	}
	hi, lo := p2, p3
	if p3.Cmp(p2) > 0 {
		hi, lo = p3, p2
	}
	if view.High24h != hi.FloatString(18) || view.Low24h != lo.FloatString(18) {
		t.Fatalf("24 h range %s..%s, want %s..%s", view.Low24h, view.High24h, lo.FloatString(18), hi.FloatString(18))
	}
	// Volume: swap 2 paid 100 NHB for its ZNHB out; swap 3 paid 1,000 ZNHB for its NHB out.
	wantZ := new(big.Int).Add(trades[1].AmountOut, trades[0].AmountIn)
	wantN := new(big.Int).Add(trades[1].AmountIn, trades[0].AmountOut)
	if view.VolumeZnhb24h.Cmp(wantZ) != 0 || view.VolumeNhb24h.Cmp(wantN) != 0 {
		t.Fatalf("volume %s ZNHB / %s NHB, want %s / %s", view.VolumeZnhb24h, view.VolumeNhb24h, wantZ, wantN)
	}
	if view.PriceNhbPerZnhb == "" || !view.SwapsOpen || view.TradeCount != 3 || view.AsOfHeight != 300 || view.AsOfTime != now {
		t.Fatalf("pool fields wrong: %+v", view)
	}

	// The ladder: eight rows, four sizes each way; each row is the quote the chain would give, and the impact only
	// grows with the size.
	if len(view.Ladder) != 8 {
		t.Fatalf("ladder rows %d, want 8", len(view.Ladder))
	}
	for i, row := range view.Ladder {
		quote, qerr := f.sp.AmmQuote(row.Direction, row.AmountIn)
		if qerr != nil || quote.Cmp(row.AmountOut) != 0 {
			t.Fatalf("row %d out %s, quote %s (%v)", i, row.AmountOut, quote, qerr)
		}
		if !row.Executable {
			t.Fatalf("row %d should be executable inside the cap and the floors: %+v", i, row)
		}
		if i%4 != 0 && row.ImpactBps < view.Ladder[i-1].ImpactBps {
			t.Fatalf("impact fell from %d to %d as the size grew (row %d)", view.Ladder[i-1].ImpactBps, row.ImpactBps, i)
		}
	}

	// Reading wrote nothing: the pool and the supply invariant are as before.
	f.checkInvariants("after the ticker reads")
}

func TestAmmTickerMarksSwapsBeyondTheCapAsNotExecutable(t *testing.T) {
	f, timeAt, now := tickerFixture(t)
	f.sp.amm.MaxSwapBps = 1 // 0.01 % of the reserve
	view, err := f.sp.ammTicker(300, now, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	var small, big1000 *AmmLadderRow
	for i := range view.Ladder {
		row := &view.Ladder[i]
		if row.Direction == AmmDirZnhbToNhb && row.AmountIn.Cmp(wei(1)) == 0 {
			small = row
		}
		if row.Direction == AmmDirZnhbToNhb && row.AmountIn.Cmp(wei(1000)) == 0 {
			big1000 = row
		}
	}
	if small == nil || big1000 == nil || !small.Executable || big1000.Executable {
		t.Fatalf("1 ZNHB must be executable and 1,000 ZNHB beyond a 0.01%% cap must not be: %+v %+v", small, big1000)
	}
}

func TestAmmTickerClosedPoolIsNotExecutable(t *testing.T) {
	f, timeAt, now := tickerFixture(t)
	f.sp.amm.SwapStartHeight = 0 // swaps closed at every height
	view, err := f.sp.ammTicker(300, now, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if view.SwapsOpen {
		t.Fatal("the pool must report its swaps closed")
	}
	for i, row := range view.Ladder {
		if row.Executable {
			t.Fatalf("row %d must not be executable while swaps are closed", i)
		}
	}
}

func TestAmmTickerScanCapAndUnknownTimes(t *testing.T) {
	f, timeAt, now := tickerFixture(t)
	defer func(old int) { ammTickerScanCap = old }(ammTickerScanCap)

	ammTickerScanCap = 1 // two swaps are inside the window, only one is walked
	view, err := f.sp.ammTicker(300, now, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || view.Trades24h != 1 {
		t.Fatalf("with a cap of 1 the 24 h numbers are partial: truncated %v, trades %d", view.Truncated, view.Trades24h)
	}

	ammTickerScanCap = 2 // the third swap is outside the window: the numbers are complete
	view, err = f.sp.ammTicker(300, now, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if view.Truncated || view.Trades24h != 2 {
		t.Fatalf("with a cap of 2 the window is fully covered: truncated %v, trades %d", view.Truncated, view.Trades24h)
	}

	ammTickerScanCap = ammTickerScanCapDefault
	view, err = f.sp.ammTicker(300, now, func(uint64) (int64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	if view.Trades24h != 0 || view.LastTradePrice == "" || view.LastTradeTime != 0 {
		t.Fatalf("with no block times the 24 h numbers are empty, the last price stays, its time is 0: %+v", view)
	}
}

func TestAmmTickerOnAnEmptyPool(t *testing.T) {
	f := newPoolFixture(t)
	view, err := f.sp.ammTicker(0, 0, func(uint64) (int64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	if view.PriceNhbPerZnhb != "" || view.LastTradePrice != "" || len(view.Ladder) != 0 || view.Trades24h != 0 {
		t.Fatalf("an empty pool reports no price, no trade and no ladder: %+v", view)
	}
}
