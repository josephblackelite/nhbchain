package storage

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"
	swap "nhbchain/native/swap"
)

func TestRecordSnapshotAndLatest(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	quote := swap.PriceQuote{Timestamp: time.Unix(1700000000, 0)}
	rat := new(big.Rat).SetFloat64(1.23)
	quote.Rate = rat
	if err := store.RecordSample(ctx, "ZNHB", "USD", "now", quote, time.Unix(1700000100, 0)); err != nil {
		t.Fatalf("record sample: %v", err)
	}
	if err := store.RecordSnapshot(ctx, "ZNHB", "USD", "1.230000000000000000", []string{"now"}, "proof", time.Unix(1700000100, 0)); err != nil {
		t.Fatalf("record snapshot: %v", err)
	}
	snap, err := store.LatestSnapshot(ctx, "ZNHB", "USD")
	if err != nil {
		t.Fatalf("latest snapshot: %v", err)
	}
	if snap.MedianRate != "1.230000000000000000" {
		t.Fatalf("unexpected median: %s", snap.MedianRate)
	}
	if len(snap.Feeders) != 1 || snap.Feeders[0] != "now" {
		t.Fatalf("unexpected feeders: %+v", snap.Feeders)
	}
}

func TestThrottlePolicy(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	policy := Policy{ID: "default", MintLimit: 100, RedeemLimit: 50, Window: time.Minute}
	if err := store.SavePolicy(ctx, policy); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	loaded, err := store.GetPolicy(ctx, "default")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if loaded.MintLimit != 100 || loaded.RedeemLimit != 50 {
		t.Fatalf("unexpected policy: %+v", loaded)
	}
	now := time.Now()
	allow, err := store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, big.NewInt(40), now)
	if err != nil {
		t.Fatalf("check throttle: %v", err)
	}
	if !allow {
		t.Fatalf("expected first mint to pass")
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `
        SELECT amount FROM throttle_events WHERE policy_id = ? AND action = ? ORDER BY occurred_at LIMIT 1
    `, "default", string(ActionMint)).Scan(&stored); err != nil {
		t.Fatalf("load stored amount: %v", err)
	}
	if strings.TrimSpace(stored) != "40" {
		t.Fatalf("unexpected stored amount: %q", stored)
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, big.NewInt(30), now.Add(time.Second))
	if !allow {
		t.Fatalf("expected second mint to pass")
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, big.NewInt(40), now.Add(2*time.Second))
	if allow {
		t.Fatalf("expected third mint to fail")
	}
	allow, err = store.CheckThrottle(ctx, "default", ActionMint, loaded.MintLimit, loaded.Window, big.NewInt(40), now.Add(loaded.Window+time.Second))
	if err != nil {
		t.Fatalf("check throttle after window: %v", err)
	}
	if !allow {
		t.Fatalf("expected mint to pass after window")
	}
	allow, err = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, big.NewInt(30), now)
	if err != nil {
		t.Fatalf("check redeem: %v", err)
	}
	if !allow {
		t.Fatalf("expected redeem to pass")
	}
	allow, err = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, big.NewInt(15), now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("second redeem: %v", err)
	}
	if !allow {
		t.Fatalf("expected second redeem to pass")
	}
	allow, _ = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, big.NewInt(10), now.Add(3*time.Second))
	if allow {
		t.Fatalf("expected redeem to fail when exceeding remainder")
	}
	allow, err = store.CheckThrottle(ctx, "default", ActionRedeem, loaded.RedeemLimit, loaded.Window, big.NewInt(10), now.Add(loaded.Window+time.Second))
	if err != nil {
		t.Fatalf("redeem after window: %v", err)
	}
	if !allow {
		t.Fatalf("expected redeem to pass after window")
	}
}

func TestDailyUsagePersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	day := time.Date(2024, time.March, 4, 12, 0, 0, 0, time.UTC)
	if err := store.SaveDailyUsage(ctx, day, 123); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	dayOut, amountOut, ok, err := store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record")
	}
	if amountOut != 123 {
		t.Fatalf("unexpected amount: got %d want %d", amountOut, 123)
	}
	wantDay := day.UTC().Truncate(24 * time.Hour)
	if !dayOut.Equal(wantDay) {
		t.Fatalf("unexpected day: got %s want %s", dayOut, wantDay)
	}
	if err := store.SaveDailyUsage(ctx, day, 456); err != nil {
		t.Fatalf("update usage: %v", err)
	}
	dayOut, amountOut, ok, err = store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage after update: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record after update")
	}
	if amountOut != 456 {
		t.Fatalf("unexpected amount after update: got %d want %d", amountOut, 456)
	}
	nextDay := wantDay.Add(24 * time.Hour)
	if err := store.SaveDailyUsage(ctx, nextDay, 10); err != nil {
		t.Fatalf("save next day usage: %v", err)
	}
	dayOut, amountOut, ok, err = store.LatestDailyUsage(ctx)
	if err != nil {
		t.Fatalf("latest usage next day: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage record for next day")
	}
	if !dayOut.Equal(nextDay) {
		t.Fatalf("unexpected day for next day record: got %s want %s", dayOut, nextDay)
	}
	if amountOut != 10 {
		t.Fatalf("unexpected amount for next day record: got %d want %d", amountOut, 10)
	}
}

func TestConsumePartnerQuota(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	day := time.Date(2024, time.January, 10, 0, 0, 0, 0, time.UTC)
	allowed, remaining, err := store.ConsumePartnerQuota(ctx, "desk-1", day, 100, 500)
	if err != nil {
		t.Fatalf("consume quota: %v", err)
	}
	if !allowed {
		t.Fatalf("expected initial consumption to succeed")
	}
	if remaining != 400 {
		t.Fatalf("unexpected remaining quota: got %d want %d", remaining, 400)
	}
	allowed, remaining, err = store.ConsumePartnerQuota(ctx, "desk-1", day, 300, 500)
	if err != nil {
		t.Fatalf("consume quota second time: %v", err)
	}
	if !allowed {
		t.Fatalf("expected quota to allow cumulative usage below limit")
	}
	if remaining != 100 {
		t.Fatalf("unexpected remaining after second usage: got %d want %d", remaining, 100)
	}
	allowed, remaining, err = store.ConsumePartnerQuota(ctx, "desk-1", day, 150, 500)
	if err != nil {
		t.Fatalf("consume quota third time: %v", err)
	}
	if allowed {
		t.Fatalf("expected quota exhaustion to reject consumption")
	}
	if remaining != 100 {
		t.Fatalf("unexpected remaining after rejection: got %d want %d", remaining, 100)
	}
	// Ensure usage persisted across calls.
	allowed, remaining, err = store.ConsumePartnerQuota(ctx, "desk-1", day, 100, 500)
	if err != nil {
		t.Fatalf("consume quota fourth time: %v", err)
	}
	if !allowed || remaining != 0 {
		t.Fatalf("unexpected final consumption result: allowed=%v remaining=%d", allowed, remaining)
	}
}

func TestOpenRequiresPath(t *testing.T) {
	if _, err := Open(""); !errors.Is(err, ErrPathRequired) {
		t.Fatalf("expected ErrPathRequired, got %v", err)
	}
}

func TestAPINoncePersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	record := gatewayauth.NonceRecord{
		APIKey:     "partner",
		Timestamp:  strconv.FormatInt(now.Unix(), 10),
		Nonce:      "nonce-1",
		ObservedAt: now,
	}
	existed, err := store.EnsureNonce(ctx, record)
	if err != nil {
		t.Fatalf("ensure nonce: %v", err)
	}
	if existed {
		t.Fatalf("expected new nonce to be inserted")
	}
	existed, err = store.EnsureNonce(ctx, record)
	if err != nil {
		t.Fatalf("ensure nonce second time: %v", err)
	}
	if !existed {
		t.Fatalf("expected duplicate nonce to be reported")
	}
	cutoff := now.Add(-time.Minute)
	records, err := store.RecentNonces(ctx, cutoff)
	if err != nil {
		t.Fatalf("recent nonces: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("unexpected nonce count: %d", len(records))
	}
	loaded := records[0]
	if loaded.APIKey != record.APIKey || loaded.Timestamp != record.Timestamp || loaded.Nonce != record.Nonce {
		t.Fatalf("unexpected nonce record: %+v", loaded)
	}
	if err := store.PruneNonces(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("prune nonces: %v", err)
	}
	records, err = store.RecentNonces(ctx, cutoff)
	if err != nil {
		t.Fatalf("recent nonces after prune: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected nonces to be pruned, got %d", len(records))
	}
}

func TestLedgerAndReservationPersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	ledger := LedgerBalanceRecord{Asset: "ZNHB", Available: 1_000_000, Reserved: 25_000, Payouts: 5_000}
	if err := store.SaveLedgerBalance(ctx, ledger); err != nil {
		t.Fatalf("save ledger: %v", err)
	}
	records, err := store.LoadLedgerBalances(ctx)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("unexpected ledger count: %d", len(records))
	}
	if records[0].Asset != "ZNHB" || records[0].Available != ledger.Available || records[0].Reserved != ledger.Reserved || records[0].Payouts != ledger.Payouts {
		t.Fatalf("unexpected ledger record: %+v", records[0])
	}
	expires := time.Unix(1_700_000_000, 0).UTC()
	res := ReservationRecord{ID: "q-1", Asset: "ZNHB", AmountIn: 100_000, AmountOut: 95_000, Price: 1_000_000_000, ExpiresAt: expires, Account: "acct-1"}
	if err := store.SaveReservation(ctx, res); err != nil {
		t.Fatalf("save reservation: %v", err)
	}
	reservations, err := store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("load reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("unexpected reservation count: %d", len(reservations))
	}
	loaded := reservations[0]
	if loaded.ID != res.ID || loaded.Asset != res.Asset || loaded.AmountIn != res.AmountIn || loaded.AmountOut != res.AmountOut {
		t.Fatalf("reservation mismatch: %+v", loaded)
	}
	if !loaded.ExpiresAt.Equal(expires) {
		t.Fatalf("reservation expiry mismatch: got %s want %s", loaded.ExpiresAt, expires)
	}
	res.IntentCreated = true
	res.IntentID = "intent-1"
	res.IntentCreatedAt = expires.Add(time.Minute)
	if err := store.SaveReservation(ctx, res); err != nil {
		t.Fatalf("update reservation: %v", err)
	}
	reservations, err = store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("reload reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("unexpected reservation count after update: %d", len(reservations))
	}
	loaded = reservations[0]
	if !loaded.IntentCreated || loaded.IntentID != res.IntentID {
		t.Fatalf("reservation intent not persisted: %+v", loaded)
	}
	if !loaded.IntentCreatedAt.Equal(res.IntentCreatedAt) {
		t.Fatalf("intent timestamp mismatch: got %s want %s", loaded.IntentCreatedAt, res.IntentCreatedAt)
	}
	if err := store.DeleteReservation(ctx, res.ID); err != nil {
		t.Fatalf("delete reservation: %v", err)
	}
	reservations, err = store.LoadReservations(ctx)
	if err != nil {
		t.Fatalf("reload reservations after delete: %v", err)
	}
	if len(reservations) != 0 {
		t.Fatalf("expected reservations to be empty, got %d", len(reservations))
	}
}

func TestRecordAndListAuditEvents(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	events := []AuditEvent{
		{EventType: "quote", PartnerID: "partner-a", SubjectID: "quote-1", Outcome: "success", Detail: `{"asset":"ZNHB"}`, TraceID: "trace-1"},
		{EventType: "reserve", PartnerID: "partner-a", SubjectID: "quote-1", Outcome: "quota_exceeded", Detail: `{"amount_out":50}`, TraceID: "trace-2"},
		{EventType: "quote", PartnerID: "partner-b", SubjectID: "quote-2", Outcome: "error", Detail: `{"error":"boom"}`, TraceID: "trace-3"},
	}
	for _, event := range events {
		if err := store.RecordAuditEvent(ctx, event); err != nil {
			t.Fatalf("record audit event: %v", err)
		}
	}

	all, err := store.ListAuditEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("list all audit events: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 audit events, got %d", len(all))
	}
	// Most recent first.
	if all[0].EventType != "quote" || all[0].PartnerID != "partner-b" || all[0].Outcome != "error" {
		t.Fatalf("unexpected most recent event: %+v", all[0])
	}

	filtered, err := store.ListAuditEvents(ctx, "partner-a", 10)
	if err != nil {
		t.Fatalf("list filtered audit events: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 audit events for partner-a, got %d", len(filtered))
	}
	for _, event := range filtered {
		if event.PartnerID != "partner-a" {
			t.Fatalf("unexpected partner in filtered results: %+v", event)
		}
	}

	if err := store.RecordAuditEvent(ctx, AuditEvent{PartnerID: "partner-a"}); err == nil {
		t.Fatalf("expected error for missing event type")
	}
}

func TestSaveAndListSettlements(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	record := SettlementRecord{
		ID:            "settle-1",
		IntentID:      "intent-1",
		ReservationID: "res-1",
		PartnerID:     "partner-a",
		Asset:         "znhb",
		AmountUnits:   102_000_000,
		Account:       "merchant-123",
		Rail:          "nowpayments",
		Status:        "pending",
		Detail:        `{"foo":"bar"}`,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.SaveSettlement(ctx, record); err != nil {
		t.Fatalf("save settlement: %v", err)
	}

	loaded, err := store.GetSettlement(ctx, "settle-1")
	if err != nil {
		t.Fatalf("get settlement: %v", err)
	}
	if loaded.Asset != "ZNHB" {
		t.Fatalf("expected asset normalised to upper case, got %q", loaded.Asset)
	}
	if loaded.Status != "pending" || loaded.Rail != "nowpayments" || !loaded.SettledAt.IsZero() {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}

	// Transition to settled -- confirms the upsert path updates mutable
	// fields (status/external_ref/detail/settled_at) without touching the
	// immutable identity fields (intent/reservation/partner/asset/amount).
	record.Status = "settled"
	record.ExternalRef = "wire-ref-9"
	record.Detail = `{"note":"confirmed"}`
	record.UpdatedAt = now.Add(time.Minute)
	record.SettledAt = now.Add(time.Minute)
	if err := store.SaveSettlement(ctx, record); err != nil {
		t.Fatalf("update settlement: %v", err)
	}
	loaded, err = store.GetSettlement(ctx, "settle-1")
	if err != nil {
		t.Fatalf("get updated settlement: %v", err)
	}
	if loaded.Status != "settled" || loaded.ExternalRef != "wire-ref-9" || loaded.SettledAt.IsZero() {
		t.Fatalf("unexpected updated record: %+v", loaded)
	}
	if loaded.IntentID != "intent-1" || loaded.AmountUnits != 102_000_000 {
		t.Fatalf("identity fields must not change on update: %+v", loaded)
	}

	second := SettlementRecord{
		ID: "settle-2", IntentID: "intent-2", ReservationID: "res-2", PartnerID: "partner-b",
		Asset: "ZNHB", AmountUnits: 50_000_000, Account: "merchant-456", Rail: "manual_treasury",
		Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveSettlement(ctx, second); err != nil {
		t.Fatalf("save second settlement: %v", err)
	}

	all, err := store.ListSettlements(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("list all settlements: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 settlements, got %d", len(all))
	}

	filtered, err := store.ListSettlements(ctx, "partner-b", "", 10)
	if err != nil {
		t.Fatalf("list by partner: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "settle-2" {
		t.Fatalf("unexpected partner filter result: %+v", filtered)
	}

	settled, err := store.ListSettlements(ctx, "", "settled", 10)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(settled) != 1 || settled[0].ID != "settle-1" {
		t.Fatalf("unexpected status filter result: %+v", settled)
	}

	if _, err := store.GetSettlement(ctx, "missing"); err == nil {
		t.Fatalf("expected error for missing settlement")
	}
	if err := store.SaveSettlement(ctx, SettlementRecord{ID: "bad"}); err == nil {
		t.Fatalf("expected error for settlement missing required fields")
	}
}

func openTestDB(t *testing.T) *Storage {
	t.Helper()
	dir := t.TempDir()
	dsn, err := FileDSN(filepath.Join(dir, "swapd.sqlite"))
	if err != nil {
		t.Fatalf("build DSN: %v", err)
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
