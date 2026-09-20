package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/subscriptions"
)

// sameDayFixture is a processor with subscriptions configured (no management
// fee) and one payer/merchant pair, driven one settlement pass at a time.
type sameDayFixture struct {
	t        *testing.T
	sp       *StateProcessor
	manager  *nhbstate.Manager
	registry *subscriptions.Registry
	payer    [20]byte
	merchant [20]byte
	height   uint64
}

func newSameDayFixture(t *testing.T, cfg subscriptions.Config) *sameDayFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	if err := sp.SetSubscriptionsConfig(cfg); err != nil {
		t.Fatalf("configure subscriptions: %v", err)
	}
	fx := &sameDayFixture{
		t:        t,
		sp:       sp,
		manager:  nhbstate.NewManager(sp.Trie),
		payer:    [20]byte{0xD1},
		merchant: [20]byte{0xD2},
	}
	fx.registry = subscriptions.NewRegistry(fx.manager)
	fx.setBalance(fx.payer, 0)
	fx.setBalance(fx.merchant, 0)
	return fx
}

func (fx *sameDayFixture) setBalance(addr [20]byte, nhb int64) {
	fx.t.Helper()
	if err := fx.sp.setAccount(addr[:], &types.Account{BalanceNHB: big.NewInt(nhb), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		fx.t.Fatalf("seed account: %v", err)
	}
}

func (fx *sameDayFixture) balance(addr [20]byte) *big.Int {
	fx.t.Helper()
	acc, err := fx.sp.getAccount(addr[:])
	if err != nil {
		fx.t.Fatalf("load account: %v", err)
	}
	return acc.BalanceNHB
}

// subscribe stores a subscription that is due at now, the way Subscribe would
// for a plan without a trial period.
func (fx *sameDayFixture) subscribe(now time.Time, price int64, interval uint64) subscriptions.SubscriptionID {
	fx.t.Helper()
	id, err := fx.manager.SubscriptionsNextSubscriptionID()
	if err != nil {
		fx.t.Fatalf("assign subscription id: %v", err)
	}
	ts := uint64(now.Unix())
	sub := &subscriptions.Subscription{
		ID:              id,
		PlanID:          1,
		Payer:           fx.payer,
		Merchant:        fx.merchant,
		PriceWei:        big.NewInt(price),
		Asset:           subscriptions.AssetNHB,
		IntervalSeconds: interval,
		Status:          subscriptions.SubscriptionStatusActive,
		StartAt:         ts,
		NextChargeAt:    ts,
		CreatedAt:       ts,
	}
	if err := fx.registry.PutSubscription(sub); err != nil {
		fx.t.Fatalf("store subscription: %v", err)
	}
	if err := fx.manager.SubscriptionsAppendDue(ts/secondsPerDay, id); err != nil {
		fx.t.Fatalf("schedule first charge: %v", err)
	}
	return id
}

// settle runs one block's settlement pass at now.
func (fx *sameDayFixture) settle(now time.Time) {
	fx.t.Helper()
	fx.height++
	fx.sp.BeginBlock(fx.height, now)
	defer fx.sp.EndBlock()
	if err := fx.sp.settleSubscriptionCharges(now.Unix()); err != nil {
		fx.t.Fatalf("settle at %s: %v", now.UTC().Format(time.RFC3339), err)
	}
}

func (fx *sameDayFixture) charges(id subscriptions.SubscriptionID) []subscriptions.Charge {
	fx.t.Helper()
	charges, err := fx.registry.ListCharges(id)
	if err != nil {
		fx.t.Fatalf("list charges: %v", err)
	}
	return charges
}

func (fx *sameDayFixture) dueOn(now time.Time) []subscriptions.SubscriptionID {
	fx.t.Helper()
	due, err := fx.manager.SubscriptionsDueOnDay(uint64(now.Unix()) / secondsPerDay)
	if err != nil {
		fx.t.Fatalf("load due bucket: %v", err)
	}
	return due
}

// A retry that lands on the same UTC day as the failed attempt must survive
// the pass that scheduled it and be made when it is due: settlement used to
// clear the whole bucket after the pass, dropping the retry, so the
// subscription was never charged again.
func TestSubscriptionRetryOnTheSameDayIsKeptAndMadeWhenDue(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 5, RetryIntervalSeconds: 3600})
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	id := fx.subscribe(start, 1_000, 30*86400)

	fx.settle(start)
	charges := fx.charges(id)
	if len(charges) != 1 || charges[0].Status != subscriptions.ChargeStatusFailed {
		t.Fatalf("expected one failed attempt on an unfunded payer, got %+v", charges)
	}
	if due := fx.dueOn(start); len(due) != 1 || due[0] != id {
		t.Fatalf("the retry scheduled for later the same day was dropped from the due bucket: %v", due)
	}

	// The retry is not due yet: the passes that follow must not bill early.
	fx.setBalance(fx.payer, 5_000)
	for i := 1; i <= 30; i++ {
		fx.settle(start.Add(time.Duration(i) * 60 * time.Second))
	}
	if got := len(fx.charges(id)); got != 1 {
		t.Fatalf("subscription was attempted again before its retry time: %d attempts", got)
	}

	fx.settle(start.Add(time.Hour))
	charges = fx.charges(id)
	if len(charges) != 2 || charges[1].Status != subscriptions.ChargeStatusPaid || charges[1].AttemptNumber != 2 {
		t.Fatalf("expected the retry to be charged when due, got %+v", charges)
	}
	if got := fx.balance(fx.merchant); got.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("merchant balance after the retry = %s, want 1000", got)
	}
	// The next cycle is a month away: nothing due today any more.
	if due := fx.dueOn(start); len(due) != 0 {
		t.Fatalf("expected the day's bucket to be empty once the retry was made, got %v", due)
	}
}
