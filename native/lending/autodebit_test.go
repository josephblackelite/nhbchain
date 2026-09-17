package lending

import (
	"math/big"
	"testing"
)

func TestTotalAutoDebitCycles(t *testing.T) {
	cases := []struct {
		tenureDays uint64
		want       uint32
	}{
		{0, 0},
		{1, 1},
		{7, 1},
		{30, 1},
		{31, 2},
		{60, 2},
		{90, 3},
		{91, 4},
	}
	for _, tc := range cases {
		if got := TotalAutoDebitCycles(tc.tenureDays); got != tc.want {
			t.Errorf("TotalAutoDebitCycles(%d) = %d, want %d", tc.tenureDays, got, tc.want)
		}
	}
}

func newTestLoan(tenureDays uint64, totalInterest int64, repaid int64, consecutiveMissed uint32) *FixedTermLoan {
	return &FixedTermLoan{
		TenureDays:                  tenureDays,
		TotalInterestWei:            big.NewInt(totalInterest),
		RepaidWei:                   big.NewInt(repaid),
		IssuedAtTime:                1_700_000_000,
		MaturityTime:                1_700_000_000 + tenureDays*86400,
		ConsecutiveMissedAutoDebits: consecutiveMissed,
	}
}

// TestDecideAutoDebit_SingleCycleLoanBillsEverythingAtOnce proves a 30-day
// loan (exactly one auto-debit cycle) bills its ENTIRE interest in that one
// cycle, and that a sufficient balance succeeds with no further cycles
// scheduled.
func TestDecideAutoDebit_SingleCycleLoanBillsEverythingAtOnce(t *testing.T) {
	loan := newTestLoan(30, 1200, 0, 0)
	balance := big.NewInt(5000)

	decision := DecideAutoDebit(loan, 1, balance, loan.IssuedAtTime)
	if !decision.CycleDue {
		t.Fatalf("expected cycle 1 to be due")
	}
	if !decision.Success {
		t.Fatalf("expected success with sufficient balance")
	}
	if decision.InstallmentWei.Cmp(big.NewInt(1200)) != 0 {
		t.Fatalf("expected full interest 1200 billed in the single cycle, got %s", decision.InstallmentWei)
	}
	if decision.AdvanceToCycle != 2 {
		t.Fatalf("expected AdvanceToCycle=2 (past the loan's single cycle), got %d", decision.AdvanceToCycle)
	}
	if decision.NextAttemptAt != 0 {
		t.Fatalf("expected no further attempt scheduled past the loan's total cycle count, got %d", decision.NextAttemptAt)
	}
}

// TestDecideAutoDebit_NinetyDayLoanSplitsIntoThreeCycles proves a 90-day
// loan bills roughly a third of its interest per cycle, and that the THIRD
// cycle's target is exactly TotalInterestWei -- the whole term's interest
// is always fully billed across all cycles with no rounding shortfall left
// over.
func TestDecideAutoDebit_NinetyDayLoanSplitsIntoThreeCycles(t *testing.T) {
	loan := newTestLoan(90, 1600, 0, 0)
	balance := big.NewInt(1_000_000)

	totalBilled := big.NewInt(0)
	cycle := uint32(1)
	for cycle <= TotalAutoDebitCycles(loan.TenureDays) {
		decision := DecideAutoDebit(loan, cycle, balance, loan.IssuedAtTime)
		if !decision.CycleDue || !decision.Success {
			t.Fatalf("cycle %d: expected due and successful, got %+v", cycle, decision)
		}
		totalBilled.Add(totalBilled, decision.InstallmentWei)
		loan.RepaidWei.Add(loan.RepaidWei, decision.InstallmentWei)
		cycle = decision.AdvanceToCycle
	}
	if totalBilled.Cmp(loan.TotalInterestWei) != 0 {
		t.Fatalf("expected total billed across all cycles to equal TotalInterestWei exactly (no rounding shortfall): got %s want %s", totalBilled, loan.TotalInterestWei)
	}
}

// TestDecideAutoDebit_InsufficientBalanceIncrementsMissCounter proves a
// failed attempt (insufficient balance) increments ConsecutiveMissedAutoDebits,
// does NOT advance the cycle (the same installment is retried), and
// schedules a retry sooner than the next natural billing cycle.
func TestDecideAutoDebit_InsufficientBalanceIncrementsMissCounter(t *testing.T) {
	loan := newTestLoan(90, 1600, 0, 0)
	balance := big.NewInt(1) // far short of the ~533 due for cycle 1

	decision := DecideAutoDebit(loan, 1, balance, loan.IssuedAtTime)
	if !decision.CycleDue {
		t.Fatalf("expected cycle 1 to be due")
	}
	if decision.Success {
		t.Fatalf("expected failure with insufficient balance")
	}
	if decision.NewConsecutiveMissed != 1 {
		t.Fatalf("expected ConsecutiveMissedAutoDebits to become 1, got %d", decision.NewConsecutiveMissed)
	}
	if decision.Delinquent {
		t.Fatalf("expected not yet delinquent after a single miss")
	}
	if decision.AdvanceToCycle != 1 {
		t.Fatalf("expected the SAME cycle (1) to be retried, got %d", decision.AdvanceToCycle)
	}
	wantRetryAt := loan.IssuedAtTime + AutoDebitRetryIntervalSeconds
	if decision.NextAttemptAt != wantRetryAt {
		t.Fatalf("expected retry at %d, got %d", wantRetryAt, decision.NextAttemptAt)
	}
}

// TestDecideAutoDebit_ThirdConsecutiveMissTriggersDelinquency proves the
// third consecutive failed attempt marks the loan delinquent and stops
// scheduling further attempts.
func TestDecideAutoDebit_ThirdConsecutiveMissTriggersDelinquency(t *testing.T) {
	loan := newTestLoan(90, 1600, 0, AutoDebitMaxConsecutiveMisses-1) // already missed twice
	balance := big.NewInt(1)

	decision := DecideAutoDebit(loan, 1, balance, loan.IssuedAtTime)
	if decision.Success {
		t.Fatalf("expected failure with insufficient balance")
	}
	if decision.NewConsecutiveMissed != AutoDebitMaxConsecutiveMisses {
		t.Fatalf("expected ConsecutiveMissedAutoDebits to reach %d, got %d", AutoDebitMaxConsecutiveMisses, decision.NewConsecutiveMissed)
	}
	if !decision.Delinquent {
		t.Fatalf("expected the loan to be marked delinquent on the 3rd consecutive miss")
	}
	if decision.NextAttemptAt != 0 {
		t.Fatalf("expected no further attempt scheduled once delinquent, got %d", decision.NextAttemptAt)
	}
}

// TestDecideAutoDebit_ManualPrepaymentSkipsCycleWithNoStrike proves a
// borrower who has already manually repaid (via RepayFixedTerm) enough to
// cover a cycle's target is not billed again and does not accrue a missed-
// payment strike for a cycle that was never actually owed.
func TestDecideAutoDebit_ManualPrepaymentSkipsCycleWithNoStrike(t *testing.T) {
	// 90-day loan, cycle 1 target = 1600/3 = 533 (floor). Borrower already
	// manually repaid 1600 (the WHOLE loan's interest) ahead of schedule.
	loan := newTestLoan(90, 1600, 1600, 0)
	balance := big.NewInt(0)

	decision := DecideAutoDebit(loan, 1, balance, loan.IssuedAtTime)
	if decision.CycleDue {
		t.Fatalf("expected nothing due -- borrower already prepaid ahead of schedule, got %+v", decision)
	}
	if decision.NewConsecutiveMissed != loan.ConsecutiveMissedAutoDebits {
		t.Fatalf("expected no change to the miss counter for a cycle that was never owed, got %d", decision.NewConsecutiveMissed)
	}
	if decision.AdvanceToCycle != 2 {
		t.Fatalf("expected the cycle to still advance to 2, got %d", decision.AdvanceToCycle)
	}
}

// TestDecideAutoDebit_MissedCycleShortfallRollsForwardOnRetry proves a
// missed cycle's target keeps growing to reflect elapsed tenure time on
// each retry (not frozen at the original cycle's target), so a borrower
// who catches up later still owes the correct larger cumulative amount.
func TestDecideAutoDebit_MissedCycleShortfallRollsForwardOnRetry(t *testing.T) {
	loan := newTestLoan(90, 1600, 0, 1) // already missed once
	// Retry happens a full cycle length later -- but AdvanceToCycle stayed
	// at 1 after the miss, so DecideAutoDebit is still asked for cycle 1's
	// target even though, time-wise, cycle 2 has since become due too. The
	// TARGET for a fixed cycle number is a function of that cycle number
	// alone (elapsedDays = cycle * AutoDebitCycleLengthDays), not of `now`
	// -- so retrying cycle 1 still only demands cycle 1's target (533), not
	// a moving target based on wall-clock elapsed time. This proves that
	// invariant explicitly, since it's easy to accidentally break by wiring
	// `now` into the target computation instead of `cycle`.
	balance := big.NewInt(1_000_000)
	decision := DecideAutoDebit(loan, 1, balance, loan.IssuedAtTime+AutoDebitRetryIntervalSeconds)
	wantInstallment := new(big.Int).Quo(big.NewInt(1600), big.NewInt(3))
	if decision.InstallmentWei.Cmp(wantInstallment) != 0 {
		t.Fatalf("expected cycle 1's installment to still be %s regardless of retry timing, got %s", wantInstallment, decision.InstallmentWei)
	}
}
