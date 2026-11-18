package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// PL-BC-B2: the per-day rows of the market-wide fill record. One small row per UTC day, written only from the pool's
// activation height, bucketed by the fill's own (block header) time, exact rational prices.

func seedFillAt(t *testing.T, node *Node, tag byte, createdAt int64, znhb, nhb int64) {
	t.Helper()
	seller := crypto.MustNewAddress(crypto.NHBPrefix, []byte("seller-address-20-by"))
	buyer := crypto.MustNewAddress(crypto.NHBPrefix, []byte("buyer-address-20-byt"))
	if err := node.WithState(func(manager *nhbstate.Manager) error {
		fill := &market.Fill{Buyer: buyer, Seller: seller, ZNHBAmount: big.NewInt(znhb), NHBAmount: big.NewInt(nhb), FeeAmount: big.NewInt(0), CreatedAt: createdAt}
		fill.ID = [32]byte{0xD0, tag, byte(createdAt), byte(createdAt >> 8), byte(createdAt >> 16), byte(createdAt >> 24)}
		if err := manager.AppendMarketFill(fill); err != nil {
			return err
		}
		return manager.AppendMarketFillGlobal(fill)
	}); err != nil {
		t.Fatal(err)
	}
}

func rowPrice(num, den *big.Int) *big.Rat { return new(big.Rat).SetFrac(num, den) }

func TestMarketDayRowFollowsTheFillsOfOneDay(t *testing.T) {
	sp := p2pScenario(t, AmmSettings{ActivationHeight: 5, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)})
	rows, err := nhbstate.NewManager(sp.Trie).ListMarketDayRows(5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("two fills in one day make one row, got %d (%v)", len(rows), err)
	}
	row := rows[0]
	// fill 1: 150 ZNHB for 150 NHB (price 1.0); fill 2 a minute later: 100 ZNHB for 50 NHB (price 0.5)
	if row.Day != uint64(marketDataTestTime/86_400) {
		t.Fatalf("day %d, want %d", row.Day, marketDataTestTime/86_400)
	}
	if rowPrice(row.OpenNum, row.OpenDen).Cmp(big.NewRat(1, 1)) != 0 || rowPrice(row.HighNum, row.HighDen).Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("open and high are the first fill's price 1.0: %s %s", rowPrice(row.OpenNum, row.OpenDen), rowPrice(row.HighNum, row.HighDen))
	}
	if rowPrice(row.LowNum, row.LowDen).Cmp(big.NewRat(1, 2)) != 0 || rowPrice(row.CloseNum, row.CloseDen).Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("low and close are the second fill's price 0.5: %s %s", rowPrice(row.LowNum, row.LowDen), rowPrice(row.CloseNum, row.CloseDen))
	}
	if row.VolumeZnhb.Cmp(big.NewInt(250)) != 0 || row.VolumeNhb.Cmp(big.NewInt(200)) != 0 || row.Count != 2 {
		t.Fatalf("volume %s ZNHB / %s NHB over %d fills, want 250 / 200 over 2", row.VolumeZnhb, row.VolumeNhb, row.Count)
	}
}

func TestMarketDayRowsAreNotWrittenWithThePoolOffOrBeforeItsHeight(t *testing.T) {
	day := uint64(marketDataTestTime / 86_400)
	for name, settings := range map[string]AmmSettings{
		"off":     {},
		"pending": {ActivationHeight: 1000, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)},
	} {
		sp := p2pScenario(t, settings)
		manager := nhbstate.NewManager(sp.Trie)
		if rows, err := manager.ListMarketDayRows(5); err != nil || len(rows) != 0 {
			t.Fatalf("%s: no day row may exist, got %d (%v)", name, len(rows), err)
		}
		if found, _ := manager.KVGet([]byte("market/fillDay/"+dayKey(day)), nil); found {
			t.Fatalf("%s: the day key must not exist", name)
		}
		if found, _ := manager.KVGet([]byte("market/fillDayLast"), nil); found {
			t.Fatalf("%s: the last-day key must not exist", name)
		}
	}
}

func dayKey(n uint64) string { return new(big.Int).SetUint64(n).String() }

func TestMarketDayRowsRollOverAtMidnightAndWalkBack(t *testing.T) {
	node := newTestNode(t)
	const D = 20_000                               // a day number
	seedFillAt(t, node, 1, (D+1)*86_400-1, 10, 20) // the last second of day D, price 2
	seedFillAt(t, node, 2, (D+1)*86_400, 10, 30)   // the first second of day D+1, price 3
	seedFillAt(t, node, 3, (D+3)*86_400+5, 10, 40) // day D+3 (D+2 is quiet), price 4
	seedFillAt(t, node, 4, (D+3)*86_400+10, 10, 10)

	manager := func() *nhbstate.Manager {
		var m *nhbstate.Manager
		_ = node.WithStateView(func(mm *nhbstate.Manager) error { m = mm; return nil })
		return m
	}()
	all, err := manager.ListMarketDayRows(10)
	if err != nil || len(all) != 3 {
		t.Fatalf("three days have fills, got %d (%v)", len(all), err)
	}
	if all[0].Day != D+3 || all[1].Day != D+1 || all[2].Day != D {
		t.Fatalf("newest day first and quiet days skipped: %d %d %d", all[0].Day, all[1].Day, all[2].Day)
	}
	if all[2].Count != 1 || all[1].Count != 1 {
		t.Fatalf("a fill one second before midnight belongs to the old day and one at midnight to the new: %d, %d", all[2].Count, all[1].Count)
	}
	last := all[0]
	if rowPrice(last.OpenNum, last.OpenDen).Cmp(big.NewRat(4, 1)) != 0 || rowPrice(last.HighNum, last.HighDen).Cmp(big.NewRat(4, 1)) != 0 ||
		rowPrice(last.LowNum, last.LowDen).Cmp(big.NewRat(1, 1)) != 0 || rowPrice(last.CloseNum, last.CloseDen).Cmp(big.NewRat(1, 1)) != 0 || last.Count != 2 {
		t.Fatalf("day D+3: open 4, high 4, low 1, close 1 over 2 fills, got %+v", last)
	}
	two, _ := manager.ListMarketDayRows(2)
	if len(two) != 2 || two[0].Day != D+3 || two[1].Day != D+1 {
		t.Fatalf("a limit of 2 returns the two newest days that have fills")
	}
	if none, _ := manager.ListMarketDayRows(0); len(none) != 0 {
		t.Fatalf("a limit of 0 returns nothing")
	}
}

func TestMarketDayRowsKeepExactPrices(t *testing.T) {
	node := newTestNode(t)
	const day = 21_000
	base := int64(day) * 86_400
	seedFillAt(t, node, 1, base+1, 3, 1) // 1/3
	// a price a hair below 1/3: 333333333333333333 / 10^18 (a float would call these equal)
	seedFillAt(t, node, 2, base+2, 1_000_000_000_000_000_000, 333_333_333_333_333_333)
	seedFillAt(t, node, 3, base+3, 6, 2) // exactly 1/3 again
	var rows []*nhbstate.MarketDayRow
	_ = node.WithStateView(func(m *nhbstate.Manager) error { rows, _ = m.ListMarketDayRows(1); return nil })
	if len(rows) != 1 {
		t.Fatal("one day expected")
	}
	row := rows[0]
	smaller := new(big.Rat).SetFrac(big.NewInt(333_333_333_333_333_333), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	if rowPrice(row.LowNum, row.LowDen).Cmp(smaller) != 0 {
		t.Fatalf("the low must be the exact smaller price, got %s", rowPrice(row.LowNum, row.LowDen).FloatString(24))
	}
	if rowPrice(row.HighNum, row.HighDen).Cmp(big.NewRat(1, 3)) != 0 {
		t.Fatalf("the high is exactly 1/3, got %s", rowPrice(row.HighNum, row.HighDen).FloatString(24))
	}
	if rowPrice(row.CloseNum, row.CloseDen).Cmp(big.NewRat(1, 3)) != 0 || rowPrice(row.OpenNum, row.OpenDen).Cmp(big.NewRat(1, 3)) != 0 {
		t.Fatal("open and close are the first and last fills' prices")
	}
}

func TestMarketDayRowsIgnoreAFillWithNoPriceOrNoTime(t *testing.T) {
	node := newTestNode(t)
	seedFillAt(t, node, 1, 21_000*86_400, 0, 5) // nothing sold
	seedFillAt(t, node, 2, 21_000*86_400, 5, 0) // nothing paid
	// Before the epoch: such a fill cannot be stored at all, but the day fold must still refuse one handed to it directly.
	if err := node.WithState(func(manager *nhbstate.Manager) error {
		bad := &market.Fill{ZNHBAmount: big.NewInt(5), NHBAmount: big.NewInt(5), CreatedAt: -5}
		bad.ID = [32]byte{0xD0, 9}
		return manager.AppendMarketFillGlobal(bad)
	}); err != nil {
		t.Fatal(err)
	}
	var rows []*nhbstate.MarketDayRow
	var seq uint64
	_ = node.WithStateView(func(m *nhbstate.Manager) error {
		rows, _ = m.ListMarketDayRows(5)
		seq, _ = m.MarketFillSequence()
		return nil
	})
	if len(rows) != 0 {
		t.Fatalf("a fill with no price or no time is not part of any range, got %d rows", len(rows))
	}
	if seq != 3 {
		t.Fatalf("the sequence still numbers them all: %d", seq)
	}
}

func TestNodeMarketDailyStats(t *testing.T) {
	node := newTestNode(t)
	for i := int64(0); i < 100; i++ { // 100 consecutive days with one fill each, price 1/2
		seedFillAt(t, node, byte(i), (20_000+i)*86_400+7, 4, 2)
	}
	week, err := node.MarketDailyStats(0)
	if err != nil || len(week) != 7 {
		t.Fatalf("the default is 7 days: got %d (%v)", len(week), err)
	}
	if week[0].DayStart != (20_000+99)*86_400 || week[6].DayStart != (20_000+93)*86_400 {
		t.Fatalf("newest day first, each stamped with its midnight: %d .. %d", week[0].DayStart, week[6].DayStart)
	}
	d := week[0]
	if d.Open != "0.500000000000000000" || d.High != d.Open || d.Low != d.Open || d.Close != d.Open || d.Fills != 1 ||
		d.VolumeZnhb.Cmp(big.NewInt(4)) != 0 || d.VolumeNhb.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("day row wrong: %+v", d)
	}
	if many, _ := node.MarketDailyStats(1000); len(many) != 90 {
		t.Fatalf("the request is capped at 90 days, got %d", len(many))
	}
	if three, _ := node.MarketDailyStats(3); len(three) != 3 {
		t.Fatalf("a request for 3 days returns 3, got %d", len(three))
	}
	if marketDayPrice(nil, big.NewInt(1)) != "" || marketDayPrice(big.NewInt(1), big.NewInt(0)) != "" || marketDayPrice(big.NewInt(-1), big.NewInt(1)) != "" || marketDayPrice(big.NewInt(0), big.NewInt(1)) != "" {
		t.Fatal("a missing or non-positive price is the empty string")
	}
}
