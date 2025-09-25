package swap

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type oracleFunc func(base, quote string) (PriceQuote, error)

func (f oracleFunc) GetRate(base, quote string) (PriceQuote, error) {
	return f(base, quote)
}

func TestManualOracleProvidesQuotes(t *testing.T) {
	manual := NewManualOracle()
	now := time.Now().UTC()
	if err := manual.SetDecimal("USD", "NHB", "0.75", now); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	quote, err := manual.GetRate("usd", "nhb")
	if err != nil {
		t.Fatalf("get rate: %v", err)
	}
	if quote.Rate == nil || quote.Rate.FloatString(2) != "0.75" {
		t.Fatalf("unexpected rate: %v", quote.Rate)
	}
	if !quote.Timestamp.Equal(now) {
		t.Fatalf("unexpected timestamp: %v", quote.Timestamp)
	}
}

func TestOracleAggregatorStaleQuote(t *testing.T) {
	manual := NewManualOracle()
	agg := NewOracleAggregator([]string{"manual"}, time.Second)
	agg.Register("manual", manual)
	if err := manual.SetDecimal("USD", "ZNHB", "0.50", time.Now().Add(-2*time.Second)); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if _, err := agg.GetRate("USD", "ZNHB"); err == nil {
		t.Fatalf("expected error for stale quote")
	}
}

func TestOracleAggregatorPriorityFallback(t *testing.T) {
	manual := NewManualOracle()
	agg := NewOracleAggregator([]string{"primary", "manual"}, 5*time.Minute)
	agg.Register("primary", oracleFunc(func(string, string) (PriceQuote, error) {
		return PriceQuote{}, fmt.Errorf("primary down")
	}))
	agg.Register("manual", manual)
	if err := manual.SetDecimal("USD", "ZNHB", "1.25", time.Now()); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	quote, err := agg.GetRate("USD", "ZNHB")
	if err != nil {
		t.Fatalf("get rate: %v", err)
	}
	if quote.Source != "manual" {
		t.Fatalf("expected manual source, got %s", quote.Source)
	}
}

func TestOracleAggregatorTWAP(t *testing.T) {
	now := time.Now().Add(-3 * time.Minute).UTC()
	quotes := []PriceQuote{
		{Rate: big.NewRat(1, 1), Timestamp: now.Add(0)},
		{Rate: big.NewRat(2, 1), Timestamp: now.Add(90 * time.Second)},
		{Rate: big.NewRat(4, 1), Timestamp: now.Add(3 * time.Minute)},
	}
	idx := 0
	agg := NewOracleAggregator([]string{"feed"}, 10*time.Minute)
	agg.SetTWAPWindow(5 * time.Minute)
	agg.SetTWAPSampleCap(16)
	agg.Register("feed", oracleFunc(func(string, string) (PriceQuote, error) {
		if idx >= len(quotes) {
			return PriceQuote{}, fmt.Errorf("exhausted")
		}
		q := quotes[idx]
		idx++
		return q, nil
	}))
	for range quotes {
		if _, err := agg.GetRate("USD", "ZNHB"); err != nil {
			t.Fatalf("get rate: %v", err)
		}
	}
	result, err := agg.TWAP("USD", "ZNHB", 0)
	if err != nil {
		t.Fatalf("twap: %v", err)
	}
	if result.Count != len(quotes) {
		t.Fatalf("expected %d samples, got %d", len(quotes), result.Count)
	}
	if got := result.Average.FloatString(3); got != "2.333" {
		t.Fatalf("unexpected twap: %s", got)
	}
	if result.Start.After(result.End) {
		t.Fatalf("invalid window: %v -> %v", result.Start, result.End)
	}
	health := agg.Health()
	if len(health.Feeds) != 1 {
		t.Fatalf("expected single feed health, got %+v", health.Feeds)
	}
	if health.Feeds[0].Observations != len(quotes) {
		t.Fatalf("unexpected observation count: %+v", health.Feeds[0])
	}
	if health.Feeds[0].Pair() != "USD/ZNHB" {
		t.Fatalf("unexpected pair: %s", health.Feeds[0].Pair())
	}
}

func TestNowPaymentsOracle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("from"); got != "USD" {
			t.Fatalf("expected from=USD, got %s", got)
		}
		if got := r.URL.Query().Get("to"); got != "ZNHB" {
			t.Fatalf("expected to=ZNHB, got %s", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"rate": "0.42", "timestamp": time.Now().Unix()})
	}))
	defer server.Close()
	oracle := NewNowPaymentsOracle(server.Client(), server.URL, "")
	quote, err := oracle.GetRate("usd", "znhb")
	if err != nil {
		t.Fatalf("get rate: %v", err)
	}
	if quote.Rate == nil || quote.Rate.FloatString(2) != "0.42" {
		t.Fatalf("unexpected rate: %v", quote.Rate)
	}
}

func TestCoinGeckoOracle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]map[string]interface{}{
			"znhb": {
				"usd":             0.91,
				"last_updated_at": time.Now().Unix(),
			},
		})
	}))
	defer server.Close()
	oracle := NewCoinGeckoOracle(server.Client(), server.URL, map[string]string{"ZNHB": "znhb"})
	quote, err := oracle.GetRate("USD", "ZNHB")
	if err != nil {
		t.Fatalf("get rate: %v", err)
	}
	if quote.Rate == nil || quote.Rate.FloatString(2) != "0.91" {
		t.Fatalf("unexpected rate: %v", quote.Rate)
	}
}

func TestComputeMintAmount(t *testing.T) {
	rate := big.NewRat(25, 1) // $25 per token
	amount, err := ComputeMintAmount("100.00", rate, 18)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	expected := new(big.Int).Mul(big.NewInt(4), scale)
	if amount.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, amount)
	}
}
