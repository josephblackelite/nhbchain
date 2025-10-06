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

	"go.opentelemetry.io/otel/trace"

	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

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
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Now:     func() time.Time { return base.Add(10 * time.Second) },
	})
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
	quoteResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/quote", quoteBody)
	assertStatus(t, quoteResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_quote.json", quoteResp.Body.Bytes())

	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveBody := `{"quote_id":"` + quoteID + `","amount_in":100,"account":"merchant-123"}`
	reserveResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/reserve", reserveBody)
	assertStatus(t, reserveResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_reserve.json", reserveResp.Body.Bytes())

	reservationID := extractField(t, reserveResp.Body.Bytes(), "reservation_id")

	cashOutBody := `{"reservation_id":"` + reservationID + `"}`
	cashOutResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/cashout", cashOutBody)
	assertStatus(t, cashOutResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_cashout.json", cashOutResp.Body.Bytes())

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

	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{})
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
