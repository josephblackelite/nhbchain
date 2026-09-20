package subscriptions_test

import (
	"errors"
	"testing"

	subscriptions "nhbchain/native/subscriptions"
)

// A self-subscription (payer == the plan's merchant) is refused at creation,
// before anything is persisted or indexed. Settling one would move the price
// from an account to itself, and in the chain's settlement code two copies of
// one account used to make it mint the price minus the management fee.
func TestRegistryCreateSubscription_RejectsSelfSubscription(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var account, other [20]byte
	account[19] = 0x01
	other[19] = 0x02

	plan := newTestPlan(manager, t, account)
	if err := registry.CreatePlan(account, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	self := &subscriptions.Subscription{
		ID:              1,
		PlanID:          plan.ID,
		Payer:           account,
		Merchant:        account,
		PriceWei:        subscriptions.MinPlanPriceWei(),
		Asset:           subscriptions.AssetNHB,
		IntervalSeconds: 86400,
		Status:          subscriptions.SubscriptionStatusActive,
	}
	err := registry.CreateSubscription(self)
	if !errors.Is(err, subscriptions.ErrSelfSubscription) {
		t.Fatalf("expected ErrSelfSubscription, got %v", err)
	}
	if _, ok := registry.GetSubscription(1); ok {
		t.Fatalf("a rejected self-subscription must not be persisted")
	}
	if ids, err := registry.ListSubscriptionsByPayer(account); err != nil || len(ids) != 0 {
		t.Fatalf("a rejected self-subscription must not be indexed by payer: ids=%v err=%v", ids, err)
	}
	if ids, err := registry.ListSubscriptionsByMerchant(account); err != nil || len(ids) != 0 {
		t.Fatalf("a rejected self-subscription must not be indexed by merchant: ids=%v err=%v", ids, err)
	}

	// A different payer subscribing to the same plan is unaffected.
	ok := *self
	ok.Payer = other
	if err := registry.CreateSubscription(&ok); err != nil {
		t.Fatalf("a distinct payer must still be able to subscribe: %v", err)
	}
}
