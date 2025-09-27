package oracle

import (
	"context"
	"math/big"
	"testing"
	"time"

	swap "nhbchain/native/swap"
	"nhbchain/services/swapd/storage"
)

type fakeSource struct {
	name  string
	quote swap.PriceQuote
	err   error
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
	_ = ctx
	if f.err != nil {
		return swap.PriceQuote{}, f.err
	}
	return f.quote, nil
}

type capturingPublisher struct {
	updates []Update
}

func (c *capturingPublisher) PublishOracleUpdate(ctx context.Context, update Update) error {
	_ = ctx
	c.updates = append(c.updates, update)
	return nil
}

func TestManagerTickAggregatesMedian(t *testing.T) {
	store, err := storage.Open("file:oracle_mgr?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	srcA := &fakeSource{name: "alpha", quote: swap.PriceQuote{Rate: mustRat("1.0"), Timestamp: now}}
	srcB := &fakeSource{name: "beta", quote: swap.PriceQuote{Rate: mustRat("1.2"), Timestamp: now}}
	srcC := &fakeSource{name: "gamma", quote: swap.PriceQuote{Rate: mustRat("1.4"), Timestamp: now}}

	publisher := &capturingPublisher{}
	mgr, err := New(store, []Source{srcA, srcB, srcC}, []Pair{{Base: "ZNHB", Quote: "USD"}}, time.Second, time.Minute, 2, WithPublisher(publisher))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	snap, err := store.LatestSnapshot(context.Background(), "ZNHB", "USD")
	if err != nil {
		t.Fatalf("latest snapshot: %v", err)
	}
	if snap.MedianRate != "1.200000000000000000" {
		t.Fatalf("unexpected median: %s", snap.MedianRate)
	}
	if len(publisher.updates) != 1 {
		t.Fatalf("expected publisher to receive one update, got %d", len(publisher.updates))
	}
	if publisher.updates[0].Median != "1.200000000000000000" {
		t.Fatalf("unexpected published median: %s", publisher.updates[0].Median)
	}
}

func mustRat(value string) *big.Rat {
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		panic("invalid rat")
	}
	return rat
}
