package stable

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"nhbchain/services/swapd/storage"
)

func mustAmountUnits(t *testing.T, amount float64) int64 {
	t.Helper()
	units, err := toAmountUnits(amount)
	if err != nil {
		t.Fatalf("amount quantisation failed: %v", err)
	}
	return units
}

func mustRateUnits(t *testing.T, rate float64) int64 {
	t.Helper()
	units, err := toRateUnits(rate)
	if err != nil {
		t.Fatalf("rate quantisation failed: %v", err)
	}
	return units
}

type testClock struct {
	now  time.Time
	step time.Duration
}

func newTestClock(base time.Time) *testClock {
	return &testClock{now: base, step: time.Second}
}

func (c *testClock) Now() time.Time {
	ts := c.now
	c.now = c.now.Add(c.step)
	return ts
}

func (c *testClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func buildTestEngine(t *testing.T, base time.Time, inventory int64, ttl time.Duration, limits Limits) (*Engine, *testClock) {
	t.Helper()
	asset := Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       ttl,
		MaxSlippageBps: 50,
		SoftInventory:  inventory,
	}
	engine, err := NewEngine([]Asset{asset}, limits, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	clock := newTestClock(base)
	engine.WithClock(clock.Now)
	engine.SetPriceMaxAge(24 * time.Hour)
	return engine, clock
}

func openTestStorage(t *testing.T) *storage.Storage {
	t.Helper()
	dsn := "file:swapd_engine_test?mode=memory&cache=shared"
	store, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestEnginePriceQuoteRequiresFreshOracle(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, clock := buildTestEngine(t, base, 1_000_000, time.Minute, Limits{})
	ctx := context.Background()
	if _, err := engine.PriceQuote(ctx, "ZNHB", 10); !errors.Is(err, ErrPriceUnavailable) {
		t.Fatalf("expected ErrPriceUnavailable, got %v", err)
	}
	engine.RecordPrice("ZNHB", "USD", 1.05, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 10)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if quote.Price != mustRateUnits(t, 1.05) {
		t.Fatalf("unexpected price units: got %d want %d", quote.Price, mustRateUnits(t, 1.05))
	}
	expiryDelta := quote.ExpiresAt.Sub(base)
	if expiryDelta < time.Minute || expiryDelta > time.Minute+10*time.Second {
		t.Fatalf("unexpected expiry delta: got %s", expiryDelta)
	}
	// Ensure subsequent quotes fail when the oracle sample ages out.
	clock.Advance(48 * time.Hour)
	if _, err := engine.PriceQuote(ctx, "ZNHB", 5); !errors.Is(err, ErrPriceUnavailable) {
		t.Fatalf("expected stale price error, got %v", err)
	}
}

func TestEngineReserveSlippageAndInventory(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 1_000, time.Minute, Limits{})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	engine.RecordPrice("ZNHB", "USD", 1.07, time.Time{})
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100); !errors.Is(err, ErrSlippageExceeded) {
		t.Fatalf("expected slippage error, got %v", err)
	}
	available, reserved, payouts, ok := engine.LedgerBalance("ZNHB")
	if !ok {
		t.Fatalf("ledger missing")
	}
	if available != mustAmountUnits(t, 1_000) || reserved != 0 || payouts != 0 {
		t.Fatalf("ledger mutated on slippage rejection: available=%d reserved=%d payouts=%d", available, reserved, payouts)
	}
	// Rewind price within tolerance and reserve successfully.
	engine.RecordPrice("ZNHB", "USD", 1.004, time.Time{})
	res, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	if res.AmountOut != mustAmountUnits(t, 100) {
		t.Fatalf("unexpected amount out units: got %d want %d", res.AmountOut, mustAmountUnits(t, 100))
	}
	available, reserved, _, _ = engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 900) {
		t.Fatalf("available mismatch after reserve: got %d", available)
	}
	if reserved != mustAmountUnits(t, 100) {
		t.Fatalf("reserved mismatch after reserve: got %d", reserved)
	}
}

func TestEngineDailyCapAndInsufficientInventory(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 150, time.Minute, Limits{DailyCap: 120})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 200)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 200); !errors.Is(err, ErrInsufficientReserve) {
		t.Fatalf("expected insufficient reserve, got %v", err)
	}
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err = engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100); err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	// A second reservation exceeding the cap should fail.
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err = engine.PriceQuote(ctx, "ZNHB", 50)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 50); !errors.Is(err, ErrDailyCapExceeded) {
		t.Fatalf("expected daily cap error, got %v", err)
	}
}

func TestEngineCashOutFlow(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, clock := buildTestEngine(t, base, 10_000, time.Minute, Limits{})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.25, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 80)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	res, err := engine.ReserveQuote(ctx, quote.ID, "merchant", 80)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	available, reserved, payouts, _ := engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 9_900) {
		t.Fatalf("available mismatch: got %d", available)
	}
	if reserved != mustAmountUnits(t, 100) {
		t.Fatalf("reserved mismatch: got %d", reserved)
	}
	if payouts != 0 {
		t.Fatalf("unexpected payouts: %d", payouts)
	}
	intent, err := engine.CreateCashOutIntent(ctx, res.QuoteID)
	if err != nil {
		t.Fatalf("cash out intent: %v", err)
	}
	if intent.Amount != 100.0 {
		t.Fatalf("unexpected intent amount: %.2f", intent.Amount)
	}
	available, reserved, payouts, _ = engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 9_900) {
		t.Fatalf("available after cashout mismatch: got %d", available)
	}
	if reserved != 0 {
		t.Fatalf("reserved after cashout mismatch: %d", reserved)
	}
	if payouts != mustAmountUnits(t, 100) {
		t.Fatalf("payouts mismatch: %d", payouts)
	}
	if _, err := engine.CreateCashOutIntent(ctx, res.QuoteID); !errors.Is(err, ErrReservationConsumed) {
		t.Fatalf("expected consumed reservation error, got %v", err)
	}
	// Ensure the daily cap counter reset via new quote once we advance to next day.
	clock.Advance(24 * time.Hour)
	engine.RecordPrice("ZNHB", "USD", 1.25, time.Time{})
	if _, err := engine.PriceQuote(ctx, "ZNHB", 10); err != nil {
		t.Fatalf("quote after advance: %v", err)
	}
}

func TestEngineReservationExpiryReleasesInventory(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	limits := Limits{DailyCap: 200}
	engine, clock := buildTestEngine(t, base, 5_000, 10*time.Second, limits)
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 150)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	res, err := engine.ReserveQuote(ctx, quote.ID, "merchant", 150)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	available, reserved, _, _ := engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 4_850) {
		t.Fatalf("available mismatch: %d", available)
	}
	if reserved != mustAmountUnits(t, 150) {
		t.Fatalf("reserved mismatch: %d", reserved)
	}
	clock.Advance(5 * time.Second)
	if _, err := engine.CreateCashOutIntent(ctx, res.QuoteID); !errors.Is(err, ErrReservationExpired) {
		t.Fatalf("expected reservation expired, got %v", err)
	}
	available, reserved, _, _ = engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 5_000) {
		t.Fatalf("available not restored: %d", available)
	}
	if reserved != 0 {
		t.Fatalf("reserved not released: %d", reserved)
	}
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err = engine.PriceQuote(ctx, "ZNHB", 150)
	if err != nil {
		t.Fatalf("quote after expiry: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "merchant", 150); err != nil {
		t.Fatalf("reserve after expiry cleanup: %v", err)
	}
}

func TestEnginePersistenceAcrossRestart(t *testing.T) {
	store := openTestStorage(t)
	ctx := context.Background()
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	asset := Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	limits := Limits{DailyCap: 1_000_000}
	engine, err := NewEngine([]Asset{asset}, limits, store)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	clock := newTestClock(base)
	engine.WithClock(clock.Now)
	engine.SetPriceMaxAge(24 * time.Hour)
	engine.RecordPrice("ZNHB", "USD", 1.02, base)
	quote, err := engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	reservation, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	available, reserved, payouts, _ := engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 1_000_000-102) {
		t.Fatalf("available mismatch after reserve: got %d", available)
	}
	if reserved != mustAmountUnits(t, 102) {
		t.Fatalf("reserved mismatch after reserve: got %d", reserved)
	}
	if payouts != 0 {
		t.Fatalf("unexpected payouts after reserve: %d", payouts)
	}

	engineRestart, err := NewEngine([]Asset{asset}, limits, store)
	if err != nil {
		t.Fatalf("restart engine: %v", err)
	}
	clock2 := newTestClock(base.Add(10 * time.Second))
	engineRestart.WithClock(clock2.Now)
	available, reserved, payouts, ok := engineRestart.LedgerBalance("ZNHB")
	if !ok {
		t.Fatalf("ledger missing after restart")
	}
	if available != mustAmountUnits(t, 1_000_000-102) || reserved != mustAmountUnits(t, 102) || payouts != 0 {
		t.Fatalf("ledger mismatch after restart: available=%d reserved=%d payouts=%d", available, reserved, payouts)
	}
	intent, err := engineRestart.CreateCashOutIntent(ctx, reservation.QuoteID)
	if err != nil {
		t.Fatalf("cashout after restart: %v", err)
	}
	if intent.ReservationID != reservation.QuoteID {
		t.Fatalf("intent reservation mismatch: got %s want %s", intent.ReservationID, reservation.QuoteID)
	}
	available, reserved, payouts, _ = engineRestart.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 1_000_000-102) || reserved != 0 || payouts != mustAmountUnits(t, 102) {
		t.Fatalf("ledger mismatch after cashout: available=%d reserved=%d payouts=%d", available, reserved, payouts)
	}

	engineFinal, err := NewEngine([]Asset{asset}, limits, store)
	if err != nil {
		t.Fatalf("final restart engine: %v", err)
	}
	clock3 := newTestClock(base.Add(20 * time.Second))
	engineFinal.WithClock(clock3.Now)
	available, reserved, payouts, ok = engineFinal.LedgerBalance("ZNHB")
	if !ok {
		t.Fatalf("ledger missing after final restart")
	}
	if available != mustAmountUnits(t, 1_000_000-102) || reserved != 0 || payouts != mustAmountUnits(t, 102) {
		t.Fatalf("ledger mismatch after final restart: available=%d reserved=%d payouts=%d", available, reserved, payouts)
	}
	if _, err := engineFinal.CreateCashOutIntent(ctx, reservation.QuoteID); !errors.Is(err, ErrReservationConsumed) {
		t.Fatalf("expected reservation consumed after restart, got %v", err)
	}
}

func TestEngineDailyCapPersistsAcrossRestart(t *testing.T) {
	base := time.Date(2024, time.July, 10, 9, 30, 0, 0, time.UTC)
	limits := Limits{DailyCap: 150}
	store := openTestStorage(t)
	ctx := context.Background()

	engine, _ := buildTestEngine(t, base, 1_000, time.Minute, limits)
	engine.WithDailyUsageStore(store)
	engine.RecordPrice("ZNHB", "USD", 1.00, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100); err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	day, amount, ok, err := store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage: %v", err)
	}
	if !ok {
		t.Fatalf("expected persisted usage record")
	}
	wantUnits := mustAmountUnits(t, 100)
	if amount != wantUnits {
		t.Fatalf("unexpected stored amount: got %d want %d", amount, wantUnits)
	}
	if !sameDay(day, base) {
		t.Fatalf("unexpected stored day: got %s want %s", day, base)
	}

	// Simulate restart by constructing a new engine and restoring persisted state.
	restartBase := base.Add(2 * time.Hour)
	engine2, _ := buildTestEngine(t, restartBase, 1_000, time.Minute, limits)
	engine2.WithDailyUsageStore(store)
	engine2.RecordPrice("ZNHB", "USD", 1.00, time.Time{})

	quote, err = engine2.PriceQuote(ctx, "ZNHB", 40)
	if err != nil {
		t.Fatalf("price quote after restart: %v", err)
	}
	if _, err := engine2.ReserveQuote(ctx, quote.ID, "acct", 40); err != nil {
		t.Fatalf("reserve after restart: %v", err)
	}
	quote, err = engine2.PriceQuote(ctx, "ZNHB", 20)
	if err != nil {
		t.Fatalf("price quote for cap check: %v", err)
	}
	if _, err := engine2.ReserveQuote(ctx, quote.ID, "acct", 20); !errors.Is(err, ErrDailyCapExceeded) {
		t.Fatalf("expected cap exceeded after restart, got %v", err)
	}

	// Ensure persistence reflects the latest successful reservation.
	day, amount, ok, err = store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage post restart: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record after restart reservations")
	}
	remaining := wantUnits + mustAmountUnits(t, 40)
	if amount != remaining {
		t.Fatalf("unexpected stored amount after restart: got %d want %d", amount, remaining)
	}
	if !sameDay(day, base) {
		t.Fatalf("expected persisted day to remain unchanged, got %s want %s", day, base)
	}
}

func TestEnginePrecisionPreserved(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 1_000_000, 2*time.Minute, Limits{})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.23456789, time.Time{})
	amount := 123.456789
	quote, err := engine.PriceQuote(ctx, "ZNHB", amount)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if quote.Price != mustRateUnits(t, 1.23456789) {
		t.Fatalf("unexpected price units: got %d", quote.Price)
	}
	res, err := engine.ReserveQuote(ctx, quote.ID, "acct", amount)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	amountUnits := mustAmountUnits(t, amount)
	rateUnits := mustRateUnits(t, 1.23456789)
	expectedOut := mulDivRound(amountUnits, rateUnits, priceScale)
	if res.AmountOut != expectedOut {
		t.Fatalf("unexpected amount out units: got %d want %d", res.AmountOut, expectedOut)
	}
	available, reserved, _, _ := engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 1_000_000)-expectedOut {
		t.Fatalf("available mismatch with precision preservation: got %d want %d", available, mustAmountUnits(t, 1_000_000)-expectedOut)
	}
	if reserved != expectedOut {
		t.Fatalf("reserved mismatch with precision preservation: got %d want %d", reserved, expectedOut)
	}
}

func TestEngineDailyCapBoundaryExactMatch(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 1_000, time.Minute, Limits{DailyCap: 150})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.0, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100); err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	engine.RecordPrice("ZNHB", "USD", 1.0, time.Time{})
	quote, err = engine.PriceQuote(ctx, "ZNHB", 50)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 50); err != nil {
		t.Fatalf("reserve quote hitting cap: %v", err)
	}
	engine.RecordPrice("ZNHB", "USD", 1.0, time.Time{})
	quote, err = engine.PriceQuote(ctx, "ZNHB", 0.000001)
	if err != nil {
		t.Fatalf("price quote tiny amount: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 0.000001); !errors.Is(err, ErrDailyCapExceeded) {
		t.Fatalf("expected daily cap exceeded on boundary, got %v", err)
	}
}

func TestEngineRejectsExcessPrecisionAmounts(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 1_000, time.Minute, Limits{})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.0, time.Time{})
	if _, err := engine.PriceQuote(ctx, "ZNHB", 1.0000004); err == nil {
		t.Fatalf("expected precision error")
	}
}

func TestEngineReserveQuoteRoundingUp(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine, _ := buildTestEngine(t, base, 10_000, time.Minute, Limits{})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.0000005, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 1)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	res, err := engine.ReserveQuote(ctx, quote.ID, "acct", 1)
	if err != nil {
		t.Fatalf("reserve quote: %v", err)
	}
	expected := mustAmountUnits(t, 1.000001)
	if res.AmountOut != expected {
		t.Fatalf("unexpected rounded amount: got %d want %d", res.AmountOut, expected)
	}
	available, reserved, _, _ := engine.LedgerBalance("ZNHB")
	if available != mustAmountUnits(t, 10_000)-expected {
		t.Fatalf("available mismatch after rounding reserve: got %d", available)
	}
	if reserved != expected {
		t.Fatalf("reserved mismatch after rounding reserve: got %d want %d", reserved, expected)
	}
}

func TestEngineDailyCapWithLargeValues(t *testing.T) {
	base := time.Date(2024, time.July, 10, 9, 30, 0, 0, time.UTC)
	hugeCap := int64(math.MaxInt64 / amountScale)
	engine, _ := buildTestEngine(t, base, 1_000_000_000, time.Minute, Limits{DailyCap: hugeCap})
	ctx := context.Background()
	engine.RecordPrice("ZNHB", "USD", 1.01, time.Time{})
	quote, err := engine.PriceQuote(ctx, "ZNHB", 100)
	if err != nil {
		t.Fatalf("price quote: %v", err)
	}
	if _, err := engine.ReserveQuote(ctx, quote.ID, "acct", 100); err != nil {
		t.Fatalf("reserve quote under large cap: %v", err)
	}
	available, reserved, _, _ := engine.LedgerBalance("ZNHB")
	if available <= 0 || reserved <= 0 {
		t.Fatalf("ledger not updated for large cap scenario: available=%d reserved=%d", available, reserved)
	}
}
