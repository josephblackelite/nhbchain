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
