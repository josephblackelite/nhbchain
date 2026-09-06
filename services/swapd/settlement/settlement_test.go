package settlement

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/services/swapd/storage"
)

// fakeStore is a minimal in-memory Store for deterministic unit tests.
type fakeStore struct {
	records map[string]storage.SettlementRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[string]storage.SettlementRecord)}
}

func (f *fakeStore) SaveSettlement(ctx context.Context, record storage.SettlementRecord) error {
	f.records[record.ID] = record
	return nil
}

func (f *fakeStore) GetSettlement(ctx context.Context, id string) (storage.SettlementRecord, error) {
	rec, ok := f.records[id]
	if !ok {
		return storage.SettlementRecord{}, errors.New("not found")
	}
	return rec, nil
}

func (f *fakeStore) ListSettlements(ctx context.Context, partnerID, status string, limit int) ([]storage.SettlementRecord, error) {
	var out []storage.SettlementRecord
	for _, rec := range f.records {
		if partnerID != "" && rec.PartnerID != partnerID {
			continue
		}
		if status != "" && rec.Status != status {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// fakePayoutClient lets tests control success/failure deterministically.
type fakePayoutClient struct {
	calls   int
	fail    bool
	failMsg string
	ref     string
}

func (f *fakePayoutClient) CreatePayout(ctx context.Context, req PayoutRequest) (PayoutResult, error) {
	f.calls++
	if f.fail {
		msg := f.failMsg
		if msg == "" {
			msg = "payout rejected"
		}
		return PayoutResult{}, errors.New(msg)
	}
	return PayoutResult{ExternalRef: f.ref}, nil
}

func newTestManager(t *testing.T, store Store, config Config, client PayoutClient) *Manager {
	t.Helper()
	mgr, err := NewManager(store, config, client)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	counter := 0
	mgr.WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	mgr.WithIDFunc(func() string {
		counter++
		return "settle-test-" + string(rune('0'+counter))
	})
	return mgr
}

func TestRailForResolution(t *testing.T) {
	cfg := Config{
		DefaultRail:  RailNowPayments,
		PartnerRails: map[string]Rail{"partner-manual": RailManualTreasury},
	}
	if got := cfg.RailFor("partner-manual"); got != RailManualTreasury {
		t.Fatalf("expected override rail, got %s", got)
	}
	if got := cfg.RailFor("partner-default"); got != RailNowPayments {
		t.Fatalf("expected default rail, got %s", got)
	}
	if got := (Config{}).RailFor("anyone"); got != RailManualTreasury {
		t.Fatalf("expected safe fallback rail, got %s", got)
	}
}

// TestSetPartnerRailAppliesLive confirms SetPartnerRail's whole reason to
// exist: registering (or updating) a partner's rail override takes effect
// on the very next Initiate call, with no restart/reconstruction of Manager
// needed -- e.g. payments-gateway's POST /admin/agents applying a newly
// approved exchange agent's manual_treasury routing immediately.
func TestSetPartnerRailAppliesLive(t *testing.T) {
	store := newFakeStore()
	client := &fakePayoutClient{ref: "batch-1"}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	// Before registering an override, this partner resolves to the default
	// rail (nowpayments) same as any unconfigured partner.
	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "agent-1",
		Asset: "USDT", AmountUnits: 100_000_000, Account: "addr-1",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if record.Rail != string(RailNowPayments) {
		t.Fatalf("expected nowpayments rail before override, got %s", record.Rail)
	}

	mgr.SetPartnerRail("agent-1", RailManualTreasury)

	record2, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-2", ReservationID: "res-2", PartnerID: "agent-1",
		Asset: "USDT", AmountUnits: 50_000_000, Account: "addr-1",
	})
	if err != nil {
		t.Fatalf("initiate after override: %v", err)
	}
	if record2.Rail != string(RailManualTreasury) {
		t.Fatalf("expected manual_treasury rail after SetPartnerRail, got %s", record2.Rail)
	}
	if record2.Status != string(StatusPending) {
		t.Fatalf("expected manual_treasury to stay pending (no automated call), got %s", record2.Status)
	}
	// A client's own CreatePayout call count must not have grown from the
	// manual_treasury Initiate -- only the earlier nowpayments call touched
	// it.
	if client.calls != 1 {
		t.Fatalf("expected exactly 1 CreatePayout call total, got %d", client.calls)
	}

	// A different, never-configured partner is unaffected by agent-1's
	// override.
	record3, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-3", ReservationID: "res-3", PartnerID: "someone-else",
		Asset: "USDT", AmountUnits: 25_000_000, Account: "addr-2",
	})
	if err != nil {
		t.Fatalf("initiate unrelated partner: %v", err)
	}
	if record3.Rail != string(RailNowPayments) {
		t.Fatalf("expected an unrelated partner to still resolve to the default rail, got %s", record3.Rail)
	}
}

// TestSetPartnerRailNilAndBlankAreNoops confirms SetPartnerRail matches this
// package's fail-quiet convention for a nil Manager or blank partner ID
// (mirroring WithClock/WithIDFunc above) rather than panicking.
func TestSetPartnerRailNilAndBlankAreNoops(t *testing.T) {
	var nilMgr *Manager
	nilMgr.SetPartnerRail("agent-1", RailManualTreasury) // must not panic

	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, nil)
	mgr.SetPartnerRail("   ", RailManualTreasury)
	if len(mgr.config.PartnerRails) != 0 {
		t.Fatalf("expected a blank partner id to be a no-op, got %v", mgr.config.PartnerRails)
	}
}

func TestInitiateManualTreasuryStaysPending(t *testing.T) {
	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailManualTreasury}, nil)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "znhb", AmountUnits: 100_000_000, Account: "merchant-1",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if record.Status != string(StatusPending) {
		t.Fatalf("expected pending status, got %s", record.Status)
	}
	if record.Rail != string(RailManualTreasury) {
		t.Fatalf("expected manual_treasury rail, got %s", record.Rail)
	}
	if record.Asset != "ZNHB" {
		t.Fatalf("expected asset normalised to upper case, got %s", record.Asset)
	}
}

func TestInitiateNowPaymentsSuccessReachesSubmitted(t *testing.T) {
	store := newFakeStore()
	client := &fakePayoutClient{ref: "batch-123"}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if record.Status != string(StatusSubmitted) {
		t.Fatalf("expected submitted status, got %s", record.Status)
	}
	if record.ExternalRef != "batch-123" {
		t.Fatalf("expected external ref from payout client, got %s", record.ExternalRef)
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one payout call, got %d", client.calls)
	}
}

func TestInitiateNowPaymentsFailureReachesFailedButRecordPersists(t *testing.T) {
	store := newFakeStore()
	client := &fakePayoutClient{fail: true, failMsg: "insufficient balance"}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err == nil {
		t.Fatalf("expected error from failed payout")
	}
	if record.Status != string(StatusFailed) {
		t.Fatalf("expected failed status, got %s", record.Status)
	}
	// The record must exist in the store even though the call failed --
	// this is the "never lose a record mid-call" guarantee.
	stored, getErr := store.GetSettlement(context.Background(), record.ID)
	if getErr != nil {
		t.Fatalf("expected failed settlement to be persisted: %v", getErr)
	}
	if stored.Status != string(StatusFailed) {
		t.Fatalf("expected persisted status failed, got %s", stored.Status)
	}
}

func TestInitiateNowPaymentsWithoutClientFailsClosed(t *testing.T) {
	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, nil)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if !errors.Is(err, ErrRailNotConfigured) {
		t.Fatalf("expected ErrRailNotConfigured, got %v", err)
	}
	if record.Status != string(StatusFailed) {
		t.Fatalf("expected failed status, got %s", record.Status)
	}
}

func TestConfirmSettledFromPendingAndSubmitted(t *testing.T) {
	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailManualTreasury}, nil)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "merchant-1",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}

	if _, err := mgr.ConfirmSettled(context.Background(), record.ID, Receipt{}); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("expected ErrReceiptRequired for empty reference, got %v", err)
	}

	confirmed, err := mgr.ConfirmSettled(context.Background(), record.ID, Receipt{Reference: "wire-9", Note: "confirmed", Operator: "ops1"})
	if err != nil {
		t.Fatalf("confirm settled: %v", err)
	}
	if confirmed.Status != string(StatusSettled) {
		t.Fatalf("expected settled status, got %s", confirmed.Status)
	}
	if confirmed.SettledAt.IsZero() {
		t.Fatalf("expected settled_at to be set")
	}

	if _, err := mgr.ConfirmSettled(context.Background(), record.ID, Receipt{Reference: "again"}); !errors.Is(err, ErrNotConfirmable) {
		t.Fatalf("expected ErrNotConfirmable for already-settled record, got %v", err)
	}
}

func TestRetryNowPaymentsOnlyFromFailed(t *testing.T) {
	store := newFakeStore()
	client := &fakePayoutClient{fail: true}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err == nil {
		t.Fatalf("expected initiate to fail")
	}
	if record.Status != string(StatusFailed) {
		t.Fatalf("expected failed status, got %s", record.Status)
	}

	client.fail = false
	client.ref = "batch-999"
	retried, err := mgr.RetryNowPayments(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.Status != string(StatusSubmitted) {
		t.Fatalf("expected submitted after retry, got %s", retried.Status)
	}
	if retried.ExternalRef != "batch-999" {
		t.Fatalf("expected updated external ref, got %s", retried.ExternalRef)
	}

	if _, err := mgr.RetryNowPayments(context.Background(), record.ID); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("expected ErrNotRetryable for already-submitted record, got %v", err)
	}
}

func TestRetryNowPaymentsRejectsManualTreasuryRecord(t *testing.T) {
	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailManualTreasury}, nil)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "merchant-1",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if _, err := mgr.RetryNowPayments(context.Background(), record.ID); !errors.Is(err, ErrNotNowPayments) {
		t.Fatalf("expected ErrNotNowPayments, got %v", err)
	}
}

func TestMarkFailedFromPendingOrSubmitted(t *testing.T) {
	store := newFakeStore()
	mgr := newTestManager(t, store, Config{DefaultRail: RailManualTreasury}, nil)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "merchant-1",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	failed, err := mgr.MarkFailed(context.Background(), record.ID, "partner cancelled")
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failed.Status != string(StatusFailed) {
		t.Fatalf("expected failed status, got %s", failed.Status)
	}
	if _, err := mgr.MarkFailed(context.Background(), record.ID, "again"); !errors.Is(err, ErrNotConfirmable) {
		t.Fatalf("expected ErrNotConfirmable for already-failed record, got %v", err)
	}
}

// failingSaveStore wraps a fakeStore and lets tests inject a SaveSettlement
// failure starting at a specific call, to prove the manager preserves the
// in-memory record (rather than discarding it) when persistence fails, and
// (via permanent=false) that a merely-transient failure is recovered by
// submittedLocked's retry rather than needing every subsequent call to
// succeed by luck.
type failingSaveStore struct {
	*fakeStore
	failOnCall int
	permanent  bool // if true, every call >= failOnCall fails; if false, only call == failOnCall fails
	calls      int
}

func (f *failingSaveStore) SaveSettlement(ctx context.Context, record storage.SettlementRecord) error {
	f.calls++
	if f.permanent {
		if f.calls >= f.failOnCall {
			return errors.New("simulated storage failure")
		}
	} else if f.calls == f.failOnCall {
		return errors.New("simulated storage failure")
	}
	return f.fakeStore.SaveSettlement(ctx, record)
}

// TestInitiateNowPaymentsTransientPersistFailureRecoveredByRetry covers the
// double-credit gap an external security audit found in this package's
// earlier behavior: a single transient SaveSettlement failure immediately
// after a real, already-verified NOWPayments payout used to leave the
// settlement record permanently stuck at Pending/no-ExternalRef --
// indistinguishable from "CreatePayout was never even called" to any
// downstream reconciler (see reconcileStuckManualReview in
// services/payments-gateway/redeem_watcher.go), which could then wrongly
// conclude no payout occurred and trigger an on-chain refund on top of a
// real payout. submittedLocked's retry (this package) exists specifically
// to shrink this window: a single transient failure must now be recovered
// transparently, with no error and the record durably persisted as
// Submitted.
func TestInitiateNowPaymentsTransientPersistFailureRecoveredByRetry(t *testing.T) {
	store := &failingSaveStore{fakeStore: newFakeStore(), failOnCall: 2} // 1st save = pending, 2nd = post-payout submitted (fails once, then recovers)
	client := &fakePayoutClient{ref: "batch-already-sent"}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err != nil {
		t.Fatalf("expected the retry to recover from a single transient persist failure, got error: %v", err)
	}
	if record.ExternalRef != "batch-already-sent" || record.Status != string(StatusSubmitted) {
		t.Fatalf("expected returned record to reflect the submitted payout, got: %+v", record)
	}
	// Must actually be durably persisted this time, not just returned in memory.
	persisted, err := store.GetSettlement(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("load persisted settlement: %v", err)
	}
	if persisted.Status != string(StatusSubmitted) || persisted.ExternalRef != "batch-already-sent" {
		t.Fatalf("expected persisted record to reflect submitted+external ref after retry, got: %+v", persisted)
	}
}

// TestInitiateNowPaymentsPersistFailureAfterSuccessfulPayoutPreservesExternalRef
// covers the case the retry cannot fix: persistence keeps failing for every
// attempt (a genuinely down/broken local store, not a blip). The CRITICAL
// error path must still trigger, with the external ref preserved in the
// error text and the in-memory record still returned (not a zero value) --
// exactly as before submittedLocked gained a retry, just after exhausting
// it instead of on the first attempt.
func TestInitiateNowPaymentsPersistFailureAfterSuccessfulPayoutPreservesExternalRef(t *testing.T) {
	store := &failingSaveStore{fakeStore: newFakeStore(), failOnCall: 2, permanent: true} // 1st save = pending, every save from the 2nd (post-payout submitted) onward fails
	client := &fakePayoutClient{ref: "batch-already-sent"}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err == nil {
		t.Fatalf("expected error when every post-payout persist attempt fails")
	}
	// The external ref must not be silently discarded -- it's the only
	// record that a real payout already happened, and callers log this
	// returned error, so it must be present in the error text.
	if !strings.Contains(err.Error(), "batch-already-sent") {
		t.Fatalf("expected external ref to survive in the error, got: %v", err)
	}
	// The in-memory record (not a zero value) must be returned so a caller
	// can still act on/display it despite the failed persist.
	if record.ExternalRef != "batch-already-sent" || record.Status != string(StatusSubmitted) {
		t.Fatalf("expected returned record to reflect the submitted payout despite persist failure: %+v", record)
	}
}

func TestConcurrentRetryOnlySubmitsOnePayout(t *testing.T) {
	store := newFakeStore()
	client := &fakePayoutClient{fail: true}
	mgr := newTestManager(t, store, Config{DefaultRail: RailNowPayments}, client)

	record, err := mgr.Initiate(context.Background(), InitiateRequest{
		IntentID: "intent-1", ReservationID: "res-1", PartnerID: "partner-a",
		Asset: "ZNHB", AmountUnits: 100_000_000, Account: "0xabc",
	})
	if err == nil {
		t.Fatalf("expected initial payout to fail")
	}
	client.fail = false
	client.ref = "batch-race"
	callsBeforeRetries := client.calls // the failed Initiate above already counted one call

	var wg sync.WaitGroup
	results := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, retryErr := mgr.RetryNowPayments(context.Background(), record.ID)
			results[idx] = retryErr
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, e := range results {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one of 5 concurrent retries to succeed (the rest must see it's no longer 'failed'), got %d successes", successes)
	}
	if retryCalls := client.calls - callsBeforeRetries; retryCalls != 1 {
		t.Fatalf("expected exactly one CreatePayout call across all concurrent retries, got %d -- concurrent retries must not double-submit a real payout", retryCalls)
	}
}

func TestManagerNilSafety(t *testing.T) {
	var mgr *Manager
	if _, err := mgr.Initiate(context.Background(), InitiateRequest{}); !errors.Is(err, ErrManagerUnconfigured) {
		t.Fatalf("expected ErrManagerUnconfigured, got %v", err)
	}
	if _, err := mgr.ConfirmSettled(context.Background(), "x", Receipt{Reference: "y"}); !errors.Is(err, ErrManagerUnconfigured) {
		t.Fatalf("expected ErrManagerUnconfigured, got %v", err)
	}
	if _, err := mgr.RetryNowPayments(context.Background(), "x"); !errors.Is(err, ErrManagerUnconfigured) {
		t.Fatalf("expected ErrManagerUnconfigured, got %v", err)
	}
	if _, err := mgr.MarkFailed(context.Background(), "x", "y"); !errors.Is(err, ErrManagerUnconfigured) {
		t.Fatalf("expected ErrManagerUnconfigured, got %v", err)
	}
	if _, err := mgr.List(context.Background(), "", "", 10); !errors.Is(err, ErrManagerUnconfigured) {
		t.Fatalf("expected ErrManagerUnconfigured, got %v", err)
	}
}
