package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"math"

	"go.opentelemetry.io/otel/trace"

	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

const amountScale = 1_000_000

func mustAmountUnits(t *testing.T, amount float64) int64 {
	t.Helper()
	return int64(math.Round(amount * float64(amountScale)))
}

func TestStableHandlersFlow(t *testing.T) {
	store, err := storage.Open("file:stable_handlers?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Now:     func() time.Time { return base.Add(10 * time.Second) },
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)

	traceCtx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xA0, 0xB0, 0xC0, 0xD0, 0xE0, 0xF0, 0x01},
		SpanID:     trace.SpanID{0x02, 0x04, 0x06, 0x08, 0x0A, 0x0C, 0x0E, 0x10},
		TraceFlags: trace.FlagsSampled,
	}))

	quoteBody := `{"asset":"ZNHB","amount":100,"account":"merchant-123"}`
	engine.RecordPrice("ZNHB", "USD", 1.02, base)

	quoteResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/quote", quoteBody)
	assertStatus(t, quoteResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_quote.json", quoteResp.Body.Bytes())

	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveBody := `{"quote_id":"` + quoteID + `","amount_in":100,"account":"merchant-123"}`
	reserveResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/reserve", reserveBody)
	assertStatus(t, reserveResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_reserve.json", reserveResp.Body.Bytes())

	available, reserved, payouts, ok := engine.LedgerBalance("ZNHB")
	if !ok {
		t.Fatalf("ledger balance missing")
	}
	if got, want := available, mustAmountUnits(t, 1_000_000-102); got != want {
		t.Fatalf("available balance mismatch: got %d want %d", got, want)
	}
	if got, want := reserved, mustAmountUnits(t, 102); got != want {
		t.Fatalf("reserved balance mismatch: got %d want %d", got, want)
	}
	if payouts != 0 {
		t.Fatalf("expected payouts 0, got %d", payouts)
	}

	reservationID := extractField(t, reserveResp.Body.Bytes(), "reservation_id")

	cashOutBody := `{"reservation_id":"` + reservationID + `"}`
	cashOutResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/cashout", cashOutBody)
	assertStatus(t, cashOutResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_cashout.json", cashOutResp.Body.Bytes())

	available, reserved, payouts, _ = engine.LedgerBalance("ZNHB")
	if got, want := available, mustAmountUnits(t, 1_000_000-102); got != want {
		t.Fatalf("available after cashout mismatch: got %d want %d", got, want)
	}
	if reserved != 0 {
		t.Fatalf("reserved after cashout mismatch: got %d want 0", reserved)
	}
	if got, want := payouts, mustAmountUnits(t, 102); got != want {
		t.Fatalf("payouts mismatch: got %d want %d", got, want)
	}

	cashOutAgain := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/cashout", cashOutBody)
	assertStatus(t, cashOutAgain.Code, http.StatusConflict)

	slippageBody := `{"asset":"ZNHB","amount":50,"account":"merchant-123"}`
	quoteSlippage := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/quote", slippageBody)
	assertStatus(t, quoteSlippage.Code, http.StatusOK)
	newQuoteID := extractField(t, quoteSlippage.Body.Bytes(), "quote_id")

	// Move the oracle by 5% to trigger slippage guard (limit is 0.5%).
	engine.RecordPrice("ZNHB", "USD", 1.07, base.Add(30*time.Second))
	reserveSlippage := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/reserve", `{"quote_id":"`+newQuoteID+`","amount_in":50,"account":"merchant-123"}`)
	assertStatus(t, reserveSlippage.Code, http.StatusConflict)

	statusResp := doStableRequest(t, mux, traceCtx, http.MethodGet, "/v1/stable/status", "")
	assertStatus(t, statusResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_status.json", statusResp.Body.Bytes())

	limitsResp := doStableRequest(t, mux, traceCtx, http.MethodGet, "/v1/stable/limits", "")
	assertStatus(t, limitsResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_limits.json", limitsResp.Body.Bytes())
}

func TestStableHandlersDisabled(t *testing.T) {
	store, err := storage.Open("file:stable_handlers_disabled?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)

	resp := doStableRequest(t, mux, context.Background(), http.MethodGet, "/v1/stable/status", "")
	assertStatus(t, resp.Code, http.StatusNotImplemented)
	assertGoldenJSON(t, "stable_disabled.json", resp.Body.Bytes())
}

func newTestStableEngine(t *testing.T, base time.Time) *stable.Engine {
	t.Helper()
	assets := []stable.Asset{{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}}
	engine, err := stable.NewEngine(assets, stable.Limits{DailyCap: 1_000_000})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	var mu sync.Mutex
	var counter int
	engine.WithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		ts := base.Add(time.Duration(counter) * time.Second)
		counter++
		return ts
	})
	engine.SetPriceMaxAge(24 * time.Hour)
	engine.RecordPrice("ZNHB", "USD", 1.02, base)
	return engine
}

func doStableRequest(t *testing.T, mux *http.ServeMux, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(ctx)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	return resp
}

func assertStatus(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("unexpected status: got %d want %d", got, want)
	}
}

func assertGoldenJSON(t *testing.T, filename string, actual []byte) {
	t.Helper()
	goldenPath := filepath.Join("testdata", filename)
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", filename, err)
	}
	var want, got interface{}
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("unmarshal golden %s: %v", filename, err)
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatalf("unmarshal actual: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("payload mismatch for %s: want=%s got=%s", filename, strings.TrimSpace(string(wantBytes)), strings.TrimSpace(string(actual)))
	}
}

func extractField(t *testing.T, payload []byte, field string) string {
	t.Helper()
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	value, ok := data[field]
	if !ok {
		t.Fatalf("field %s missing", field)
	}
	str, ok := value.(string)
	if !ok {
		t.Fatalf("field %s not string", field)
	}
	return str
}
