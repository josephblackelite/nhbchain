package core

import (
	"math/big"
	"testing"
)

// PL-R5T-87: the Reward Pool / Sale Pool booking of ZNHB that enters or leaves
// the treasury wallet is a consensus rule that has been live since r5, with no
// activation height of its own. These tests pin its observable behavior in the
// four shapes the review named, so a later edit cannot change it unnoticed:
// inflow only, outflow then inflow, inflow then outflow, and a transfer
// between two ordinary users that must not touch either pool at all.
//
// Each case also checks the supply invariant after every transaction and that
// the total ZNHB across every watched account never changes (a transfer moves
// value, it never mints or burns it).
func TestTreasuryBookingSequences(t *testing.T) {
	type step struct {
		from, to int
		amount   int64
	}
	cases := []struct {
		name        string
		steps       []step
		rewardDelta int64
		saleDelta   int64
	}{
		{
			name:        "inflow only",
			steps:       []step{{from: 0, to: treasuryID, amount: 300_000}},
			rewardDelta: 300_000,
			saleDelta:   0,
		},
		{
			name: "outflow then inflow",
			steps: []step{
				{from: treasuryID, to: 1, amount: 500_000},
				{from: 0, to: treasuryID, amount: 200_000},
			},
			rewardDelta: -500_000 + 200_000,
			saleDelta:   0,
		},
		{
			name: "inflow then outflow",
			steps: []step{
				{from: 0, to: treasuryID, amount: 400_000},
				{from: treasuryID, to: 1, amount: 900_000},
			},
			rewardDelta: 400_000 - 900_000,
			saleDelta:   0,
		},
		{
			name:        "user to user leaves both pools untouched",
			steps:       []step{{from: 0, to: 1, amount: 250_000}},
			rewardDelta: 0,
			saleDelta:   0,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.fund(0, 5_000_000)
			f.fund(1, 5_000_000)

			rewardBefore, saleBefore := f.rewardPool(), f.salePool()
			totalBefore := f.totalZNHB()

			for i, s := range tc.steps {
				label := tc.name
				if err := f.apply(label, f.transferZNHB(s.from, s.to, s.amount, "")); err != nil {
					t.Fatalf("step %d (%d -> %d, %d): %v", i, s.from, s.to, s.amount, err)
				}
				if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
					t.Fatalf("step %d: supply invariant broken: %v", i, err)
				}
			}

			wantReward := new(big.Int).Add(rewardBefore, big.NewInt(tc.rewardDelta))
			if got := f.rewardPool(); got.Cmp(wantReward) != 0 {
				t.Fatalf("reward pool = %s, want %s", got, wantReward)
			}
			wantSale := new(big.Int).Add(saleBefore, big.NewInt(tc.saleDelta))
			if got := f.salePool(); got.Cmp(wantSale) != 0 {
				t.Fatalf("sale pool = %s, want %s", got, wantSale)
			}
			if got := f.totalZNHB(); got.Cmp(totalBefore) != 0 {
				t.Fatalf("total ZNHB went from %s to %s: a transfer created or destroyed value", totalBefore, got)
			}
		})
	}
}
