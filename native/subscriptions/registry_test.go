package subscriptions_test

import (
	"math/big"
	"testing"

	"nhbchain/core/state"
	subscriptions "nhbchain/native/subscriptions"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newTestRegistry(t *testing.T) (*subscriptions.Registry, *state.Manager) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	manager := state.NewManager(tr)
	registry := subscriptions.NewRegistry(manager)
	return registry, manager
}

func newTestPlan(manager *state.Manager, t *testing.T, merchant [20]byte) *subscriptions.Plan {
	t.Helper()
	id, err := manager.SubscriptionsNextPlanID()
	if err != nil {
		t.Fatalf("assign plan id: %v", err)
	}
	return &subscriptions.Plan{
		ID:              id,
		Merchant:        merchant,
		Name:            "Pro Monthly",
		PriceWei:        big.NewInt(10_000),
		Asset:           subscriptions.AssetNHB,
		IntervalSeconds: 2_592_000,
		Active:          true,
	}
}

func TestRegistryCreatePlan_UnauthorizedCallerRejected(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var merchant, stranger [20]byte
	merchant[19] = 0x01
	stranger[19] = 0x02

	plan := &subscriptions.Plan{
		Merchant:        merchant,
		Name:            "Pro Monthly",
		PriceWei:        big.NewInt(10_000),
		Asset:           subscriptions.AssetNHB,
		IntervalSeconds: 86400,
	}
	if err := registry.CreatePlan(stranger, plan); err == nil {
		t.Fatalf("expected unauthorized error when a stranger creates a plan on the merchant's behalf")
	}
	if err := registry.CreatePlan(merchant, plan); err != nil {
		t.Fatalf("merchant creating their own plan should succeed: %v", err)
	}
}

func TestRegistryCreatePlan_RejectsInvalidTerms(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var merchant [20]byte
	merchant[19] = 0x01

	cases := []struct {
		name string
		plan *subscriptions.Plan
	}{
		{"zero price", &subscriptions.Plan{Merchant: merchant, Name: "x", PriceWei: big.NewInt(0), Asset: subscriptions.AssetNHB, IntervalSeconds: 86400}},
		{"negative price", &subscriptions.Plan{Merchant: merchant, Name: "x", PriceWei: big.NewInt(-5), Asset: subscriptions.AssetNHB, IntervalSeconds: 86400}},
		{"bad asset", &subscriptions.Plan{Merchant: merchant, Name: "x", PriceWei: big.NewInt(10), Asset: "USD", IntervalSeconds: 86400}},
		{"zero interval", &subscriptions.Plan{Merchant: merchant, Name: "x", PriceWei: big.NewInt(10), Asset: subscriptions.AssetNHB, IntervalSeconds: 0}},
		{"empty name", &subscriptions.Plan{Merchant: merchant, Name: "  ", PriceWei: big.NewInt(10), Asset: subscriptions.AssetNHB, IntervalSeconds: 86400}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := registry.CreatePlan(merchant, tc.plan); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestRegistryUpdatePlan_PricingTermsImmutable(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var merchant [20]byte
	merchant[19] = 0x01
	plan := newTestPlan(manager, t, merchant)
	if err := registry.CreatePlan(merchant, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	updated, err := registry.UpdatePlan(merchant, plan.ID, "Pro Monthly (renamed)", false)
	if err != nil {
		t.Fatalf("update plan: %v", err)
	}
	if updated.Name != "Pro Monthly (renamed)" {
		t.Fatalf("name = %q, want updated", updated.Name)
	}
	if updated.Active {
		t.Fatalf("expected Active to be false after update")
	}
	// UpdatePlan's signature has no way to touch PriceWei/Asset/IntervalSeconds
	// at all -- confirm they survived the update completely untouched.
	if updated.PriceWei.Cmp(plan.PriceWei) != 0 {
		t.Fatalf("priceWei changed: got %s, want %s", updated.PriceWei, plan.PriceWei)
	}
	if updated.Asset != plan.Asset {
		t.Fatalf("asset changed: got %s, want %s", updated.Asset, plan.Asset)
	}
	if updated.IntervalSeconds != plan.IntervalSeconds {
		t.Fatalf("intervalSeconds changed: got %d, want %d", updated.IntervalSeconds, plan.IntervalSeconds)
	}
}

func TestRegistryUpdatePlan_UnauthorizedCallerRejected(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var merchant, stranger [20]byte
	merchant[19] = 0x01
	stranger[19] = 0x02
	plan := newTestPlan(manager, t, merchant)
	if err := registry.CreatePlan(merchant, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := registry.UpdatePlan(stranger, plan.ID, "hijacked", false); err == nil {
		t.Fatalf("expected unauthorized error")
	}
}

func TestRegistrySubscriptionLifecycle(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var merchant, payer, stranger [20]byte
	merchant[19] = 0x01
	payer[19] = 0x02
	stranger[19] = 0x03

	plan := newTestPlan(manager, t, merchant)
	if err := registry.CreatePlan(merchant, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	subID, err := manager.SubscriptionsNextSubscriptionID()
	if err != nil {
		t.Fatalf("assign subscription id: %v", err)
	}
	sub := &subscriptions.Subscription{
		ID:              subID,
		PlanID:          plan.ID,
		Payer:           payer,
		Merchant:        merchant,
		PriceWei:        plan.PriceWei,
		Asset:           plan.Asset,
		IntervalSeconds: plan.IntervalSeconds,
		Status:          subscriptions.SubscriptionStatusActive,
	}
	if err := registry.CreateSubscription(sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := registry.CreateSubscription(sub); err == nil {
		t.Fatalf("expected duplicate subscription creation to be rejected")
	}

	payerSubs, err := registry.ListSubscriptionsByPayer(payer)
	if err != nil || len(payerSubs) != 1 || payerSubs[0] != subID {
		t.Fatalf("ListSubscriptionsByPayer = %v, err %v", payerSubs, err)
	}
	merchantSubs, err := registry.ListSubscriptionsByMerchant(merchant)
	if err != nil || len(merchantSubs) != 1 || merchantSubs[0] != subID {
		t.Fatalf("ListSubscriptionsByMerchant = %v, err %v", merchantSubs, err)
	}

	// A stranger (neither payer, merchant, nor admin) may not cancel.
	if _, err := registry.CancelSubscription(stranger, subID, 1000); err == nil {
		t.Fatalf("expected unauthorized error for stranger cancelling")
	}

	// The payer themself may cancel.
	cancelled, err := registry.CancelSubscription(payer, subID, 1000)
	if err != nil {
		t.Fatalf("payer cancel: %v", err)
	}
	if cancelled.Status != subscriptions.SubscriptionStatusCancelled {
		t.Fatalf("status = %v, want cancelled", cancelled.Status)
	}
	if cancelled.CancelledAt != 1000 {
		t.Fatalf("cancelledAt = %d, want 1000", cancelled.CancelledAt)
	}

	// Cancelling an already-cancelled subscription is rejected, not a
	// silent no-op -- callers (core/subscriptions_tx.go) rely on this to
	// surface a clear error rather than accepting a redundant cancel.
	if _, err := registry.CancelSubscription(payer, subID, 2000); err == nil {
		t.Fatalf("expected error cancelling an already-cancelled subscription")
	}
}

func TestRegistryAppendCharge_PreservesOrderNoDedup(t *testing.T) {
	registry, _ := newTestRegistry(t)
	subID := subscriptions.SubscriptionID(1)

	for i := 0; i < 3; i++ {
		if err := registry.AppendCharge(subID, subscriptions.Charge{
			SubscriptionID: subID,
			AmountWei:      big.NewInt(100),
			Status:         subscriptions.ChargeStatusPaid,
			AttemptNumber:  uint32(i + 1),
		}); err != nil {
			t.Fatalf("append charge %d: %v", i, err)
		}
	}
	charges, err := registry.ListCharges(subID)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	// Three structurally-similar charges (same amount/status) must all
	// survive -- AttemptNumber is what distinguishes them, and
	// AppendCharge must never deduplicate on the other fields.
	if len(charges) != 3 {
		t.Fatalf("len(charges) = %d, want 3 (no dedup)", len(charges))
	}
	for i, c := range charges {
		if c.AttemptNumber != uint32(i+1) {
			t.Fatalf("charges[%d].AttemptNumber = %d, want %d (order preserved)", i, c.AttemptNumber, i+1)
		}
	}
}
