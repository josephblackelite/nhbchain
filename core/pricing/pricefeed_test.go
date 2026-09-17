package pricing

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/native/loyalty"
	"nhbchain/native/swap"
)

type fakeOracle struct {
	quote   swap.PriceQuote
	twap    swap.TWAPResult
	rateErr error
	twapErr error
}

func (f *fakeOracle) GetRate(base, quote string) (swap.PriceQuote, error) {
	return f.quote, f.rateErr
}

func (f *fakeOracle) TWAP(base, quote string, window time.Duration) (swap.TWAPResult, error) {
	return f.twap, f.twapErr
}

func TestDefaultPriceFeedOK(t *testing.T) {
	now := time.Now().UTC()
	oracle := &fakeOracle{
		quote: swap.PriceQuote{Rate: big.NewRat(150, 100), Timestamp: now.Add(-30 * time.Second)},
		twap:  swap.TWAPResult{Average: big.NewRat(150, 100)},
	}
	guard := loyalty.PriceGuardConfig{
		PricePair:          "ZNHB/USD",
		PriceMaxAgeSeconds: 900,
		TwapWindowSeconds:  3600,
		MaxDeviationBps:    500,
	}
	feed, err := NewDefaultPriceFeed(oracle, guard)
	if err != nil {
		t.Fatalf("construct feed: %v", err)
	}
	result, err := feed.GetZNHBUSD(now)
	if err != nil {
		t.Fatalf("get price: %v", err)
	}
	if result.Status != PriceStatusOK {
		t.Fatalf("expected ok status, got %s", result.Status)
	}
	if result.AgeSeconds == 0 || result.AgeSeconds > 31 {
		t.Fatalf("unexpected age seconds: %d", result.AgeSeconds)
	}
	expected := new(big.Int).Mul(big.NewInt(150), new(big.Int).Lsh(big.NewInt(1), 64))
	expected.Quo(expected, big.NewInt(100))
	if result.PriceQ64.Cmp(expected) != 0 {
		t.Fatalf("unexpected price q64: %s", result.PriceQ64)
	}
}

func TestDefaultPriceFeedStale(t *testing.T) {
	now := time.Now().UTC()
	guard := loyalty.PriceGuardConfig{
		PricePair:          "ZNHB/USD",
		PriceMaxAgeSeconds: 60,
	}
	oracle := &fakeOracle{
		quote: swap.PriceQuote{Rate: big.NewRat(1, 1), Timestamp: now.Add(-2 * time.Minute)},
		twap:  swap.TWAPResult{Average: big.NewRat(1, 1)},
	}
	feed, err := NewDefaultPriceFeed(oracle, guard)
	if err != nil {
		t.Fatalf("construct feed: %v", err)
	}
	result, err := feed.GetZNHBUSD(now)
	if err != nil {
		t.Fatalf("get price: %v", err)
	}
	if result.Status != PriceStatusStale {
		t.Fatalf("expected stale status, got %s", result.Status)
	}
	if result.AgeSeconds <= guard.PriceMaxAgeSeconds {
		t.Fatalf("expected age above guard, got %d", result.AgeSeconds)
	}
}

func TestDefaultPriceFeedDeviant(t *testing.T) {
	now := time.Now().UTC()
	guard := loyalty.PriceGuardConfig{
		PricePair:          "ZNHB/USD",
		PriceMaxAgeSeconds: 900,
		TwapWindowSeconds:  3600,
		MaxDeviationBps:    500,
	}
	oracle := &fakeOracle{
		quote: swap.PriceQuote{Rate: big.NewRat(150, 100), Timestamp: now.Add(-45 * time.Second)},
		twap:  swap.TWAPResult{Average: big.NewRat(1, 1)},
	}
	feed, err := NewDefaultPriceFeed(oracle, guard)
	if err != nil {
		t.Fatalf("construct feed: %v", err)
	}
	result, err := feed.GetZNHBUSD(now)
	if err != nil {
		t.Fatalf("get price: %v", err)
	}
	if result.Status != PriceStatusDeviant {
		t.Fatalf("expected deviant status, got %s", result.Status)
	}
	if result.AgeSeconds == 0 {
		t.Fatalf("expected non-zero age, got %d", result.AgeSeconds)
	}
}
