package lending

import (
	"math/big"
	"testing"
)

func newTestDeposit(tenureDays uint64, totalInterest int64, paidInterest int64, payout FixedTermDepositPayout, nextCycle uint32) *FixedTermDeposit {
	return &FixedTermDeposit{
		TenureDays:           tenureDays,
		TotalInterestOwedWei: big.NewInt(totalInterest),
		PaidInterestWei:      big.NewInt(paidInterest),
		Payout:               payout,
		IssuedAtTime:         1_700_000_000,
		MaturityTime:         1_700_000_000 + tenureDays*86400,
		NextPayoutCycle:      nextCycle,
	}
}

// TestDecideDepositPayout_LumpSumPaysEverythingAtOnce proves the lump-sum
// preference always demands principal AND the full outstanding interest in
// a single step, regardless of tenure.
func TestDecideDepositPayout_LumpSumPaysEverythingAtOnce(t *testing.T) {
	deposit := newTestDeposit(90, 1600, 0, FixedTermDepositPayoutLumpSumAtMaturity, 0)
	decision := DecideDepositPayout(deposit)
	if !decision.PrincipalDue {
		t.Fatalf("expected principal due for lump-sum payout")
	}
	if decision.InterestDue.Cmp(big.NewInt(1600)) != 0 {
		t.Fatalf("expected full interest 1600 due, got %s", decision.InterestDue)
	}
	if decision.NextAttemptAt != 0 {
		t.Fatalf("expected no further attempt scheduled after a lump-sum payout, got %d", decision.NextAttemptAt)
	}
}

// TestDecideDepositPayout_PeriodicSplitsIntoThreeCyclesAndSumsExactly proves
// a 90-day periodic deposit pays interest across 3 cycles that sum to
// EXACTLY TotalInterestOwedWei (no rounding shortfall), with principal only
// due on the final cycle -- never before.
func TestDecideDepositPayout_PeriodicSplitsIntoThreeCyclesAndSumsExactly(t *testing.T) {
	deposit := newTestDeposit(90, 1600, 0, FixedTermDepositPayoutPeriodic, 1)

	totalPaid := big.NewInt(0)
	for cycle := 1; cycle <= 3; cycle++ {
		decision := DecideDepositPayout(deposit)
		if decision.PrincipalDue != (cycle == 3) {
			t.Fatalf("cycle %d: expected PrincipalDue=%v, got %v", cycle, cycle == 3, decision.PrincipalDue)
		}
		totalPaid.Add(totalPaid, decision.InterestDue)
		deposit.PaidInterestWei = new(big.Int).Add(deposit.PaidInterestWei, decision.InterestDue)
		deposit.NextPayoutCycle = decision.AdvanceToCycle
		if cycle < 3 {
			if decision.NextAttemptAt == 0 {
				t.Fatalf("cycle %d: expected a next attempt scheduled before the final cycle", cycle)
			}
		} else {
			if decision.NextAttemptAt != 0 {
				t.Fatalf("expected no further attempt scheduled after the final cycle (principal handled in the SAME step), got %d", decision.NextAttemptAt)
			}
		}
	}
	if totalPaid.Cmp(big.NewInt(1600)) != 0 {
		t.Fatalf("expected total interest paid across all cycles to equal TotalInterestOwedWei exactly: got %s want 1600", totalPaid)
	}
}

// TestDecideDepositPayout_SingleCycleDepositPaysPrincipalWithFirstInterest
// proves a 30-day periodic deposit (a single cycle) pays principal in that
// SAME first step, not deferred to a separate maturity step.
func TestDecideDepositPayout_SingleCycleDepositPaysPrincipalWithFirstInterest(t *testing.T) {
	deposit := newTestDeposit(30, 1200, 0, FixedTermDepositPayoutPeriodic, 1)
	decision := DecideDepositPayout(deposit)
	if !decision.PrincipalDue {
		t.Fatalf("expected principal due in the single cycle of a 30-day periodic deposit")
	}
	if decision.InterestDue.Cmp(big.NewInt(1200)) != 0 {
		t.Fatalf("expected full interest 1200 due, got %s", decision.InterestDue)
	}
	if decision.NextAttemptAt != 0 {
		t.Fatalf("expected nothing more scheduled, got %d", decision.NextAttemptAt)
	}
}
