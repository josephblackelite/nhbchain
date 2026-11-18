package core

import "testing"

// PL-BC-B2: a swap beyond the scan cap whose block time cannot be read is not evidence that the window is partial.
func TestAmmTickerPartialWindowNeedsAKnownBlockTime(t *testing.T) {
	f, _, _ := tickerFixture(t)
	defer func(old int) { ammTickerScanCap = old }(ammTickerScanCap)
	ammTickerScanCap = 1
	// "Now" is second 100, so the window starts before second 0; only the newest swap's block time is known (second 50).
	timeAt := func(h uint64) (int64, bool) { return 50, h == 300 }
	view, err := f.sp.ammTicker(300, 100, timeAt)
	if err != nil {
		t.Fatal(err)
	}
	if view.Trades24h != 1 {
		t.Fatalf("the newest swap is inside the window: counted %d, want 1", view.Trades24h)
	}
	if view.Truncated {
		t.Fatalf("the next swap's block time is unknown, so nothing says the window is partial")
	}
	// The same cap with the next swap's time known and inside the window is partial.
	known := func(h uint64) (int64, bool) { return 50, true }
	view, _ = f.sp.ammTicker(300, 100, known)
	if !view.Truncated {
		t.Fatalf("a next swap known to be inside the window makes the 24 hour numbers partial")
	}
}

// The ladder reports the impact of a swap that sells ZNHB (the average price is below the spot price) as a positive number.
func TestAmmTickerImpactOfASellIsPositive(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	view, err := f.sp.ammTicker(200, marketDataTestTime, func(uint64) (int64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ { // the four ZNHB-to-NHB rows
		if view.Ladder[i].ImpactBps == 0 {
			t.Fatalf("selling row %d must report an impact of at least the fee (30 bps), got 0", i)
		}
	}
}
