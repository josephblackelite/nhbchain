package main

import (
	"context"
	"log"
	"math/big"
	"time"

	swap "nhbchain/native/swap"
	"nhbchain/services/swapd/oracle"
	"nhbchain/services/swapd/storage"
)

// Example showing how to trigger a single aggregation tick and read the latest snapshot.
func main() {
	store, err := storage.Open("file:example?mode=memory&cache=shared")
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer store.Close()

	src := &staticSource{}
	mgr, err := oracle.New(store, []oracle.Source{src}, []oracle.Pair{{Base: "ZNHB", Quote: "USD"}}, time.Second, time.Minute, 1)
	if err != nil {
		log.Fatalf("manager: %v", err)
	}
	if err := mgr.Tick(context.Background()); err != nil {
		log.Fatalf("tick: %v", err)
	}
	snap, err := store.LatestSnapshot(context.Background(), "ZNHB", "USD")
	if err != nil {
		log.Fatalf("snapshot: %v", err)
	}
	log.Printf("median %s with feeders %v", snap.MedianRate, snap.Feeders)
}

type staticSource struct{}

func (s *staticSource) Name() string { return "static" }

func (s *staticSource) Fetch(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
	_ = ctx
	return swap.PriceQuote{Rate: new(big.Rat).SetFloat64(1.05), Timestamp: time.Now(), Source: "static"}, nil
}
