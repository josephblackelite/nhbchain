package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/services/swapd/settlement"
	swapdstorage "nhbchain/services/swapd/storage"
)

// --- computeRedemptionPayout -----------------------------------------------

func TestComputeRedemptionPayout(t *testing.T) {
	cases := []struct {
		name        string
		nhbWei      string
		wantDecimal string
		wantUnits   int64
		wantErr     bool
	}{
		{
			name:        "whole amount",
			nhbWei:      "400000000000000000000", // 400 NHB
			wantDecimal: "400",
			wantUnits:   400_000_000,
		},
		{
			name:        "all 18 decimals populated",
			nhbWei:      "123456789012345678", // 0.123456789012345678 NHB
			wantDecimal: "0.123456789012345678",
			// floor(0.123456789012345678 * 1_000_000) = 123456 -- never
			// overpays by rounding up the truncated digits.
			wantUnits: 123456,
		},
		{
			name:        "very small amount rounds to zero settlement units",
			nhbWei:      "1", // 1 wei = 1e-18 NHB
			wantDecimal: "0.000000000000000001",
			wantErr:     true,
		},
		{
			name:    "zero amount",
			nhbWei:  "0",
			wantErr: true,
		},
		{
			name:    "negative amount",
			nhbWei:  "-100",
			wantErr: true,
		},
		{
			name:    "garbage input",
			nhbWei:  "not-a-number",
			wantErr: true,
		},
		{
			name:    "empty input",
			nhbWei:  "",
			wantErr: true,
		},
		{
			name:        "just above the settlement floor",
			nhbWei:      "1000000000000", // 1e12 wei = 0.000001 NHB
			wantDecimal: "0.000001",
			wantUnits:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decimal, units, err := computeRedemptionPayout(tc.nhbWei)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got decimal=%s units=%d", decimal, units)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decimal != tc.wantDecimal {
				t.Fatalf("decimal: got %s, want %s", decimal, tc.wantDecimal)
			}
			if units != tc.wantUnits {
				t.Fatalf("units: got %d, want %d", units, tc.wantUnits)
			}
		})
	}
}

// TestComputeRedemptionPayoutNeverOverpays is a property-style check across a
// spread of odd wei amounts: the settlement units this function returns,
// converted back to NHB-equivalent USDT at 6 decimals, must never exceed the
// exact decimal payout amount -- the custody wallet holds exactly 1 USDT per
// 1 NHB in circulation, so any rounding must only ever round down.
func TestComputeRedemptionPayoutNeverOverpays(t *testing.T) {
	amounts := []string{
		"1",
		"999999999999",
		"1000000000001",
		"400000000000000000000",
		"123456789012345678901",
		"7000000000000000001",
		"999999999999999999",
	}
	for _, wei := range amounts {
		decimal, units, err := computeRedemptionPayout(wei)
		if err != nil {
			// A rejected (rounds-to-zero) amount trivially never overpays.
			continue
		}
		// Recompute the exact settlement-unit ceiling implied by the frozen
		// decimal amount and confirm computeRedemptionPayout never exceeds
		// it.
		exactUnits, err := decimalAmountToUnitsForTest(decimal)
		if err != nil {
			t.Fatalf("wei=%s: %v", wei, err)
		}
		if units > exactUnits {
			t.Fatalf("wei=%s: computed units %d exceed the exact value %d implied by decimal %s -- this would overpay a burn", wei, units, exactUnits, decimal)
		}
	}
}

// decimalAmountToUnitsForTest independently floors decimal (a base-10
// string) to 6 decimal places without going through computeRedemptionPayout
// itself, so TestComputeRedemptionPayoutNeverOverpays isn't just checking
// the function against itself.
func decimalAmountToUnitsForTest(decimal string) (int64, error) {
	parts := strings.SplitN(decimal, ".", 2)
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	frac = frac[:6]
	combined := strings.TrimLeft(whole+frac, "0")
	if combined == "" {
		combined = "0"
	}
	var v int64
	if _, err := fmt.Sscanf(combined, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// --- isValidTRC20Address ----------------------------------------------------

func TestIsValidTRC20Address(t *testing.T) {
	cases := []struct {
		name    string
		address string
		valid   bool
	}{
		{"valid mainnet address", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", true},
		{"empty", "", false},
		{"too short", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuW", false},
		{"too long", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwbXX", false},
		{"wrong prefix character", "A9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", false},
		{"not base58 (contains 0)", "T0yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", false},
		{"random ascii garbage, right length", "T234567890123456789012345678901234", false},
		{"ethereum-style address", "0x0000000000000000000000000000000000dead", false},
		{"bad checksum", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidTRC20Address(tc.address); got != tc.valid {
				t.Fatalf("isValidTRC20Address(%q) = %v, want %v", tc.address, got, tc.valid)
			}
		})
	}
}

// --- watcher state-machine tests --------------------------------------------

// newTestRedeemStore returns a fresh in-memory SQLiteStore with the
// redemption tables created, isolated per test via a unique DSN name (unlike
// server_test.go's newTestStore, which intentionally shares one DB across
// the whole package's mint/deposit tests).
func newTestRedeemStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := fmt.Sprintf("file:test-redeem-%s?mode=memory&cache=shared", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	store, err := NewSQLiteStore(dsn)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	if err := store.initRedemptionTables(); err != nil {
		t.Fatalf("init redemption tables: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// fakePayoutClient mirrors services/swapd/settlement/settlement_test.go's
// fakePayoutClient exactly (same fields, same behavior) -- reused here so
// settlement.Manager's own crash-safety/state-machine guarantees are
// exercised the same way its own test suite exercises them, just through
// this package's Store implementation instead of swapd's.
type fakePayoutClient struct {
	mu      sync.Mutex
	calls   int
	fail    bool
	failMsg string
	ref     string
}

func (f *fakePayoutClient) CreatePayout(ctx context.Context, req settlement.PayoutRequest) (settlement.PayoutResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		msg := f.failMsg
		if msg == "" {
			msg = "payout rejected"
		}
		return settlement.PayoutResult{}, errors.New(msg)
	}
	return settlement.PayoutResult{ExternalRef: f.ref}, nil
}

func (f *fakePayoutClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRedeemNode is a controllable NodeClient for watcher tests: it never
// talks to a real node, and lets tests directly control what
// ListPendingRedemptions returns, what SendAttestRedemption does, and
// whether GetTransactionReceipt reports a given tx hash as confirmed.
type fakeRedeemNode struct {
	mu sync.Mutex

	pending []RedemptionRequest
	listErr error

	attestCalls  []attestCallRecord
	attestErr    error
	attestTxHash string

	receiptConfirmed map[string]bool
	receiptErr       error
}

type attestCallRecord struct {
	RequestID       string
	Status          string
	PayoutReference string
	FailureReason   string
}

func newFakeRedeemNode() *fakeRedeemNode {
	return &fakeRedeemNode{receiptConfirmed: make(map[string]bool)}
}

func (n *fakeRedeemNode) MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error) {
	return "", fmt.Errorf("fakeRedeemNode: MintWithSig not implemented")
}

func (n *fakeRedeemNode) ListPendingRedemptions(ctx context.Context) ([]RedemptionRequest, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.listErr != nil {
		return nil, n.listErr
	}
	out := make([]RedemptionRequest, len(n.pending))
	copy(out, n.pending)
	return out, nil
}

func (n *fakeRedeemNode) SendAttestRedemption(ctx context.Context, requestID, status, payoutReference, failureReason string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attestCalls = append(n.attestCalls, attestCallRecord{requestID, status, payoutReference, failureReason})
	if n.attestErr != nil {
		return "", n.attestErr
	}
	hash := n.attestTxHash
	if hash == "" {
		hash = fmt.Sprintf("0xattest-%d", len(n.attestCalls))
	}
	return hash, nil
}

func (n *fakeRedeemNode) GetTransactionReceipt(ctx context.Context, txHash string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.receiptErr != nil {
		return false, n.receiptErr
	}
	return n.receiptConfirmed[txHash], nil
}

func (n *fakeRedeemNode) setPending(reqs ...RedemptionRequest) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending = append([]RedemptionRequest(nil), reqs...)
}

func (n *fakeRedeemNode) confirmReceipt(txHash string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.receiptConfirmed[txHash] = true
}

func (n *fakeRedeemNode) attestCallCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.attestCalls)
}

func (n *fakeRedeemNode) lastAttestCall() attestCallRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.attestCalls) == 0 {
		return attestCallRecord{}
	}
	return n.attestCalls[len(n.attestCalls)-1]
}

const testValidTRC20Address = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"

func newTestSettlementManager(t *testing.T, store *SQLiteStore, client settlement.PayoutClient) *settlement.Manager {
	t.Helper()
	mgr, err := settlement.NewManager(store, settlement.Config{DefaultRail: settlement.RailNowPayments}, client)
	if err != nil {
		t.Fatalf("new settlement manager: %v", err)
	}
	return mgr
}

// TestRedeemWatcherHappyPath exercises the full discovered -> initiating ->
// (operator confirms settlement) -> attesting -> attested lifecycle for a
// single, valid redemption request.
func TestRedeemWatcherHappyPath(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-123"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)

	const requestID = "req-happy-1"
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "400000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: testValidTRC20Address,
		Status:             "pending",
		CreatedAt:          1700000000,
	})

	ctx := context.Background()

	// Tick 1: discover, validate, initiate settlement (fake payout client
	// succeeds -> Submitted).
	watcher.runOnce(ctx)

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row == nil {
		t.Fatalf("expected row to be discovered and processed")
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status initiating after tick 1, got %s", row.LocalStatus)
	}
	if row.PayoutAmountDecimal != "400" || row.PayoutAmountUnits != 400_000_000 {
		t.Fatalf("unexpected frozen payout amount: decimal=%s units=%d", row.PayoutAmountDecimal, row.PayoutAmountUnits)
	}
	if row.SettlementID == "" {
		t.Fatalf("expected settlement id to be recorded")
	}
	if payoutClient.callCount() != 1 {
		t.Fatalf("expected exactly one payout call, got %d", payoutClient.callCount())
	}
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation before settlement is confirmed settled, got %d calls", node.attestCallCount())
	}

	// Tick 2: settlement is still only Submitted (NOWPayments 2FA not yet
	// confirmed) -- nothing should change.
	watcher.runOnce(ctx)
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status to remain initiating while settlement is only submitted, got %s", row.LocalStatus)
	}
	if node.attestCallCount() != 0 {
		t.Fatalf("expected still no attestation, got %d calls", node.attestCallCount())
	}

	// Operator confirms the payout cleared (the manual step NOWPayments'
	// email 2FA requires).
	if _, err := settlementMgr.ConfirmSettled(ctx, row.SettlementID, settlement.Receipt{Reference: "wire-confirmed-1", Operator: "ops"}); err != nil {
		t.Fatalf("confirm settled: %v", err)
	}

	// Tick 3: settlement is now Settled -> attest paid.
	watcher.runOnce(ctx)
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation call, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	// The attested payoutReference is the settlement's ExternalRef at the
	// moment it reached Settled -- settlement.Manager.ConfirmSettled
	// overwrites ExternalRef with the operator's confirmed reference
	// ("wire-confirmed-1"), superseding CreatePayout's original batch id
	// ("batch-123"). That's correct: the on-chain attestation should point
	// at the real, operator-verified evidence, not the earlier
	// submitted-but-unconfirmed batch reference.
	if call.RequestID != requestID || call.Status != redemptionOutcomePaid || call.PayoutReference != "wire-confirmed-1" {
		t.Fatalf("unexpected attest call: %+v", call)
	}
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected status attesting after attestation submitted, got %s", row.LocalStatus)
	}
	if row.AttestTxHash == "" {
		t.Fatalf("expected attest tx hash to be recorded")
	}

	// Tick 4: receipt not yet confirmed -- stays attesting.
	watcher.runOnce(ctx)
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected status to remain attesting before receipt confirms, got %s", row.LocalStatus)
	}

	// Confirm the attestation transaction landed on-chain.
	node.confirmReceipt(row.AttestTxHash)
	watcher.runOnce(ctx)
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttested {
		t.Fatalf("expected status attested after receipt confirmed, got %s", row.LocalStatus)
	}
	if row.Outcome != redemptionOutcomePaid {
		t.Fatalf("expected outcome paid, got %s", row.Outcome)
	}

	// One more tick must not re-attest or re-initiate anything.
	watcher.runOnce(ctx)
	if node.attestCallCount() != 1 {
		t.Fatalf("expected no additional attestation calls once attested, got %d", node.attestCallCount())
	}
	if payoutClient.callCount() != 1 {
		t.Fatalf("expected no additional payout calls once attested, got %d", payoutClient.callCount())
	}
}

// TestNowPaymentsPayoutCurrency is the regression test for a real production
// incident: two separate real redemptions were rejected by NOWPayments with
// "Invalid payout address" against perfectly valid TRC20 addresses, because
// the bare "USDT" label on-chain redemption requests use isn't a real
// NOWPayments currency code -- every stablecoin there is network-qualified.
func TestNowPaymentsPayoutCurrency(t *testing.T) {
	cases := []struct{ in, want string }{
		{"USDT", "USDTTRC20"},
		{"usdt", "USDTTRC20"},
		{"  USDT  ", "USDTTRC20"},
		{"USDTTRC20", "USDTTRC20"},
		{"ZNHB", "ZNHB"},
	}
	for _, tc := range cases {
		if got := nowPaymentsPayoutCurrency(tc.in); got != tc.want {
			t.Errorf("nowPaymentsPayoutCurrency(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fakeStatusChecker is a controllable PayoutStatusChecker: tests set what
// GetPayoutStatus returns for a given external ref, or force an error.
type fakeStatusChecker struct {
	mu      sync.Mutex
	byRef   map[string]string
	err     error
	queries []string
}

func newFakeStatusChecker() *fakeStatusChecker {
	return &fakeStatusChecker{byRef: make(map[string]string)}
}

func (f *fakeStatusChecker) GetPayoutStatus(ctx context.Context, externalRef string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, externalRef)
	if f.err != nil {
		return "", f.err
	}
	return f.byRef[externalRef], nil
}

func (f *fakeStatusChecker) setStatus(ref, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRef[ref] = status
}

func (f *fakeStatusChecker) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

// TestRedeemWatcherAutoPollConfirmsFinished exercises the fully-automated
// path a TOTP-configured deployment relies on: no operator ever calls
// confirm-payout by hand, the watcher's own poll against NOWPayments'
// real status is what drives Submitted -> Settled -> on-chain attestation.
func TestRedeemWatcherAutoPollConfirmsFinished(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-auto-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	checker := newFakeStatusChecker()
	watcher := NewRedeemWatcher(store, node, settlementMgr, checker, time.Second)

	const requestID = "req-auto-1"
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "400000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})
	ctx := context.Background()

	watcher.runOnce(ctx) // discover -> initiate -> Submitted
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation before poll reports FINISHED, got %d", node.attestCallCount())
	}

	// Still in flight on NOWPayments' side -- must not confirm or attest yet.
	checker.setStatus("batch-auto-1", "PROCESSING")
	watcher.runOnce(ctx)
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation while status is PROCESSING, got %d", node.attestCallCount())
	}
	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status to remain initiating while PROCESSING, got %s", row.LocalStatus)
	}

	// NOWPayments finishes the payout for real -- next poll must auto-confirm
	// and attest paid, with zero operator action.
	checker.setStatus("batch-auto-1", "FINISHED")
	watcher.runOnce(ctx)
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation after FINISHED, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	if call.RequestID != requestID || call.Status != redemptionOutcomePaid || call.PayoutReference != "batch-auto-1" {
		t.Fatalf("unexpected attest call: %+v", call)
	}
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected status attesting after auto-confirm, got %s", row.LocalStatus)
	}
}

// TestRedeemWatcherAutoPollFailsRejected confirms a batch NOWPayments itself
// rejects (e.g. an unverified 2FA window expiring) is automatically attested
// failed on-chain, without requiring an operator to notice and call
// fail-payout by hand.
func TestRedeemWatcherAutoPollFailsRejected(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-auto-2"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	checker := newFakeStatusChecker()
	watcher := NewRedeemWatcher(store, node, settlementMgr, checker, time.Second)

	const requestID = "req-auto-2"
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "18000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})
	ctx := context.Background()

	watcher.runOnce(ctx) // discover -> initiate -> Submitted

	checker.setStatus("batch-auto-2", "REJECTED")
	watcher.runOnce(ctx)
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation after REJECTED, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	if call.RequestID != requestID || call.Status != redemptionOutcomeFailed {
		t.Fatalf("unexpected attest call: %+v", call)
	}
	if !strings.Contains(call.FailureReason, "REJECTED") {
		t.Fatalf("expected failure reason to mention the real NOWPayments status, got %q", call.FailureReason)
	}
}

// TestRedeemWatcherAutoPollErrorLeavesRowUnchanged confirms a transient
// polling error (network blip, NOWPayments outage) never mis-attests --
// the row must simply be retried next tick, exactly like every other
// network-error path in this watcher.
func TestRedeemWatcherAutoPollErrorLeavesRowUnchanged(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-auto-3"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	checker := newFakeStatusChecker()
	checker.err = errors.New("network blip")
	watcher := NewRedeemWatcher(store, node, settlementMgr, checker, time.Second)

	const requestID = "req-auto-3"
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "18000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})
	ctx := context.Background()

	watcher.runOnce(ctx) // discover -> initiate -> Submitted
	watcher.runOnce(ctx) // poll errors
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation when the status poll errors, got %d", node.attestCallCount())
	}
	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status to remain initiating after a polling error, got %s", row.LocalStatus)
	}
	if checker.queryCount() == 0 {
		t.Fatalf("expected the status checker to have been queried")
	}
}

// TestRedeemWatcherRetryPayoutRejectsAlreadyAttested is the regression test
// for a real production-blocking finding: RetryPayout must never be
// callable once a redemption's outcome has already been submitted on-chain
// (attesting or attested) -- doing so would create a brand new, real
// NOWPayments payout with no way to ever attest it (TxTypeAttestRedemption
// allows exactly one terminal transition, ever), permanently orphaning real
// money.
func TestRedeemWatcherRetryPayoutRejectsAlreadyAttested(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{fail: true, failMsg: "first attempt rejected"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)

	const requestID = "req-no-double-retry"
	node.setPending(RedemptionRequest{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "18000000000000000000",
		DestinationAsset: "usdttrc20", DestinationAddress: testValidTRC20Address, Status: "pending", CreatedAt: 1700000000,
	})
	ctx := context.Background()

	// Tick 1: discover -> initiate -> payout fails synchronously -> Failed ->
	// attested failed on-chain immediately (processDiscovered's inline path).
	watcher.runOnce(ctx)
	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected status attesting after an immediate payout failure, got %s", row.LocalStatus)
	}
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation call, got %d", node.attestCallCount())
	}

	// An operator (or a confused script) tries to retry the payout now that
	// it's already been attested failed on-chain. This must be rejected --
	// allowing it would spend real money a second time with no way to ever
	// record the outcome on-chain.
	if _, err := watcher.RetryPayout(ctx, requestID); err == nil {
		t.Fatalf("expected RetryPayout to reject a request already in status %q", redemptionStatusAttesting)
	}
	if payoutClient.callCount() != 1 {
		t.Fatalf("expected no additional payout call from the rejected retry, got %d total calls", payoutClient.callCount())
	}

	// Confirm the same rejection holds once the row reaches the fully
	// terminal "attested" status.
	node.confirmReceipt(row.AttestTxHash)
	watcher.runOnce(ctx)
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttested {
		t.Fatalf("expected status attested, got %s", row.LocalStatus)
	}
	if _, err := watcher.RetryPayout(ctx, requestID); err == nil {
		t.Fatalf("expected RetryPayout to reject a request already in status %q", redemptionStatusAttested)
	}
	if _, err := watcher.FailPayout(ctx, requestID, "operator mistake"); err == nil {
		t.Fatalf("expected FailPayout to reject a request already in status %q", redemptionStatusAttested)
	}
	if _, err := watcher.ConfirmPayout(ctx, requestID, settlement.Receipt{Reference: "x", Operator: "ops"}); err == nil {
		t.Fatalf("expected ConfirmPayout to reject a request already in status %q", redemptionStatusAttested)
	}
	if payoutClient.callCount() != 1 {
		t.Fatalf("expected still no additional payout call, got %d total calls", payoutClient.callCount())
	}
}

// TestRedeemWatcherRetryPayoutSucceedsFromStuckManualReview confirms the
// intended, safe use of RetryPayout: a row Recover() parked in
// stuck_manual_review after a crash (before any on-chain attestation was
// ever submitted for it) can be retried once an operator has manually
// verified it's safe to do so, and afterward resumes normal tick handling.
func TestRedeemWatcherRetryPayoutSucceedsFromStuckManualReview(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-recovered"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	const requestID = "req-crashed-retry"
	// A settlement that reached Submitted (real batch created), with no
	// on-chain attestation yet -- exactly what Recover() finds and parks in
	// stuck_manual_review after a crash mid-flow.
	rec, err := settlementMgr.Initiate(ctx, settlement.InitiateRequest{
		IntentID: requestID, ReservationID: requestID, Asset: "USDT", AmountUnits: 18_000_000, Account: testValidTRC20Address,
	})
	if err != nil {
		t.Fatalf("initiate settlement: %v", err)
	}
	if rec.Status != string(settlement.StatusSubmitted) {
		t.Fatalf("expected submitted settlement, got %s", rec.Status)
	}
	if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
		RequestID: requestID, Account: "nhb1testaccount", NHBAmountWei: "18000000000000000000",
		DestinationAsset: "USDT", DestinationAddress: testValidTRC20Address,
		LocalStatus: redemptionStatusStuckManualReview, SettlementID: rec.ID,
		PayoutAmountDecimal: "18", PayoutAmountUnits: 18_000_000,
		CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatalf("insert stuck row: %v", err)
	}

	// Operator has manually verified the original batch never actually paid
	// out (e.g. checked the NOWPayments dashboard) and closes it out first.
	if _, err := watcher.FailPayout(ctx, requestID, "operator verified original batch never settled"); err != nil {
		t.Fatalf("fail payout: %v", err)
	}
	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected FailPayout to resume normal flow (status initiating), got %s", row.LocalStatus)
	}

	// Now retry -- this must be permitted (settlement is Failed, row is back
	// to initiating) and must actually submit a fresh payout.
	if _, err := watcher.RetryPayout(ctx, requestID); err != nil {
		t.Fatalf("retry payout: %v", err)
	}
	if payoutClient.callCount() != 2 {
		t.Fatalf("expected exactly 2 total payout calls (original + retry), got %d", payoutClient.callCount())
	}
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusInitiating {
		t.Fatalf("expected status initiating after retry so the watcher's own tick resumes handling it, got %s", row.LocalStatus)
	}

	// The watcher's normal tick machinery now picks the retried settlement
	// up exactly like a first attempt.
	watcher.runOnce(ctx)
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation yet -- the retried settlement is only Submitted, not confirmed, got %d attest calls", node.attestCallCount())
	}
}

// TestRedeemWatcherInvalidDestinationAddressSkipsPayout is the safety
// property that an unpayable address never reaches the payout API at all --
// it should be attested failed directly.
func TestRedeemWatcherInvalidDestinationAddressSkipsPayout(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "should-not-be-used"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)

	const requestID = "req-bad-address-1"
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "100000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: "not-a-real-trc20-address",
		Status:             "pending",
		CreatedAt:          1700000000,
	})

	ctx := context.Background()
	watcher.runOnce(ctx)

	if payoutClient.callCount() != 0 {
		t.Fatalf("expected the payout API to never be called for an invalid address, got %d calls", payoutClient.callCount())
	}
	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation call, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	if call.Status != redemptionOutcomeFailed {
		t.Fatalf("expected failed attestation, got status=%s", call.Status)
	}
	if !strings.Contains(call.FailureReason, "invalid destination address") {
		t.Fatalf("expected failure reason to mention invalid destination address, got %q", call.FailureReason)
	}

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.SettlementID != "" {
		t.Fatalf("expected no settlement to have been created, got settlement id %q", row.SettlementID)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected status attesting (attestation submitted, awaiting receipt), got %s", row.LocalStatus)
	}
}

// TestRedeemWatcherRecoverMovesInitiatingToStuckManualReview is the most
// safety-critical behavior in the whole feature: a row left in "initiating"
// by a process that crashed mid-payout must never be silently auto-resumed
// (which could double-pay a redeemer if the earlier attempt actually
// succeeded) and must never be silently dropped (which could leave a real,
// already-paid-out request permanently unattested). It must land in
// stuck_manual_review and stay there untouched by the normal ticker loop.
func TestRedeemWatcherRecoverMovesInitiatingToStuckManualReview(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-should-not-be-called-again"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	ctx := context.Background()

	const requestID = "req-crashed-1"
	now := time.Now().UTC()

	// Simulate a previous process instance that had already: discovered the
	// request, validated it, and committed to "initiating" (durably, per
	// processDiscovered's ordering) -- then crashed before the settlement
	// ever resolved to Settled/Failed and before any attestation was ever
	// submitted. A real settlement row exists in Submitted state (as if the
	// payout API call itself had actually succeeded and the crash happened
	// after that).
	settlementRecord, err := settlementMgr.Initiate(ctx, settlement.InitiateRequest{
		IntentID:      requestID,
		ReservationID: requestID,
		PartnerID:     redemptionSettlementPartnerID,
		Asset:         "USDTTRC20",
		AmountUnits:   100_000_000,
		Account:       testValidTRC20Address,
	})
	if err != nil {
		t.Fatalf("seed settlement: %v", err)
	}
	if settlementRecord.Status != string(settlement.StatusSubmitted) {
		t.Fatalf("expected seeded settlement to be submitted, got %s", settlementRecord.Status)
	}
	if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
		RequestID:           requestID,
		Account:             "nhb1testaccount",
		NHBAmountWei:        "100000000000000000000",
		PayoutAmountDecimal: "100",
		PayoutAmountUnits:   100_000_000,
		DestinationAsset:    "USDTTRC20",
		DestinationAddress:  testValidTRC20Address,
		LocalStatus:         redemptionStatusInitiating,
		SettlementID:        settlementRecord.ID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("seed crashed row: %v", err)
	}

	// New watcher instance -- simulating the restarted process.
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	if err := watcher.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusStuckManualReview {
		t.Fatalf("expected stuck_manual_review after recovery, got %s", row.LocalStatus)
	}

	// The chain still shows this request as pending (it is -- the burn
	// happened, nothing has attested it yet). Run several ticks: the row
	// must never be re-discovered, re-initiated, or auto-attested.
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "100000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: testValidTRC20Address,
		Status:             "pending",
		CreatedAt:          1700000000,
	})
	// Also mark the underlying settlement Settled, as if the earlier payout
	// really had gone through -- proving even a genuinely-Settled
	// settlement behind a stuck row is not auto-attested by the normal tick
	// path once Recover has moved it out of "initiating".
	if _, err := settlementMgr.ConfirmSettled(ctx, settlementRecord.ID, settlement.Receipt{Reference: "wire-x", Operator: "ops"}); err != nil {
		t.Fatalf("confirm settled: %v", err)
	}

	for i := 0; i < 3; i++ {
		watcher.runOnce(ctx)
	}

	if payoutClient.callCount() != 1 {
		// The single call above is the seeded Initiate call; runOnce must
		// never have triggered a second one.
		t.Fatalf("expected no additional payout calls after recovery, got %d total calls", payoutClient.callCount())
	}
	if node.attestCallCount() != 0 {
		t.Fatalf("expected no attestation calls for a stuck_manual_review row, got %d", node.attestCallCount())
	}
	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusStuckManualReview {
		t.Fatalf("expected row to remain stuck_manual_review across ticks, got %s", row.LocalStatus)
	}
}

// TestRedeemWatcherReconcileStuckManualReview covers the CORRECTED
// (post-security-audit) behavior: reconcileStuckManualReview NEVER takes an
// on-chain-affecting action automatically, no matter how old a row is or
// how clean its "pending, no external ref" signal looks -- that signal
// cannot actually distinguish "no payout was ever dispatched" from "a real,
// already-verified payout's post-success persist failed or was
// interrupted" (see settlement.Manager.submittedLocked's doc comment), so
// auto-firing MarkFailed here was a real double-credit bug an adversarial
// security audit found before this ever reached production. This test
// pins the corrected behavior: detection and alerting only, zero
// MarkFailed/attestation/payout calls, regardless of case.
func TestRedeemWatcherReconcileStuckManualReview(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	watcher.WithStuckReviewSafetyMargin(2 * time.Hour)
	ctx := context.Background()

	fixedNow := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	watcher.nowFn = func() time.Time { return fixedNow }

	seedStuckRow := func(t *testing.T, requestID string, settlementAge time.Duration, settlementStatus settlement.Status, externalRef string) {
		t.Helper()
		createdAt := fixedNow.Add(-settlementAge)
		settlementID := "settle-" + requestID
		if err := store.SaveSettlement(ctx, swapdstorage.SettlementRecord{
			ID:            settlementID,
			IntentID:      requestID,
			ReservationID: requestID,
			PartnerID:     redemptionSettlementPartnerID,
			Asset:         "USDTTRC20",
			AmountUnits:   100_000_000,
			Account:       testValidTRC20Address,
			Rail:          string(settlement.RailNowPayments),
			Status:        string(settlementStatus),
			ExternalRef:   externalRef,
			Detail:        "{}",
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		}); err != nil {
			t.Fatalf("seed settlement for %s: %v", requestID, err)
		}
		if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
			RequestID:           requestID,
			Account:             "nhb1testaccount",
			NHBAmountWei:        "100000000000000000000",
			PayoutAmountDecimal: "100",
			PayoutAmountUnits:   100_000_000,
			DestinationAsset:    "USDTTRC20",
			DestinationAddress:  testValidTRC20Address,
			LocalStatus:         redemptionStatusStuckManualReview,
			SettlementID:        settlementID,
			CreatedAt:           createdAt,
			UpdatedAt:           createdAt,
		}); err != nil {
			t.Fatalf("seed stuck row for %s: %v", requestID, err)
		}
	}

	// Case 1: aged well past the margin, settlement still exactly Pending
	// with no external ref -- the safe-to-auto-resolve case.
	const requestIDSafe = "req-stuck-safe"
	seedStuckRow(t, requestIDSafe, 3*time.Hour, settlement.StatusPending, "")

	// Case 2: aged past the margin, but the settlement DOES have an
	// external ref (CreatePayout's HTTP call actually completed) -- must
	// never be auto-resolved, no matter how old.
	const requestIDAmbiguous = "req-stuck-ambiguous"
	seedStuckRow(t, requestIDAmbiguous, 10*time.Hour, settlement.StatusPending, "batch-real-123")

	// Case 3: still Pending with no external ref, but too recent -- within
	// the window a genuinely in-flight CreatePayout call could still be
	// running.
	const requestIDTooRecent = "req-stuck-recent"
	seedStuckRow(t, requestIDTooRecent, 5*time.Minute, settlement.StatusPending, "")

	watcher.runOnce(ctx)

	// ALL THREE cases must remain exactly stuck_manual_review -- this method
	// must never transition any row's LocalStatus, regardless of how old or
	// "clean-looking" its signal is.
	for _, id := range []string{requestIDSafe, requestIDAmbiguous, requestIDTooRecent} {
		row, err := store.GetRedemptionWatch(ctx, id)
		if err != nil {
			t.Fatalf("get row %s: %v", id, err)
		}
		if row.LocalStatus != redemptionStatusStuckManualReview {
			t.Fatalf("expected %s to remain stuck_manual_review, got %s", id, row.LocalStatus)
		}
	}

	// Zero attestation calls, zero real payout calls -- this method must
	// NEVER take an on-chain or money-moving action by itself.
	if node.attestCallCount() != 0 {
		t.Fatalf("expected zero attestation calls -- reconcileStuckManualReview must never auto-attest, got %d", node.attestCallCount())
	}
	if payoutClient.callCount() != 0 {
		t.Fatalf("expected zero real payout calls, got %d", payoutClient.callCount())
	}

	// The settlement backing the safe case must still be exactly Pending --
	// MarkFailed must NEVER be called automatically here.
	safeSettlement, err := store.GetSettlement(ctx, "settle-"+requestIDSafe)
	if err != nil {
		t.Fatalf("get safe settlement: %v", err)
	}
	if safeSettlement.Status != string(settlement.StatusPending) {
		t.Fatalf("expected safe case settlement to remain Pending (no auto-action), got %s", safeSettlement.Status)
	}
	ambiguousSettlement, err := store.GetSettlement(ctx, "settle-"+requestIDAmbiguous)
	if err != nil {
		t.Fatalf("get ambiguous settlement: %v", err)
	}
	if ambiguousSettlement.Status != string(settlement.StatusPending) || ambiguousSettlement.ExternalRef != "batch-real-123" {
		t.Fatalf("expected ambiguous case settlement to remain untouched (no auto-action), got %+v", ambiguousSettlement)
	}

	// Both the safe case (past margin, pending, no ref) AND the ambiguous
	// case (has a real external ref -- the higher-stakes one, per the
	// follow-up fix) must be alerted on; only the too-recent case (still
	// within the possible in-flight window) must not be alerted yet.
	if _, alerted := watcher.stuckReviewAlerted[requestIDSafe]; !alerted {
		t.Fatalf("expected the safe case to be recorded as alerted")
	}
	if _, alerted := watcher.stuckReviewAlerted[requestIDAmbiguous]; !alerted {
		t.Fatalf("expected the ambiguous case (real external ref) to ALSO be alerted on -- it must never fall silent")
	}
	if _, alerted := watcher.stuckReviewAlerted[requestIDTooRecent]; alerted {
		t.Fatalf("expected the too-recent case to never be alerted on yet")
	}
	firstAlertTime := watcher.stuckReviewAlerted[requestIDSafe]

	// A second tick at the same fixed time must not re-alert (within
	// stuckReviewAlertInterval) and must still take zero action.
	watcher.runOnce(ctx)
	if node.attestCallCount() != 0 || payoutClient.callCount() != 0 {
		t.Fatalf("expected still zero attestation/payout calls after a second tick")
	}
	if watcher.stuckReviewAlerted[requestIDSafe] != firstAlertTime {
		t.Fatalf("expected no re-alert within stuckReviewAlertInterval")
	}

	// Advancing time past stuckReviewAlertInterval must re-alert (the row is
	// still unresolved -- an operator hasn't acted on it), but still take no
	// action.
	laterNow := fixedNow.Add(stuckReviewAlertInterval + time.Minute)
	watcher.nowFn = func() time.Time { return laterNow }
	watcher.runOnce(ctx)
	if node.attestCallCount() != 0 || payoutClient.callCount() != 0 {
		t.Fatalf("expected still zero attestation/payout calls even after the alert-interval elapses")
	}
	if watcher.stuckReviewAlerted[requestIDSafe] != laterNow {
		t.Fatalf("expected a fresh alert timestamp once stuckReviewAlertInterval elapses, got %v want %v", watcher.stuckReviewAlerted[requestIDSafe], laterNow)
	}

	// Once an operator actually resolves a row (the real, human-triggered
	// admin action -- unaffected by anything in this diff), its entry must
	// be pruned from the alert map rather than lingering forever.
	if _, err := settlementMgr.MarkFailed(ctx, "settle-"+requestIDSafe, "operator confirmed no payout was ever sent"); err != nil {
		t.Fatalf("mark failed via operator action: %v", err)
	}
	resolvedRow, err := store.GetRedemptionWatch(ctx, requestIDSafe)
	if err != nil {
		t.Fatalf("get resolved row: %v", err)
	}
	resolvedRow.LocalStatus = redemptionStatusInitiating // simulates resumeNormalFlow, as FailPayout would do via the admin endpoint
	resolvedRow.UpdatedAt = laterNow
	if err := store.UpdateRedemptionWatch(ctx, *resolvedRow); err != nil {
		t.Fatalf("resume resolved row: %v", err)
	}
	watcher.runOnce(ctx)
	if _, stillAlerted := watcher.stuckReviewAlerted[requestIDSafe]; stillAlerted {
		t.Fatalf("expected the resolved row's alert entry to be pruned once it left stuck_manual_review")
	}
	// The still-unresolved ambiguous case must remain in the map.
	if _, alerted := watcher.stuckReviewAlerted[requestIDAmbiguous]; !alerted {
		t.Fatalf("expected the still-unresolved ambiguous case to remain in the alert map")
	}
}

// fakeNotifier is a controllable RedemptionNotifier: records every event it
// receives, and can be configured to fail every call (to prove a failing
// notifier never blocks or reverses the watcher's own state machine).
type fakeNotifier struct {
	mu     sync.Mutex
	events []RedemptionOutcomeEvent
	fail   bool
}

func (f *fakeNotifier) Notify(ctx context.Context, event RedemptionOutcomeEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	if f.fail {
		return errors.New("simulated notify failure")
	}
	return nil
}

func (f *fakeNotifier) recordedEvents() []RedemptionOutcomeEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RedemptionOutcomeEvent, len(f.events))
	copy(out, f.events)
	return out
}

// TestRedeemWatcherNotifiesOnConfirmedAttestation covers the customer-email
// hook: once an attestation transaction is confirmed on-chain, the
// configured notifier must be called exactly once with the right outcome
// (including Refunded=true for a failed redemption, since this deployment's
// on-chain change always refunds a failed one), and a failing notifier must
// never prevent the row from reaching redemptionStatusAttested or affect
// any subsequent tick.
func TestRedeemWatcherNotifiesOnConfirmedAttestation(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-notify-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	notifier := &fakeNotifier{fail: true}
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	watcher.WithNotifier(notifier)

	const requestID = "req-notify-1"
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "400000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: testValidTRC20Address,
		Status:             "pending",
		CreatedAt:          1700000000,
	})

	ctx := context.Background()
	watcher.runOnce(ctx) // discover -> initiate (Submitted)

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if _, err := settlementMgr.MarkFailed(ctx, row.SettlementID, "nowpayments payout status: REJECTED"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	watcher.runOnce(ctx) // initiating -> attest failed (submits attestation)

	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttesting {
		t.Fatalf("expected attesting after failed settlement is attested, got %s", row.LocalStatus)
	}
	if len(notifier.recordedEvents()) != 0 {
		t.Fatalf("expected no notify call before the attestation tx is confirmed, got %d", len(notifier.recordedEvents()))
	}

	// Confirm the attestation transaction landed on-chain -- THIS is when
	// notify should fire, despite the notifier being configured to fail.
	node.confirmReceipt(row.AttestTxHash)
	watcher.runOnce(ctx)

	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusAttested {
		t.Fatalf("expected attested even though the notifier fails, got %s", row.LocalStatus)
	}

	events := notifier.recordedEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one notify call, got %d", len(events))
	}
	event := events[0]
	if event.RequestID != requestID {
		t.Fatalf("unexpected requestId in notify event: %+v", event)
	}
	if event.Outcome != redemptionOutcomeFailed {
		t.Fatalf("expected outcome failed in notify event, got %s", event.Outcome)
	}
	if !event.Refunded {
		t.Fatalf("expected Refunded=true for a failed outcome, got false: %+v", event)
	}

	// One more tick must not re-notify (already attested, filtered out of
	// processAttesting's status query) and must not have been disrupted by
	// the earlier notify failure.
	watcher.runOnce(ctx)
	if len(notifier.recordedEvents()) != 1 {
		t.Fatalf("expected no additional notify calls on a later tick, got %d", len(notifier.recordedEvents()))
	}
}

// TestRedeemWatcherNotifiesPaidWithRefundedFalse covers the mirror-image
// case of TestRedeemWatcherNotifiesOnConfirmedAttestation: a successful
// ("paid") redemption must notify with Refunded=false and the real
// PayoutReference -- guarding against a future regression that marks every
// outcome refunded regardless of status.
func TestRedeemWatcherNotifiesPaidWithRefundedFalse(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-paid-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	notifier := &fakeNotifier{}
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	watcher.WithNotifier(notifier)

	const requestID = "req-notify-paid-1"
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "400000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: testValidTRC20Address,
		Status:             "pending",
		CreatedAt:          1700000000,
	})

	ctx := context.Background()
	watcher.runOnce(ctx) // discover -> initiate (Submitted)

	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if _, err := settlementMgr.ConfirmSettled(ctx, row.SettlementID, settlement.Receipt{Reference: "wire-confirmed-paid", Operator: "ops"}); err != nil {
		t.Fatalf("confirm settled: %v", err)
	}

	watcher.runOnce(ctx) // initiating -> attest paid

	row, err = store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	node.confirmReceipt(row.AttestTxHash)
	watcher.runOnce(ctx) // attesting -> attested, fires notify

	events := notifier.recordedEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one notify call, got %d", len(events))
	}
	event := events[0]
	if event.Outcome != redemptionOutcomePaid {
		t.Fatalf("expected outcome paid, got %s", event.Outcome)
	}
	if event.Refunded {
		t.Fatalf("expected Refunded=false for a paid outcome, got true: %+v", event)
	}
	if event.PayoutReference != "wire-confirmed-paid" {
		t.Fatalf("expected the real payout reference in the notify event, got %+v", event)
	}
}

// TestRedeemWatcherSkipsRequestNoLongerPending covers the fresh-re-read
// race guard: if a request is no longer pending by the time processDiscovered
// gets to it (e.g. another watcher instance already handled it), it must be
// marked skipped_already_settled rather than having a second payout
// attempted for it.
func TestRedeemWatcherSkipsRequestNoLongerPending(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{ref: "batch-1"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	const requestID = "req-race-1"
	now := time.Now().UTC()
	if err := store.InsertRedemptionWatch(ctx, RedemptionWatchRecord{
		RequestID:           requestID,
		Account:             "nhb1testaccount",
		NHBAmountWei:        "100000000000000000000",
		PayoutAmountDecimal: "100",
		PayoutAmountUnits:   100_000_000,
		DestinationAsset:    "USDTTRC20",
		DestinationAddress:  testValidTRC20Address,
		LocalStatus:         redemptionStatusDiscovered,
		CreatedAt:           now,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("seed discovered row: %v", err)
	}
	// The chain's pending list no longer contains this request by the time
	// processDiscovered's fresh re-read runs.
	node.setPending()

	watcher.runOnce(ctx)

	if payoutClient.callCount() != 0 {
		t.Fatalf("expected no payout call once the request is no longer pending, got %d", payoutClient.callCount())
	}
	row, err := store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		t.Fatalf("get redemption watch: %v", err)
	}
	if row.LocalStatus != redemptionStatusSkippedAlreadySettled {
		t.Fatalf("expected skipped_already_settled, got %s", row.LocalStatus)
	}
}

// TestRedeemWatcherFailedSettlementAttestsFailed covers the payout-API
// rejection path: Manager.Initiate reaching StatusFailed must lead straight
// to an on-chain failed attestation.
func TestRedeemWatcherFailedSettlementAttestsFailed(t *testing.T) {
	store := newTestRedeemStore(t)
	payoutClient := &fakePayoutClient{fail: true, failMsg: "insufficient custody balance"}
	settlementMgr := newTestSettlementManager(t, store, payoutClient)
	node := newFakeRedeemNode()
	watcher := NewRedeemWatcher(store, node, settlementMgr, nil, time.Second)
	ctx := context.Background()

	const requestID = "req-payout-fails-1"
	node.setPending(RedemptionRequest{
		RequestID:          requestID,
		Account:            "nhb1testaccount",
		NHBAmountWei:       "250000000000000000000",
		DestinationAsset:   "usdttrc20",
		DestinationAddress: testValidTRC20Address,
		Status:             "pending",
		CreatedAt:          1700000000,
	})

	watcher.runOnce(ctx) // discover, validate, initiate (fails) -> attest failed

	if node.attestCallCount() != 1 {
		t.Fatalf("expected exactly one attestation call, got %d", node.attestCallCount())
	}
	call := node.lastAttestCall()
	if call.Status != redemptionOutcomeFailed {
		t.Fatalf("expected failed attestation, got status=%s", call.Status)
	}
	if !strings.Contains(call.FailureReason, "insufficient custody balance") {
		t.Fatalf("expected failure reason to surface the payout error, got %q", call.FailureReason)
	}
}
