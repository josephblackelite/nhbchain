package subscriptions_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	subscriptions "nhbchain/native/subscriptions"
)

// The chain charges every subscription by itself, so how often it may charge
// one is fixed when the plan is created: a plan cannot name a billing interval
// under a day (or over ten years), a trial over ten years or a price under one
// whole token. Before, only "interval above zero" and "price above zero" were
// checked, and a plan of one wei every second was a valid plan.
func TestRegistryCreatePlan_EnforcesTheBoundsOnPlanTerms(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var merchant [20]byte
	merchant[19] = 0x01

	valid := func() *subscriptions.Plan {
		return &subscriptions.Plan{
			Merchant:        merchant,
			Name:            "Pro",
			PriceWei:        subscriptions.MinPlanPriceWei(),
			Asset:           subscriptions.AssetNHB,
			IntervalSeconds: subscriptions.MinPlanIntervalSeconds,
		}
	}
	refused := []struct {
		name   string
		mutate func(*subscriptions.Plan)
	}{
		{"an interval of one second", func(p *subscriptions.Plan) { p.IntervalSeconds = 1 }},
		{"an interval of a minute", func(p *subscriptions.Plan) { p.IntervalSeconds = 60 }},
		{"an interval of an hour", func(p *subscriptions.Plan) { p.IntervalSeconds = 3600 }},
		{"an interval a second under a day", func(p *subscriptions.Plan) { p.IntervalSeconds = 86399 }},
		{"an interval a second over ten years", func(p *subscriptions.Plan) { p.IntervalSeconds = 315_360_001 }},
		{"an interval at the end of the uint64 range", func(p *subscriptions.Plan) { p.IntervalSeconds = math.MaxUint64 }},
		{"a trial a second over ten years", func(p *subscriptions.Plan) { p.TrialPeriodSeconds = 315_360_001 }},
		{"a trial at the end of the uint64 range", func(p *subscriptions.Plan) { p.TrialPeriodSeconds = math.MaxUint64 }},
		{"a price of one wei", func(p *subscriptions.Plan) { p.PriceWei = big.NewInt(1) }},
		{"a price a wei under one whole token", func(p *subscriptions.Plan) {
			p.PriceWei = new(big.Int).Sub(subscriptions.MinPlanPriceWei(), big.NewInt(1))
		}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			plan := valid()
			tc.mutate(plan)
			err := registry.CreatePlan(merchant, plan)
			if !errors.Is(err, subscriptions.ErrInvalidPlan) {
				t.Fatalf("expected %s to be refused with ErrInvalidPlan, got %v", tc.name, err)
			}
		})
	}
	if ids, err := registry.ListPlansByMerchant(merchant); err != nil || len(ids) != 0 {
		t.Fatalf("a refused plan must not be stored or indexed: ids=%v err=%v", ids, err)
	}
	if _, ok := registry.GetPlan(0); ok {
		t.Fatalf("a refused plan must not be stored")
	}

	// The bounds themselves are allowed.
	accepted := []struct {
		name   string
		mutate func(*subscriptions.Plan)
	}{
		{"an interval of exactly a day", func(p *subscriptions.Plan) { p.IntervalSeconds = 86400 }},
		{"an interval of exactly ten years", func(p *subscriptions.Plan) { p.IntervalSeconds = 315_360_000 }},
		{"a trial of exactly ten years", func(p *subscriptions.Plan) { p.TrialPeriodSeconds = 315_360_000 }},
		{"no trial", func(p *subscriptions.Plan) { p.TrialPeriodSeconds = 0 }},
		{"a price of exactly one whole token", func(p *subscriptions.Plan) { p.PriceWei = big.NewInt(1_000_000_000_000_000_000) }},
		{"a price of a hundred tokens in ZNHB", func(p *subscriptions.Plan) {
			p.Asset = subscriptions.AssetZNHB
			p.PriceWei = new(big.Int).Mul(subscriptions.MinPlanPriceWei(), big.NewInt(100))
		}},
	}
	for i, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			plan := valid()
			plan.ID = subscriptions.PlanID(i + 1)
			tc.mutate(plan)
			if err := registry.CreatePlan(merchant, plan); err != nil {
				t.Fatalf("expected %s to be accepted, got %v", tc.name, err)
			}
		})
	}
}

// The bounds are the ones the documentation states, and the minimum price is
// handed out as a copy.
func TestPlanBoundsAreTheDocumentedOnes(t *testing.T) {
	if subscriptions.MinPlanIntervalSeconds != 86_400 {
		t.Fatalf("minimum interval = %d, want one day (86400 s)", subscriptions.MinPlanIntervalSeconds)
	}
	if subscriptions.MaxPlanIntervalSeconds != 315_360_000 || subscriptions.MaxTrialPeriodSeconds != 315_360_000 {
		t.Fatalf("maximum interval/trial = %d/%d, want ten years (315360000 s)", subscriptions.MaxPlanIntervalSeconds, subscriptions.MaxTrialPeriodSeconds)
	}
	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	if got := subscriptions.MinPlanPriceWei(); got.Cmp(want) != 0 {
		t.Fatalf("minimum price = %s, want one whole token (10^18 wei)", got)
	}
	subscriptions.MinPlanPriceWei().SetInt64(1)
	if got := subscriptions.MinPlanPriceWei(); got.Cmp(want) != 0 {
		t.Fatalf("changing the returned minimum price changed the minimum: now %s", got)
	}
}

func TestAddSecondsSaturatesInsteadOfWrapping(t *testing.T) {
	cases := []struct{ t, d, want uint64 }{
		{0, 0, 0},
		{1_800_000_000, 86_400, 1_800_086_400},
		{math.MaxUint64 - 5, 5, math.MaxUint64},
		{math.MaxUint64 - 5, 6, math.MaxUint64},
		{1_800_000_000, math.MaxUint64, math.MaxUint64},
		{math.MaxUint64, math.MaxUint64, math.MaxUint64},
	}
	for _, tc := range cases {
		if got := subscriptions.AddSeconds(tc.t, tc.d); got != tc.want {
			t.Fatalf("AddSeconds(%d, %d) = %d, want %d", tc.t, tc.d, got, tc.want)
		}
	}
}
