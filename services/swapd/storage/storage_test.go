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

func openTestDB(t *testing.T) *Storage {
	t.Helper()
	store, err := Open("file:swapd_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
