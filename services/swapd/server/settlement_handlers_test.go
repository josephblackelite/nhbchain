package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/services/swapd/settlement"
	"nhbchain/services/swapd/stable"
)

// fakePayoutClient lets settlement-handler tests control payout outcomes
// deterministically without a real NOWPayments account.
type fakePayoutClient struct {
	fail bool
	ref  string
}

func (f *fakePayoutClient) CreatePayout(ctx context.Context, req settlement.PayoutRequest) (settlement.PayoutResult, error) {
	if f.fail {
		return settlement.PayoutResult{}, errors.New("simulated payout rejection")
	}
	return settlement.PayoutResult{ExternalRef: f.ref}, nil
}

// newSettlementTestServer builds a full stable+settlement server and an
// HTTP mux mirroring exactly what Server.Run registers -- required because
// r.PathValue("id") on the admin settlement routes only resolves when the
// request is actually routed through a stdlib mux with the same wildcard
// patterns, not when a handler is invoked directly.
func newSettlementTestServer(t *testing.T, storeName string, railCfg settlement.Config, payoutClient settlement.PayoutClient) (*Server, *http.ServeMux, partnerCreds) {
	t.Helper()
	store := openStableTestStore(t, storeName)
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD",
		QuoteTTL: time.Minute, MaxSlippageBps: 50, SoftInventory: 1_000_000,
	}
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	mgr, err := settlement.NewManager(store, railCfg, payoutClient)
	if err != nil {
		t.Fatalf("new settlement manager: %v", err)
	}

	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  limits,
		Assets:  []stable.Asset{asset},
		Partners: []Partner{{
			ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret, DailyQuota: mustAmountUnits(t, 10_000),
		}},
		Settlement: mgr,
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)
	mux.Handle("GET /admin/settlements", srv.requireAdmin(http.HandlerFunc(srv.handleListSettlements)))
	mux.Handle("POST /admin/settlements/{id}/confirm", srv.requireAdmin(http.HandlerFunc(srv.handleConfirmSettlement)))
	mux.Handle("POST /admin/settlements/{id}/retry", srv.requireAdmin(http.HandlerFunc(srv.handleRetrySettlement)))
	mux.Handle("POST /admin/settlements/{id}/fail", srv.requireAdmin(http.HandlerFunc(srv.handleFailSettlement)))
	return srv, mux, creds
}

func doAdminRequest(t *testing.T, mux *http.ServeMux, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder
}

func createCashOutIntent(t *testing.T, mux *http.ServeMux, creds partnerCreds) map[string]any {
	t.Helper()
	quoteResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/quote", `{"asset":"ZNHB","amount":10,"account":"merchant-1"}`, &creds)
	assertStatus(t, quoteResp.Code, http.StatusOK)
	quoteID := extractField(t, quoteResp.Body.Bytes(), "quote_id")

	reserveResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/reserve", `{"quote_id":"`+quoteID+`","amount_in":10,"account":"merchant-1"}`, &creds)
	assertStatus(t, reserveResp.Code, http.StatusOK)
	reservationID := extractField(t, reserveResp.Body.Bytes(), "reservation_id")

	cashOutResp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/stable/cashout", `{"reservation_id":"`+reservationID+`"}`, &creds)
	assertStatus(t, cashOutResp.Code, http.StatusOK)
	var body map[string]any
	if err := json.Unmarshal(cashOutResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal cashout response: %v", err)
	}
	return body
}

func TestCashOutWiresManualTreasurySettlement(t *testing.T) {
	_, mux, creds := newSettlementTestServer(t, "settlement_manual", settlement.Config{DefaultRail: settlement.RailManualTreasury}, nil)

	body := createCashOutIntent(t, mux, creds)
	if body["settlement_rail"] != string(settlement.RailManualTreasury) {
		t.Fatalf("expected manual_treasury rail in response: %+v", body)
	}
	if body["settlement_status"] != string(settlement.StatusPending) {
		t.Fatalf("expected pending status in response: %+v", body)
	}
	settlementID, _ := body["settlement_id"].(string)
	if settlementID == "" {
		t.Fatalf("expected non-empty settlement_id: %+v", body)
	}

	// Confirming without a reference must fail.
	badConfirm := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/confirm", `{"note":"missing ref"}`, "test-token")
	assertStatus(t, badConfirm.Code, http.StatusBadRequest)

	confirmResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/confirm", `{"reference":"wire-123","note":"confirmed by ops","operator":"alice"}`, "test-token")
	assertStatus(t, confirmResp.Code, http.StatusOK)
	var confirmed map[string]any
	if err := json.Unmarshal(confirmResp.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("unmarshal confirm response: %v", err)
	}
	if confirmed["status"] != string(settlement.StatusSettled) {
		t.Fatalf("expected settled status, got %+v", confirmed)
	}
	if confirmed["external_ref"] != "wire-123" {
		t.Fatalf("expected external ref to be wire reference, got %+v", confirmed)
	}

	// Confirming again must fail -- already settled.
	again := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/confirm", `{"reference":"wire-456"}`, "test-token")
	assertStatus(t, again.Code, http.StatusConflict)
}

func TestCashOutWiresNowPaymentsSettlementSuccessThenConfirm(t *testing.T) {
	client := &fakePayoutClient{ref: "batch-777"}
	_, mux, creds := newSettlementTestServer(t, "settlement_nowpayments_success", settlement.Config{DefaultRail: settlement.RailNowPayments}, client)

	body := createCashOutIntent(t, mux, creds)
	if body["settlement_rail"] != string(settlement.RailNowPayments) {
		t.Fatalf("expected nowpayments rail: %+v", body)
	}
	if body["settlement_status"] != string(settlement.StatusSubmitted) {
		t.Fatalf("expected submitted status: %+v", body)
	}
	settlementID, _ := body["settlement_id"].(string)

	confirmResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/confirm", `{"reference":"batch-777-verified","operator":"ops1"}`, "test-token")
	assertStatus(t, confirmResp.Code, http.StatusOK)
}

func TestCashOutWiresNowPaymentsSettlementFailureAndRetry(t *testing.T) {
	client := &fakePayoutClient{fail: true}
	_, mux, creds := newSettlementTestServer(t, "settlement_nowpayments_failure", settlement.Config{DefaultRail: settlement.RailNowPayments}, client)

	body := createCashOutIntent(t, mux, creds)
	if body["settlement_status"] != string(settlement.StatusFailed) {
		t.Fatalf("expected failed status when payout client fails: %+v", body)
	}
	settlementID, _ := body["settlement_id"].(string)
	if settlementID == "" {
		t.Fatalf("expected settlement id to be present even on failure: %+v", body)
	}

	// Retrying while still failing keeps it failed.
	retryStillFails := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/retry", "{}", "test-token")
	assertStatus(t, retryStillFails.Code, http.StatusInternalServerError)

	// Fix the client, retry again -- should now succeed.
	client.fail = false
	client.ref = "batch-888"
	retryResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/retry", "{}", "test-token")
	assertStatus(t, retryResp.Code, http.StatusOK)
	var retried map[string]any
	if err := json.Unmarshal(retryResp.Body.Bytes(), &retried); err != nil {
		t.Fatalf("unmarshal retry response: %v", err)
	}
	if retried["status"] != string(settlement.StatusSubmitted) {
		t.Fatalf("expected submitted after successful retry: %+v", retried)
	}
	if retried["external_ref"] != "batch-888" {
		t.Fatalf("expected updated external ref: %+v", retried)
	}
}

func TestFailSettlementEndpoint(t *testing.T) {
	_, mux, creds := newSettlementTestServer(t, "settlement_fail_endpoint", settlement.Config{DefaultRail: settlement.RailManualTreasury}, nil)
	body := createCashOutIntent(t, mux, creds)
	settlementID, _ := body["settlement_id"].(string)

	failResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/fail", `{"reason":"partner cancelled trade"}`, "test-token")
	assertStatus(t, failResp.Code, http.StatusOK)
	var failed map[string]any
	if err := json.Unmarshal(failResp.Body.Bytes(), &failed); err != nil {
		t.Fatalf("unmarshal fail response: %v", err)
	}
	if failed["status"] != string(settlement.StatusFailed) {
		t.Fatalf("expected failed status: %+v", failed)
	}

	// A failed settlement is retryable only via the nowpayments retry path,
	// and this one is manual_treasury -- retry must be rejected.
	retryResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/"+settlementID+"/retry", "{}", "test-token")
	assertStatus(t, retryResp.Code, http.StatusConflict)
}

func TestListSettlementsEndpoint(t *testing.T) {
	_, mux, creds := newSettlementTestServer(t, "settlement_list_endpoint", settlement.Config{DefaultRail: settlement.RailManualTreasury}, nil)
	createCashOutIntent(t, mux, creds)
	createCashOutIntent(t, mux, creds)

	listResp := doAdminRequest(t, mux, http.MethodGet, "/admin/settlements?partner_id="+creds.id, "", "test-token")
	assertStatus(t, listResp.Code, http.StatusOK)
	var listBody struct {
		Settlements []map[string]any `json:"settlements"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listBody.Settlements) != 2 {
		t.Fatalf("expected 2 settlements, got %d: %+v", len(listBody.Settlements), listBody.Settlements)
	}
}

func TestSettlementAdminEndpointsRequireAuth(t *testing.T) {
	_, mux, _ := newSettlementTestServer(t, "settlement_auth", settlement.Config{DefaultRail: settlement.RailManualTreasury}, nil)

	listResp := doAdminRequest(t, mux, http.MethodGet, "/admin/settlements", "", "")
	assertStatus(t, listResp.Code, http.StatusUnauthorized)

	confirmResp := doAdminRequest(t, mux, http.MethodPost, "/admin/settlements/missing/confirm", `{"reference":"x"}`, "")
	assertStatus(t, confirmResp.Code, http.StatusUnauthorized)
}

func TestSettlementEndpointsWhenNotConfigured(t *testing.T) {
	store := openStableTestStore(t, "settlement_disabled")
	t.Cleanup(func() { _ = store.Close() })
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newTestStableEngine(t, base, store)
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	// StableRuntime.Settlement deliberately left nil.
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{
		Enabled: true,
		Engine:  engine,
		Limits:  stable.Limits{DailyCap: 1_000_000},
		Assets:  []stable.Asset{{Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD", QuoteTTL: time.Minute, MaxSlippageBps: 50, SoftInventory: 1_000_000}},
		Partners: []Partner{{
			ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret, DailyQuota: mustAmountUnits(t, 10_000),
		}},
	}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerStableHandlers(mux)
	mux.Handle("GET /admin/settlements", srv.requireAdmin(http.HandlerFunc(srv.handleListSettlements)))

	body := createCashOutIntent(t, mux, creds)
	if _, ok := body["settlement_id"]; ok {
		t.Fatalf("expected no settlement fields when settlement manager not configured: %+v", body)
	}

	listResp := doAdminRequest(t, mux, http.MethodGet, "/admin/settlements", "", "test-token")
	assertStatus(t, listResp.Code, http.StatusNotImplemented)
}
