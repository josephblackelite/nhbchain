package storage

import (
	"context"
	"math/big"
	"testing"
	"time"

	swap "nhbchain/native/swap"
)

func TestRecordSnapshotAndLatest(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	quote := swap.PriceQuote{Timestamp: time.Unix(1700000000, 0)}
	rat := new(big.Rat).SetFloat64(1.23)
	quote.Rate = rat
	if err := store.RecordSample(ctx, "ZNHB", "USD", "now", quote, time.Unix(1700000100, 0)); err != nil {
		t.Fatalf("record sample: %v", err)
	}
	if err := store.RecordSnapshot(ctx, "ZNHB", "USD", "1.230000000000000000", []string{"now"}, "proof", time.Unix(1700000100, 0)); err != nil {
		t.Fatalf("record snapshot: %v", err)
	}
	snap, err := store.LatestSnapshot(ctx, "ZNHB", "USD")
	if err != nil {
		t.Fatalf("latest snapshot: %v", err)
	}
	if snap.MedianRate != "1.230000000000000000" {
		t.Fatalf("unexpected median: %s", snap.MedianRate)
	}
	if len(snap.Feeders) != 1 || snap.Feeders[0] != "now" {
		t.Fatalf("unexpected feeders: %+v", snap.Feeders)
	}
}

func TestThrottlePolicy(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	policy := Policy{ID: "default", MintLimit: 2, RedeemLimit: 1, Window: time.Minute}
	if err := store.SavePolicy(ctx, policy); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	loaded, err := store.GetPolicy(ctx, "default")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if loaded.MintLimit != 2 || loaded.RedeemLimit != 1 {
		t.Fatalf("unexpected policy: %+v", loaded)
	}
	now := time.Now()
	allow, err := store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, now)
	if err != nil {
		t.Fatalf("check throttle: %v", err)
	}
	if !allow {
		t.Fatalf("expected first mint to pass")
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, now.Add(time.Second))
	if !allow {
		t.Fatalf("expected second mint to pass")
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, now.Add(2*time.Second))
	if allow {
		t.Fatalf("expected third mint to fail")
	}
	allow, err = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, now)
	if err != nil {
		t.Fatalf("check redeem: %v", err)
	}
	if !allow {
		t.Fatalf("expected redeem to pass")
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, now.Add(2*time.Second))
	if allow {
		t.Fatalf("expected redeem to fail")
	}
}

func TestDailyUsagePersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	day := time.Date(2024, time.March, 4, 12, 0, 0, 0, time.UTC)
	if err := store.SaveDailyUsage(ctx, day, 123); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	dayOut, amountOut, ok, err := store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record")
	}
	if amountOut != 123 {
		t.Fatalf("unexpected amount: got %d want %d", amountOut, 123)
	}
	wantDay := day.UTC().Truncate(24 * time.Hour)
	if !dayOut.Equal(wantDay) {
		t.Fatalf("unexpected day: got %s want %s", dayOut, wantDay)
	}
	if err := store.SaveDailyUsage(ctx, day, 456); err != nil {
		t.Fatalf("update usage: %v", err)
	}
	dayOut, amountOut, ok, err = store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage after update: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record after update")
	}
	if amountOut != 456 {
		t.Fatalf("unexpected amount after update: got %d want %d", amountOut, 456)
	}
	nextDay := wantDay.Add(24 * time.Hour)
	if err := store.SaveDailyUsage(ctx, nextDay, 10); err != nil {
		t.Fatalf("save next day usage: %v", err)
	}
	dayOut, amountOut, ok, err = store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage next day: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record for next day")
	}
	if !dayOut.Equal(nextDay) {
		t.Fatalf("unexpected day for next day record: got %s want %s", dayOut, nextDay)
	}
	if amountOut != 10 {
		t.Fatalf("unexpected amount for next day record: got %d want %d", amountOut, 10)
	}
}

func TestLedgerAndReservationPersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	ledger := LedgerBalanceRecord{Asset: "ZNHB", Available: 1_000_000, Reserved: 25_000, Payouts: 5_000}
	if err := store.SaveLedgerBalance(ctx, ledger); err != nil {
		t.Fatalf("save ledger: %v", err)
	}
	records, err := store.LoadLedgerBalances(ctx)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("unexpected ledger count: %d", len(records))
	}
	if records[0].Asset != "ZNHB" || records[0].Available != ledger.Available || records[0].Reserved != ledger.Reserved || records[0].Payouts != ledger.Payouts {
		t.Fatalf("unexpected ledger record: %+v", records[0])
	}
	expires := time.Unix(1_700_000_000, 0).UTC()
	res := ReservationRecord{ID: "q-1", Asset: "ZNHB", AmountIn: 100_000, AmountOut: 95_000, Price: 1_000_000_000, ExpiresAt: expires, Account: "acct-1"}
	if err := store.SaveReservation(ctx, res); err != nil {
		t.Fatalf("save reservation: %v", err)
	}
	reservations, err := store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("load reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("unexpected reservation count: %d", len(reservations))
	}
	loaded := reservations[0]
	if loaded.ID != res.ID || loaded.Asset != res.Asset || loaded.AmountIn != res.AmountIn || loaded.AmountOut != res.AmountOut {
		t.Fatalf("reservation mismatch: %+v", loaded)
	}
	if !loaded.ExpiresAt.Equal(expires) {
		t.Fatalf("reservation expiry mismatch: got %s want %s", loaded.ExpiresAt, expires)
	}
	res.IntentCreated = true
	res.IntentID = "intent-1"
	res.IntentCreatedAt = expires.Add(time.Minute)
	if err := store.SaveReservation(ctx, res); err != nil {
		t.Fatalf("update reservation: %v", err)
	}
	reservations, err = store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("reload reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("unexpected reservation count after update: %d", len(reservations))
	}
	loaded = reservations[0]
	if !loaded.IntentCreated || loaded.IntentID != res.IntentID {
		t.Fatalf("reservation intent not persisted: %+v", loaded)
	}
	if !loaded.IntentCreatedAt.Equal(res.IntentCreatedAt) {
		t.Fatalf("intent timestamp mismatch: got %s want %s", loaded.IntentCreatedAt, res.IntentCreatedAt)
	}
	if err := store.DeleteReservation(ctx, res.ID); err != nil {
		t.Fatalf("delete reservation: %v", err)
	}
	reservations, err = store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("reload reservations after delete: %v", err)
	}
	if len(reservations) != 0 {
		t.Fatalf("expected reservations to be empty, got %d", len(reservations))
	}
}

func openTestDB(t *testing.T) *Storage {
	t.Helper()
	store, err := Open("file:swapd_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
