package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/services/swapd/settlement"
)

// --- assignAgent / partnerIDFor ---------------------------------------------

// TestAssignAgentNoActiveAgentsLeavesUnassigned confirms a deployment with no
// exchange agents configured at all behaves identically to before this
// feature existed: every redemption's AssignedAgentID stays "", so
// partnerIDFor falls back to redemptionSettlementPartnerID and the
// automated NOWPayments rail.
func TestAssignAgentNoActiveAgentsLeavesUnassigned(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	const requestID = "req-no-agents-1"
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "400000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})

	watcher.runOnce(ctx)

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.AssignedAgentID != "" {
		t.Fatalf("expected no assigned agent, got %q", row.AssignedAgentID)
	}
	if payoutClient.callCount() != 1 {
		t.Fatalf("expected the automated payout rail to still be used, got %d calls", payoutClient.callCount())
	}
}

// TestAssignAgentRoundRobinsAcrossActiveAgents confirms assignAgent's own
// round-robin logic still works correctly -- multiple calls spread evenly
// across every currently-active agent, in a deterministic order, and a
// deactivated agent stops being returned. assignAgent is exercised DIRECTLY
// here, not through discoverNew/runOnce: as of 2026-09-06, discoverNew no
// longer calls it at all (see discoverNew's own doc comment on why) -- this
// function and its test are kept only so the still-open agent/cash-out
// redesign has a working, tested building block to wire back in deliberately,
// not as a claim that anything currently invokes it.
func TestAssignAgentRoundRobinsAcrossActiveAgents(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	now := time.Now().UTC()
	for _, agent := range []string{"agent-a", "agent-b"} {
		if err := store.UpsertExchangeAgent(ctx, ExchangeAgent{ID: agent, Name: agent, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("upsert agent %s: %v", agent, err)
		}
	}

	// Each call must alternate agent-a, agent-b, agent-a, agent-b -- it reads
	// CountAssignedRedemptionWatch fresh every time, so seed one assigned row
	// per iteration to advance that counter exactly like a real assignment
	// would.
	wantAgents := []string{"agent-a", "agent-b", "agent-a", "agent-b"}
	for i, want := range wantAgents {
		got := watcher.assignAgent(ctx)
		if got != want {
			t.Fatalf("call %d: expected agent %s, got %s", i, want, got)
		}
		requestID := "req-rr-" + string(rune('1'+i))
		if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
			RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "10000000000000000000",
			DestinationAsset: "USDTTRC20", DestinationAddress: testValidTRC20Address,
			LocalStatus: redemptionStatusDiscovered, AssignedAgentID: got, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed assigned row %d: %v", i, err)
		}
	}

	// Deactivate agent-b -- subsequent calls must only ever return agent-a.
	if err := store.UpsertExchangeAgent(ctx, ExchangeAgent{ID: "agent-b", Name: "agent-b", Active: false, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("deactivate agent-b: %v", err)
	}
	for i := 0; i < 3; i++ {
		got := watcher.assignAgent(ctx)
		if got != "agent-a" {
			t.Fatalf("expected only agent-a after deactivation, got %s", got)
		}
		requestID := "req-rr-after-deactivate-" + string(rune('1'+i))
		if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
			RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "10000000000000000000",
			DestinationAsset: "USDTTRC20", DestinationAddress: testValidTRC20Address,
			LocalStatus: redemptionStatusDiscovered, AssignedAgentID: got, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed assigned row after deactivate %d: %v", i, err)
		}
	}

	_ = payoutClient // unused in this direct-call test; kept for a consistent test-setup shape
}

// TestDiscoverNewNeverAutoAssignsAgent is the regression test for the actual
// incident: discoverNew must NEVER assign an exchange agent to an ordinary
// redemption, no matter how many agents are active, because doing so used
// to silently divert real customer Withdraw-to-USDT requests away from the
// automated NOWPayments rail onto a human-fulfilled one -- with no way for
// the customer, or anything in the request itself, to have asked for that.
// A separate, correctly-isolated agent/cash-out product (nhbportal's
// "Withdraw NHB to Cash", a plain on-chain Send, never a TxTypeRedeemNHB
// burn) already covers the legitimate agent use case and never reaches this
// code at all -- so every ordinary redemption discovered here must always
// use the shared automated rail, unconditionally.
func TestDiscoverNewNeverAutoAssignsAgent(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-restored"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	now := time.Now().UTC()
	for _, agent := range []string{"agent-a", "agent-b"} {
		if err := store.UpsertExchangeAgent(ctx, ExchangeAgent{ID: agent, Name: agent, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("upsert agent %s: %v", agent, err)
		}
	}

	for i := 0; i < 3; i++ {
		requestID := "req-no-auto-assign-" + string(rune('1'+i))
		node.setPending(RedemptionRequest{
			RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "20000000000000000000",
			DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
		})
		watcher.runOnce(ctx)
		row, err := store.GetRedemptionWatch(ctx, requestID)
		if err != nil {
			t.Fatalf("get redemption watch %s: %v", requestID, err)
		}
		if row.AssignedAgentID != "" {
			t.Fatalf("request %d: expected NO agent assignment despite %d active agents, got %q", i, 2, row.AssignedAgentID)
		}
		settlementRec, err := store.GetSettlement(ctx, row.SettlementID)
		if err != nil {
			t.Fatalf("get settlement %d: %v", i, err)
		}
		if settlementRec.Rail != string(settlement.RailNowPayments) {
			t.Fatalf("request %d: expected the automated nowpayments rail, got %s", i, settlementRec.Rail)
		}
	}
	if payoutClient.callCount() != 3 {
		t.Fatalf("expected all 3 requests to go through the automated payout rail, got %d calls", payoutClient.callCount())
	}
}

// TestExchangeAgentRoutesToManualTreasuryAndConfirmPayoutWorks covers
// processDiscovered's own routing/confirm-payout mechanics given a row that
// IS already assigned to an agent -- independent of HOW it came to be
// assigned. As of 2026-09-06, discoverNew itself never assigns one (see its
// doc comment and TestDiscoverNewNeverAutoAssignsAgent); this test seeds an
// already-assigned "discovered" row directly, simulating whatever the
// agent/cash-out redesign eventually wires up, and confirms that GIVEN such
// a row, it still correctly routes to RailManualTreasury (never touching the
// automated NOWPayments payout API), stays pending until the agent's "Mark
// Paid" action (RedeemWatcher.ConfirmPayout, the same mechanism the admin
// confirm-payout HTTP endpoint calls) supplies a reference, and only then
// proceeds to on-chain attestation exactly like a NOWPayments-settled
// redemption would.
func TestExchangeAgentRoutesToManualTreasuryAndConfirmPayoutWorks(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "should-not-be-used"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	now := time.Now().UTC()
	const agentID = "agent-solo"
	if err := store.UpsertExchangeAgent(ctx, ExchangeAgent{ID: agentID, Name: "Solo Agent", Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	// Mirrors main.go's startup wiring: an active agent's rail override must
	// be registered on the settlement manager for routing to actually work.
	settlementMgr.SetPartnerRail(agentID, settlement.RailManualTreasury)

	const requestID = "req-agent-flow-1"
	// Seed the row already assigned and in "discovered" state directly --
	// bypassing discoverNew/node.setPending entirely, since discovery no
	// longer performs this assignment itself.
	if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "50000000000000000000",
		PayoutAmountDecimal: "50", PayoutAmountUnits: 50_000_000,
		DestinationAsset: "USDTTRC20", DestinationAddress: testValidTRC20Address,
		LocalStatus: redemptionStatusDiscovered, AssignedAgentID: agentID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed assigned discovered row: %v", err)
	}
	// Still pending on-chain, so processDiscovered's fresh re-read (the race
	// guard against another watcher instance) finds it unchanged.
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "50000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})

	watcher.runOnce(ctx)

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.AssignedAgentID != agentID {
		t.Fatalf("expected assigned agent %s, got %s", agentID, row.AssignedAgentID)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status initiating, got %s", row.LocalStatus)
	}
	if payoutClient.callCount() != 0 {
		t.Fatalf("expected the automated NOWPayments rail to never be called for an agent-assigned request, got %d calls", payoutClient.callCount())
	}
	settlementRec, err := store.GetSettlement(ctx, row.SettlementID)
	if err != nil {
		t.Fatalf("get settlement: %v", err)
	}
	if settlementRec.Rail != string(settlement.RailManualTreasury) {
		t.Fatalf("expected manual_treasury rail, got %s", settlementRec.Rail)
	}
	if settlementRec.Status != string(settlement.StatusPending) {
		t.Fatalf("expected settlement to stay pending awaiting the agent's confirmation, got %s", settlementRec.Status)
	}

	// Another tick with nothing new must not attest or call the payout API.
	watcher.runOnce(ctx)
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation before the agent marks paid, got %d", node.attestCallCount())
	}

	// The exchange agent's "Mark Paid" action -- the same RedeemWatcher
	// method the admin confirm-payout HTTP endpoint calls.
	if _, err := watcher.ConfirmPayout(ctx, requestID, settlement.Receipt{
		Reference: "agent-bank-wire-ref-123",
		Operator:  "Solo Agent",
	}); err != nil {
		t.Fatalf("confirm payout: %v", err)
	}

	watcher.runOnce(ctx)
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation after the agent confirmed payout, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	if call.RequestID != requestID || call.Status != redemptionOutcomePaid || call.PayoutReference != "agent-bank-wire-ref-123" {
		t.Fatalf("unexpected attest call: %+v", call)
	}
}

// --- HTTP admin endpoints ----------------------------------------------------

// fakePartnerRailSetter records SetPartnerRail calls without needing a real
// settlement.Manager, for asserting handleAdminUpsertExchangeAgent applies
// the override immediately.
type fakePartnerRailSetter struct {
	calls []struct {
		partnerID string
		rail      settlement.Rail
	}
}

func (f *fakePartnerRailSetter) SetPartnerRail(partnerID string, rail settlement.Rail) {
	f.calls = append(f.calls, struct {
		partnerID string
		rail      settlement.Rail
	}{partnerID, rail})
}

// newTestExchangeAgentServer builds a minimal Server sufficient for exercising
// only the /admin/agents and /admin/redemptions endpoints -- these never
// touch the quotes/payments/webhook_events tables newTestStore leaves
// uncreated, so newTestRedeemStore (redemption tables only) is enough.
func newTestExchangeAgentServer(t *testing.T) (*Server, *SQLiteStore, *fakePartnerRailSetter) {
	t.Helper()
	store := newTestRedeemStore(t)
	srv := NewServer(store, NewOracle(time.Minute, 0.10, 0.50), &stubNowPayments{}, &stubNode{}, &stubSigner{}, time.Minute, "USD", "NHB", 0, "secret", "https://api.nhbcoin.com/webhooks/nowpayments")
	srv.SetAdminToken("admin-secret")
	rails := &fakePartnerRailSetter{}
	srv.SetExchangeAgentRailSetter(rails)
	return srv, store, rails
}

func authedRedemptionsRequest(method, path, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	}
	req.Header.Set("Authorization", "Bearer admin-secret")
	return req
}

func TestHandleAdminUpsertExchangeAgentRequiresAuth(t *testing.T) {
	srv, _, _ := newTestExchangeAgentServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/agents", bytes.NewReader([]byte(`{"id":"agent-1","name":"Agent One"}`)))
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no Authorization header, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleAdminUpsertExchangeAgentCreatesAndAppliesRailImmediately(t *testing.T) {
	srv, store, rails := newTestExchangeAgentServer(t)
	ctx := context.Background()

	req := authedRedemptionsRequest(http.MethodPost, "/admin/agents", `{"id":"agent-1","name":"Agent One"}`)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "agent-1" || !body.Active {
		t.Fatalf("unexpected response: %+v", body)
	}

	// Active defaults to true and must have been persisted.
	ids, err := store.ListActiveExchangeAgentIDs(ctx)
	if err != nil {
		t.Fatalf("list active agents: %v", err)
	}
	if len(ids) != 1 || ids[0] != "agent-1" {
		t.Fatalf("expected agent-1 to be active, got %v", ids)
	}

	// The rail override must have been applied immediately, not just
	// persisted for a future restart to pick up.
	if len(rails.calls) != 1 || rails.calls[0].partnerID != "agent-1" || rails.calls[0].rail != settlement.RailManualTreasury {
		t.Fatalf("expected an immediate SetPartnerRail(agent-1, manual_treasury) call, got %+v", rails.calls)
	}

	// Deactivating the same agent must update the stored row (so future
	// assignAgent calls skip it) but must NOT call SetPartnerRail again --
	// an already-routed redemption's rail must stay stable.
	req2 := authedRedemptionsRequest(http.MethodPost, "/admin/agents", `{"id":"agent-1","name":"Agent One","active":false}`)
	res2 := httptest.NewRecorder()
	srv.ServeHTTP(res2, req2)
	if res2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res2.Code, res2.Body.String())
	}
	ids, err = store.ListActiveExchangeAgentIDs(ctx)
	if err != nil {
		t.Fatalf("list active agents after deactivate: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no active agents after deactivation, got %v", ids)
	}
	if len(rails.calls) != 1 {
		t.Fatalf("expected no additional SetPartnerRail call from deactivation, got %d total calls", len(rails.calls))
	}
}

func TestHandleAdminUpsertExchangeAgentRejectsMissingID(t *testing.T) {
	srv, _, _ := newTestExchangeAgentServer(t)
	req := authedRedemptionsRequest(http.MethodPost, "/admin/agents", `{"name":"No ID"}`)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing id, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleAdminListRedemptionsFiltersByAgentAndStatus(t *testing.T) {
	srv, store, _ := newTestExchangeAgentServer(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(requestID, agentID, status string) {
		if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
			RequestID: requestID, Account: "nhb1acct", NHBAmountWei: "1000000000000000000",
			DestinationAsset: "USDTTRC20", DestinationAddress: testValidTRC20Address,
			LocalStatus: status, AssignedAgentID: agentID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", requestID, err)
		}
	}
	seed("req-a-1", "agent-a", redemptionStatusInitiating)
	seed("req-a-2", "agent-a", redemptionStatusAttested)
	seed("req-b-1", "agent-b", redemptionStatusInitiating)
	seed("req-unassigned-1", "", redemptionStatusDiscovered)

	// Requires auth like every other /admin/* endpoint.
	unauthed := httptest.NewRequest(http.MethodGet, "/admin/redemptions?agentId=agent-a", nil)
	unauthedRes := httptest.NewRecorder()
	srv.ServeHTTP(unauthedRes, unauthed)
	if unauthedRes.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no Authorization header, got %d", unauthedRes.Code)
	}

	req := authedRedemptionsRequest(http.MethodGet, "/admin/redemptions?agentId=agent-a", "")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	var listBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(listBody.Items) != 2 {
		t.Fatalf("expected 2 items for agent-a, got %d: %+v", len(listBody.Items), listBody.Items)
	}
	for _, item := range listBody.Items {
		if item["assignedAgentId"] != "agent-a" {
			t.Fatalf("expected every returned item to belong to agent-a, got %+v", item)
		}
	}

	// Further filtered by status.
	req2 := authedRedemptionsRequest(http.MethodGet, "/admin/redemptions?agentId=agent-a&status="+redemptionStatusInitiating, "")
	res2 := httptest.NewRecorder()
	srv.ServeHTTP(res2, req2)
	var filteredBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(res2.Body.Bytes(), &filteredBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(filteredBody.Items) != 1 || filteredBody.Items[0]["requestId"] != "req-a-1" {
		t.Fatalf("expected exactly req-a-1, got %+v", filteredBody.Items)
	}

	// A caller for an agent with zero rows gets an empty (not null/error)
	// list.
	req3 := authedRedemptionsRequest(http.MethodGet, "/admin/redemptions?agentId=agent-nobody", "")
	res3 := httptest.NewRecorder()
	srv.ServeHTTP(res3, req3)
	if res3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res3.Code, res3.Body.String())
	}
	var emptyBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(res3.Body.Bytes(), &emptyBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(emptyBody.Items) != 0 {
		t.Fatalf("expected an empty list, got %+v", emptyBody.Items)
	}
}
