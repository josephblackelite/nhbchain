package payoutd

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhbchain/native/swap"
	"nhbchain/services/payoutd/wallet"
)

type fakeTreasuryWallet struct {
	balances  map[string]*big.Int
	transfers int
}

type fakeAttestor struct {
	receipts int
	aborts   int
}

func (f *fakeAttestor) SubmitReceipt(context.Context, Receipt) error {
	f.receipts++
	return nil
}

func (f *fakeAttestor) AbortIntent(context.Context, string, string) error {
	f.aborts++
	return nil
}

func (f *fakeTreasuryWallet) Transfer(context.Context, string, string, *big.Int) (string, error) {
	f.transfers++
	return "0xtx", nil
}

func (f *fakeTreasuryWallet) WaitForConfirmations(context.Context, string, int, time.Duration) error {
	return nil
}

func (f *fakeTreasuryWallet) Balance(_ context.Context, asset string) (*big.Int, error) {
	if f.balances == nil {
		return big.NewInt(0), nil
	}
	value := f.balances[strings.ToUpper(strings.TrimSpace(asset))]
	if value == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(value), nil
}

func (f *fakeTreasuryWallet) Status() wallet.Status {
	return wallet.Status{
		Mode:        "test",
		FromAddress: "0xhot",
		Assets: map[string]wallet.AssetStatus{
			"USDC": {Native: false, TokenAddress: "0xusdc"},
		},
	}
}

func TestProcessorRejectsUnderfundedHotWallet(t *testing.T) {
	policies, err := NewPolicyEnforcer([]Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000),
		SoftInventory: big.NewInt(1_000),
		Confirmations: 1,
	}})
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	hotWallet := &fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(50)}}
	processor := NewProcessor(policies, WithWallet(hotWallet))

	err = processor.Process(context.Background(), CashOutRequest{
		Intent: &swap.CashOutIntent{
			IntentID:     "intent-1",
			StableAsset:  swap.StableAssetUSDC,
			StableAmount: big.NewInt(100),
			NhbAmount:    big.NewInt(100),
		},
		Destination: "0x00000000000000000000000000000000000000aa",
	})
	if err == nil || !strings.Contains(err.Error(), ErrOnChainBalanceInsufficient.Error()) {
		t.Fatalf("expected on-chain balance error, got %v", err)
	}
	if hotWallet.transfers != 0 {
		t.Fatalf("expected no transfer attempt, got %d", hotWallet.transfers)
	}
}

func TestProcessorPersistsPayoutExecutions(t *testing.T) {
	store, err := NewBoltPayoutExecutionStore(filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()
	policies, err := NewPolicyEnforcer([]Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000),
		SoftInventory: big.NewInt(1_000),
		Confirmations: 1,
	}})
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	processor := NewProcessor(policies,
		WithWallet(&fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(500)}}),
		WithAttestor(&fakeAttestor{}),
		WithExecutionStore(store),
		WithClock(func() time.Time { return time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC) }),
	)

	err = processor.Process(context.Background(), CashOutRequest{
		Intent: &swap.CashOutIntent{
			IntentID:     "intent-persist-1",
			Account:      "nhb1merchant",
			StableAsset:  swap.StableAssetUSDC,
			StableAmount: big.NewInt(100),
			NhbAmount:    big.NewInt(100),
		},
		Destination: "0x00000000000000000000000000000000000000aa",
		EvidenceURI: "evidence://payout/1",
		PartnerID:   "partner-west",
		Region:      "uae",
		RequestedBy: "operator-1",
		Approval: ApprovalMetadata{
			ApprovedBy: "checker-1",
			Reference:  "case-1",
		},
	})
	if err != nil {
		t.Fatalf("process payout: %v", err)
	}

	record, ok, err := store.Get("intent-persist-1")
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if !ok {
		t.Fatalf("expected execution record")
	}
	if record.Status != PayoutExecutionSettled {
		t.Fatalf("expected settled status, got %q", record.Status)
	}
	if record.TxHash != "0xtx" {
		t.Fatalf("expected tx hash 0xtx, got %q", record.TxHash)
	}
	if record.Account != "nhb1merchant" || record.PartnerID != "partner-west" || record.Region != "uae" {
		t.Fatalf("expected persisted payout metadata, got %+v", record)
	}
}

func TestTreasurySnapshotBuildsRefillPlan(t *testing.T) {
	policies, err := NewPolicyEnforcer([]Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000),
		SoftInventory: big.NewInt(500),
		Confirmations: 1,
	}})
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	hotWallet := &fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(200)}}
	processor := NewProcessor(policies,
		WithWallet(hotWallet),
		WithTreasuryAssets(map[string]TreasuryAssetConfig{
			"USDC": {
				Asset:            "USDC",
				ColdAddress:      "0xcold",
				HotMinBalance:    big.NewInt(300),
				HotTargetBalance: big.NewInt(500),
			},
		}),
	)

	snapshot, err := processor.TreasurySnapshot(context.Background())
	if err != nil {
		t.Fatalf("treasury snapshot: %v", err)
	}
	if len(snapshot.Assets) != 1 {
		t.Fatalf("expected one asset, got %d", len(snapshot.Assets))
	}
	asset := snapshot.Assets[0]
	if asset.Action != "refill_hot" {
		t.Fatalf("expected refill_hot action, got %q", asset.Action)
	}
	if asset.RecommendedAmount != "300" {
		t.Fatalf("expected recommended refill 300, got %q", asset.RecommendedAmount)
	}
	if asset.CoverageDelta != "-300" {
		t.Fatalf("expected coverage delta -300, got %q", asset.CoverageDelta)
	}

	plan, err := processor.TreasurySweepPlan(context.Background())
	if err != nil {
		t.Fatalf("treasury sweep plan: %v", err)
	}
	if len(plan.Plans) != 1 || plan.Plans[0].Asset != "USDC" {
		t.Fatalf("unexpected sweep plan %+v", plan.Plans)
	}
}

func TestAdminServerTreasurySweepPlanEndpoint(t *testing.T) {
	policies, err := NewPolicyEnforcer([]Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000),
		SoftInventory: big.NewInt(100),
		Confirmations: 1,
	}})
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	processor := NewProcessor(policies,
		WithWallet(&fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(800)}}),
		WithTreasuryAssets(map[string]TreasuryAssetConfig{
			"USDC": {
				Asset:            "USDC",
				ColdAddress:      "0xcold",
				HotMinBalance:    big.NewInt(200),
				HotTargetBalance: big.NewInt(500),
			},
		}),
	)
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	server := NewAdminServer(processor, auth)
	req := httptest.NewRequest(http.MethodGet, "/treasury/sweep-plan", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var payload TreasurySweepPlan
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Plans) != 1 {
		t.Fatalf("expected one sweep plan, got %d", len(payload.Plans))
	}
	if payload.Plans[0].Action != "sweep_to_cold" {
		t.Fatalf("expected sweep_to_cold, got %q", payload.Plans[0].Action)
	}
	if payload.Plans[0].RecommendedAmount != "300" {
		t.Fatalf("expected sweep amount 300, got %q", payload.Plans[0].RecommendedAmount)
	}
}

func TestTreasuryInstructionMakerCheckerFlow(t *testing.T) {
	store, err := NewBoltTreasuryInstructionStore(filepath.Join(t.TempDir(), "treasury.db"))
	if err != nil {
		t.Fatalf("new treasury store: %v", err)
	}
	defer store.Close()
	processor := NewProcessor(
		nil,
		WithWallet(&fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(800)}}),
		WithTreasuryAssets(map[string]TreasuryAssetConfig{
			"USDC": {
				Asset:            "USDC",
				ColdAddress:      "0xcold",
				HotMinBalance:    big.NewInt(200),
				HotTargetBalance: big.NewInt(500),
			},
		}),
		WithTreasuryStore(store),
	)

	instruction, err := processor.CreateTreasuryInstruction(TreasuryInstructionRequest{
		Action:      "sweep_to_cold",
		Asset:       "USDC",
		Amount:      big.NewInt(300),
		RequestedBy: "maker-1",
		Notes:       "sweep excess",
	})
	if err != nil {
		t.Fatalf("create instruction: %v", err)
	}
	if instruction.Status != TreasuryInstructionPending {
		t.Fatalf("expected pending instruction, got %q", instruction.Status)
	}
	if instruction.Destination != "0xcold" {
		t.Fatalf("unexpected destination %q", instruction.Destination)
	}
	if _, err := processor.ReviewTreasuryInstruction(TreasuryInstructionDecision{
		ID:    instruction.ID,
		Actor: "maker-1",
	}); err == nil {
		t.Fatalf("expected maker-checker rejection")
	}
	reviewed, err := processor.ReviewTreasuryInstruction(TreasuryInstructionDecision{
		ID:    instruction.ID,
		Actor: "checker-1",
		Notes: "approved",
	})
	if err != nil {
		t.Fatalf("review instruction: %v", err)
	}
	if reviewed.Status != TreasuryInstructionApproved {
		t.Fatalf("expected approved instruction, got %q", reviewed.Status)
	}
	items, err := processor.ListTreasuryInstructions("approved")
	if err != nil {
		t.Fatalf("list instructions: %v", err)
	}
	if len(items) != 1 || items[0].ID != instruction.ID {
		t.Fatalf("unexpected approved list %+v", items)
	}
}

func TestAdminServerTreasuryInstructionEndpoints(t *testing.T) {
	store, err := NewBoltTreasuryInstructionStore(filepath.Join(t.TempDir(), "treasury.db"))
	if err != nil {
		t.Fatalf("new treasury store: %v", err)
	}
	defer store.Close()
	processor := NewProcessor(
		nil,
		WithWallet(&fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(800)}}),
		WithTreasuryAssets(map[string]TreasuryAssetConfig{
			"USDC": {
				Asset:            "USDC",
				ColdAddress:      "0xcold",
				HotMinBalance:    big.NewInt(200),
				HotTargetBalance: big.NewInt(500),
			},
		}),
		WithTreasuryStore(store),
	)
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	server := NewAdminServer(processor, auth)

	createReq := httptest.NewRequest(http.MethodPost, "/treasury/instructions", strings.NewReader(`{"action":"sweep_to_cold","asset":"USDC","amount":"300","requested_by":"maker-1","notes":"sweep"}`))
	createReq.Header.Set("Authorization", "Bearer secret")
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createResp.Code, createResp.Body.String())
	}
	var created TreasuryInstruction
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created instruction: %v", err)
	}
	reviewReq := httptest.NewRequest(http.MethodPost, "/treasury/instructions/review", strings.NewReader(`{"id":"`+created.ID+`","actor":"checker-1","notes":"approved"}`))
	reviewReq.Header.Set("Authorization", "Bearer secret")
	reviewReq.Header.Set("Content-Type", "application/json")
	reviewResp := httptest.NewRecorder()
	server.ServeHTTP(reviewResp, reviewReq)
	if reviewResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", reviewResp.Code, reviewResp.Body.String())
	}
	listReq := httptest.NewRequest(http.MethodGet, "/treasury/instructions?status=approved", nil)
	listReq.Header.Set("Authorization", "Bearer secret")
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listResp.Code, listResp.Body.String())
	}
	var items []TreasuryInstruction
	if err := json.NewDecoder(listResp.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(items) != 1 || items[0].Status != TreasuryInstructionApproved {
		t.Fatalf("unexpected listed instructions %+v", items)
	}
}

func TestAdminServerExecutionEndpoint(t *testing.T) {
	executionStore, err := NewBoltPayoutExecutionStore(filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer executionStore.Close()
	if err := executionStore.Put(PayoutExecution{
		IntentID:    "intent-1",
		StableAsset: "USDC",
		Status:      PayoutExecutionSettled,
		TxHash:      "0xtx",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("put execution: %v", err)
	}
	processor := NewProcessor(nil, WithExecutionStore(executionStore))
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	server := NewAdminServer(processor, auth)
	req := httptest.NewRequest(http.MethodGet, "/executions?status=settled", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var items []PayoutExecution
	if err := json.NewDecoder(recorder.Body).Decode(&items); err != nil {
		t.Fatalf("decode executions: %v", err)
	}
	if len(items) != 1 || items[0].IntentID != "intent-1" {
		t.Fatalf("unexpected execution list %+v", items)
	}
}

func TestProcessorRejectsActiveHold(t *testing.T) {
	holdStore, err := NewBoltHoldStore(filepath.Join(t.TempDir(), "holds.db"))
	if err != nil {
		t.Fatalf("new hold store: %v", err)
	}
	defer holdStore.Close()
	if err := holdStore.Put(HoldRecord{
		ID:        "hold-1",
		Scope:     HoldScopeDestination,
		Value:     "0xblocked",
		Reason:    "sanctions-review",
		CreatedBy: "risk-1",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Active:    true,
	}); err != nil {
		t.Fatalf("put hold: %v", err)
	}
	policies, err := NewPolicyEnforcer([]Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000),
		SoftInventory: big.NewInt(1_000),
		Confirmations: 1,
	}})
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	processor := NewProcessor(policies,
		WithWallet(&fakeTreasuryWallet{balances: map[string]*big.Int{"USDC": big.NewInt(1_000)}}),
		WithAttestor(&fakeAttestor{}),
		WithHoldStore(holdStore),
	)
	err = processor.Process(context.Background(), CashOutRequest{
		Intent: &swap.CashOutIntent{
			IntentID:     "intent-held",
			Account:      "nhb1customer",
			StableAsset:  swap.StableAssetUSDC,
			StableAmount: big.NewInt(100),
			NhbAmount:    big.NewInt(100),
		},
		Destination: "0xblocked",
	})
	if err == nil || !strings.Contains(err.Error(), "active destination hold") {
		t.Fatalf("expected active hold error, got %v", err)
	}
}

func TestAdminServerHoldEndpoints(t *testing.T) {
	holdStore, err := NewBoltHoldStore(filepath.Join(t.TempDir(), "holds.db"))
	if err != nil {
		t.Fatalf("new hold store: %v", err)
	}
	defer holdStore.Close()
	processor := NewProcessor(nil, WithHoldStore(holdStore), WithClock(func() time.Time {
		return time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	}))
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	server := NewAdminServer(processor, auth)

	createReq := httptest.NewRequest(http.MethodPost, "/holds", strings.NewReader(`{"scope":"destination","value":"0xwatch","reason":"manual review","created_by":"risk-1"}`))
	createReq.Header.Set("Authorization", "Bearer secret")
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createResp.Code, createResp.Body.String())
	}
	var created HoldRecord
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created hold: %v", err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/holds?scope=destination&active=true", nil)
	listReq.Header.Set("Authorization", "Bearer secret")
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listResp.Code, listResp.Body.String())
	}
	var items []HoldRecord
	if err := json.NewDecoder(listResp.Body).Decode(&items); err != nil {
		t.Fatalf("decode holds: %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("unexpected holds %+v", items)
	}
	releaseReq := httptest.NewRequest(http.MethodPost, "/holds/release", strings.NewReader(`{"id":"`+created.ID+`","actor":"risk-2","notes":"cleared"}`))
	releaseReq.Header.Set("Authorization", "Bearer secret")
	releaseReq.Header.Set("Content-Type", "application/json")
	releaseResp := httptest.NewRecorder()
	server.ServeHTTP(releaseResp, releaseReq)
	if releaseResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", releaseResp.Code, releaseResp.Body.String())
	}
}
