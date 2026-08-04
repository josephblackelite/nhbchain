package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"math"

	"go.opentelemetry.io/otel/trace"

	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

const amountScale = 1_000_000

func mustAmountUnits(t *testing.T, amount float64) int64 {
	t.Helper()
	return int64(math.Round(amount * float64(amountScale)))
}

type partnerCreds struct {
	id     string
	apiKey string
	secret string
}

var (
	nonceCounter     uint64
	timestampCounter int64 = time.Now().UTC().Unix()
)

func TestStableHandlersFlow(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_flow")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID:         creds.id,
			APIKey:     creds.apiKey,
			Secret:     creds.secret,
			DailyQuota: mustAmountUnits(t, 10_000),
		}},
		Now: func() time.Time { return base.Add(10 * time.Second) },
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

	quoteResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/quote", quoteBody, &creds)
	assertStatus(t, quoteResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_quote.json", quoteResp.Body.Bytes())

	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveBody := `{"quote_id":"` + quoteID + `","amount_in":100,"account":"merchant-123"}`
	reserveResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/reserve", reserveBody, &creds)
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
	cashOutResp := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/cashout", cashOutBody, &creds)
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

	cashOutAgain := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/cashout", cashOutBody, &creds)
	assertStatus(t, cashOutAgain.Code, http.StatusConflict)

	slippageBody := `{"asset":"ZNHB","amount":50,"account":"merchant-123"}`
	quoteSlippage := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/quote", slippageBody, &creds)
	assertStatus(t, quoteSlippage.Code, http.StatusOK)
	newQuoteID := extractField(t, quoteSlippage.Body.Bytes(), "quote_id")

	// Move the oracle by 5% to trigger slippage guard (limit is 0.5%).
	engine.RecordPrice("ZNHB", "USD", 1.07, base.Add(30*time.Second))
	reserveSlippage := doStableRequest(t, mux, traceCtx, http.MethodPost, "/v1/stable/reserve", `{"quote_id":"`+newQuoteID+`","amount_in":50,"account":"merchant-123"}`, &creds)
	assertStatus(t, reserveSlippage.Code, http.StatusConflict)

	statusResp := doStableRequest(t, mux, traceCtx, http.MethodGet, "/v1/stable/status", "", &creds)
	assertStatus(t, statusResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_status.json", statusResp.Body.Bytes())

	limitsResp := doStableRequest(t, mux, traceCtx, http.MethodGet, "/v1/stable/limits", "", &creds)
	assertStatus(t, limitsResp.Code, http.StatusOK)
	assertGoldenJSON(t, "stable_limits.json", limitsResp.Body.Bytes())

	auditEvents, err := store.ListAuditEvents(context.Background(), creds.id, 50)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(auditEvents) != 6 {
		t.Fatalf("expected 6 audit events, got %d: %+v", len(auditEvents), auditEvents)
	}
	var successCount, errorCount int
	for _, event := range auditEvents {
		if event.PartnerID != creds.id {
			t.Fatalf("unexpected partner on audit event: %+v", event)
		}
		if event.TraceID == "" {
			t.Fatalf("expected trace id on audit event: %+v", event)
		}
		switch event.Outcome {
		case "success":
			successCount++
		case "error":
			errorCount++
		}
	}
	if successCount != 4 || errorCount != 2 {
		t.Fatalf("unexpected audit outcome mix: success=%d error=%d events=%+v", successCount, errorCount, auditEvents)
	}
}

func TestAdminAuditEventsEndpoint(t *testing.T) {
	store := openStableTestStore(t, "admin_audit_events")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID:         creds.id,
			APIKey:     creds.apiKey,
			Secret:     creds.secret,
			DailyQuota: mustAmountUnits(t, 10_000),
		}},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	stableMux := http.NewServeMux()
	srv.registerStableHandlers(stableMux)
	quoteResp := doStableRequest(t, stableMux, context.Background(), http.MethodPost, "/v1/stable/quote", `{"asset":"ZNHB","amount":10,"account":"merchant-1"}`, &creds)
	assertStatus(t, quoteResp.Code, http.StatusOK)

	// No admin route without authentication.
	req := httptest.NewRequest(http.MethodGet, "/admin/audit/events", nil)
	recorder := httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.handleAuditEvents)).ServeHTTP(recorder, req)
	assertStatus(t, recorder.Code, http.StatusUnauthorized)

	req = httptest.NewRequest(http.MethodGet, "/admin/audit/events?partner_id="+creds.id, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	recorder = httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.handleAuditEvents)).ServeHTTP(recorder, req)
	assertStatus(t, recorder.Code, http.StatusOK)

	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal audit events response: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("expected 1 audit event, got %d: %s", len(body.Events), recorder.Body.String())
	}
	if body.Events[0]["event_type"] != "quote" || body.Events[0]["partner_id"] != creds.id {
		t.Fatalf("unexpected audit event: %+v", body.Events[0])
	}
}

func TestHandleStableCashOutRejectsWrongPartnerReservation(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_cross_partner")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD",
		QuoteTTL: time.Minute, MaxSlippageBps: 50, SoftInventory: 1_000_000,
	}
	credsA := partnerCreds{id: "desk-a", apiKey: "key-a", secret: "secret-a"}
	credsB := partnerCreds{id: "desk-b", apiKey: "key-b", secret: "secret-b"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{
			{ID: credsA.id, APIKey: credsA.apiKey, Secret: credsA.secret, DailyQuota: mustAmountUnits(t, 10_000)},
			{ID: credsB.id, APIKey: credsB.apiKey, Secret: credsB.secret, DailyQuota: mustAmountUnits(t, 10_000)},
		},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)

	quoteResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/quote", `{"asset":"ZNHB","amount":10,"account":"merchant-a"}`, &credsA)
	assertStatus(t, quoteResp.Code, http.StatusOK)
	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/reserve", `{"quote_id":"`+quoteID+`","amount_in":10,"account":"merchant-a"}`, &credsA)
	assertStatus(t, reserveResp.Code, http.StatusOK)
	reservationID := extractField(t, reserveResp.Body.Bytes(), "reservation_id")

	// Partner B, fully authenticated with its own valid credentials, must
	// not be able to cash out a reservation partner A created.
	crossPartnerAttempt := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/cashout", `{"reservation_id":"`+reservationID+`"}`, &credsB)
	assertStatus(t, crossPartnerAttempt.Code, http.StatusForbidden)

	// The legitimate owner must still be able to cash it out afterward --
	// the rejection above must not have consumed or damaged the reservation.
	ownerAttempt := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/cashout", `{"reservation_id":"`+reservationID+`"}`, &credsA)
	assertStatus(t, ownerAttempt.Code, http.StatusOK)

	// A retry of the now-already-consumed reservation by its rightful owner
	// must surface the normal "already consumed" conflict, not a misleading
	// "not owned" rejection.
	retryAttempt := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/cashout", `{"reservation_id":"`+reservationID+`"}`, &credsA)
	assertStatus(t, retryAttempt.Code, http.StatusConflict)

	// An entirely unknown reservation ID must be rejected the same way as a
	// real-but-foreign one -- no existence oracle.
	unknownAttempt := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/cashout", `{"reservation_id":"q-does-not-exist"}`, &credsB)
	assertStatus(t, unknownAttempt.Code, http.StatusForbidden)
}

func TestStableHandlersEnforcePartnerQuota(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_quota")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID:         creds.id,
			APIKey:     creds.apiKey,
			Secret:     creds.secret,
			DailyQuota: mustAmountUnits(t, 150),
		}},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)

	quoteBody := `{"asset":"ZNHB","amount":100,"account":"merchant-123"}`
	quoteResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/quote", quoteBody, &creds)
	if quoteResp.Code != http.StatusOK {
		t.Fatalf("quote status=%d body=%s", quoteResp.Code, quoteResp.Body.String())
	}
	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveBody := `{"quote_id":"` + quoteID + `","amount_in":100,"account":"merchant-123"}`
	reserveResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/reserve", reserveBody, &creds)
	if reserveResp.Code != http.StatusOK {
		t.Fatalf("reserve status=%d body=%s", reserveResp.Code, reserveResp.Body.String())
	}

	quoteBody2 := `{"asset":"ZNHB","amount":60,"account":"merchant-123"}`
	quoteResp2 := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/quote", quoteBody2, &creds)
	assertStatus(t, quoteResp2.Code, http.StatusOK)
	quoteID2 := extractField(t, quoteResp2.Body.Bytes(), "quote_id")

	reserveBody2 := `{"quote_id":"` + quoteID2 + `","amount_in":60,"account":"merchant-123"}`
	reserveResp2 := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/reserve", reserveBody2, &creds)
	assertStatus(t, reserveResp2.Code, http.StatusTooManyRequests)
	assertGoldenJSON(t, "stable_quota_exceeded.json", reserveResp2.Body.Bytes())

	available, reserved, _, ok := engine.LedgerBalance("ZNHB")
	if !ok {
		t.Fatalf("ledger balance missing")
	}
	if got, want := reserved, mustAmountUnits(t, 102); got != want {
		t.Fatalf("reserved balance mismatch after quota enforcement: got %d want %d", got, want)
	}
	if got, want := available, mustAmountUnits(t, 1_000_000-102); got != want {
		t.Fatalf("available balance mismatch after quota enforcement: got %d want %d", got, want)
	}
}

func TestStableHandlersDisabled(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_disabled")
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

	resp := doStableRequest(t, mux, context.Background(), http.MethodGet, "/v1/stable/status", "", nil)
	assertStatus(t, resp.Code, http.StatusNotImplemented)
	assertGoldenJSON(t, "stable_disabled.json", resp.Body.Bytes())
}

func TestStableHandlersRequireAuthentication(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_auth")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID:         creds.id,
			APIKey:     creds.apiKey,
			Secret:     creds.secret,
			DailyQuota: mustAmountUnits(t, 10_000),
		}},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)

	resp := doStableRequest(t, mux, context.Background(), http.MethodGet, "/v1/stable/status", "", nil)
	assertStatus(t, resp.Code, http.StatusUnauthorized)

	limitsResp := doStableRequest(t, mux, context.Background(), http.MethodGet, "/v1/stable/limits", "", nil)
	assertStatus(t, limitsResp.Code, http.StatusUnauthorized)
}

func TestStableHandlersRejectInvalidPrincipal(t *testing.T) {
	store := openStableTestStore(t, "stable_handlers_forbidden")
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID:         creds.id,
			APIKey:     creds.apiKey,
			Secret:     creds.secret,
			DailyQuota: mustAmountUnits(t, 10_000),
		}},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/stable/status", nil)
	ctx := context.WithValue(req.Context(), principalContextKey{}, &Principal{})
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()

	srv.handleStableStatus(recorder, req)

	assertStatus(t, recorder.Code, http.StatusForbidden)
	assertGoldenJSON(t, "stable_principal_forbidden.json", recorder.Body.Bytes())
}

func newTestStableEngine(t *testing.T, base time.Time, store *storage.Storage) *stable.Engine {
	t.Helper()
	assets := []stable.Asset{{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}}
	engine, err := stable.NewEngine(assets, stable.Limits{DailyCap: 1_000_000}, store)
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

func doStableRequest(t *testing.T, mux *http.ServeMux, ctx context.Context, method, path, body string, creds *partnerCreds) *httptest.ResponseRecorder {
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
	if creds != nil {
		signPartnerRequest(t, req, []byte(body), creds)
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

func openStableTestStore(t *testing.T, name string) *storage.Storage {
	t.Helper()
	dir := t.TempDir()
	file := strings.ReplaceAll(name, "/", "_") + ".sqlite"
	dsn, err := storage.FileDSN(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("build DSN: %v", err)
	}
	store, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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

func signPartnerRequest(t *testing.T, req *http.Request, body []byte, creds *partnerCreds) {
	t.Helper()
	if req == nil || creds == nil {
		return
	}
	ts := atomic.AddInt64(&timestampCounter, 1)
	timestamp := strconv.FormatInt(ts, 10)
	nonce := fmt.Sprintf("nonce-%d", atomic.AddUint64(&nonceCounter, 1))
	signature := gatewayauth.ComputeSignature(creds.secret, timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), body)
	req.Header.Set(gatewayauth.HeaderAPIKey, creds.apiKey)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))
}
