package lending

import "math/big"

// AutoDebitCycleLengthDays is the billing cadence for fixed-term interest
// installments: a 30-day loan bills once, at maturity; a 90-day loan bills
// at day 30, day 60, and day 90. Deliberately a package constant, not a
// governance/RiskParameters field, for the same reason DefaultFixedTermRateSchedule
// started as one before rates specifically became governance-adjustable --
// keep the surface area additive and scoped until there's a real need to
// widen it.
const AutoDebitCycleLengthDays = 30

// AutoDebitRetryIntervalSeconds is how soon a missed installment is
// re-attempted -- mirrors native/subscriptions.Config.RetryIntervalSeconds'
// dunning shape (short retries within a billing cycle, not a full cycle's
// wait), giving a borrower with a temporarily low balance several chances
// to top up before ConsecutiveMissedAutoDebits reaches the delinquency
// threshold.
const AutoDebitRetryIntervalSeconds = 86_400 // 1 day

// AutoDebitMaxConsecutiveMisses is how many auto-debit attempts in a row
// may fail (for insufficient balance) before the loan is marked
// FixedTermLoanStatusDelinquent -- independent of and in addition to the
// LTV-based Liquidate path (see combinedDebtWei's doc comment for why
// fixed-term debt is deliberately excluded from that one). See
// FixedTermLoanStatusDelinquent's own doc comment for why this stops at
// flagging rather than seizing collateral itself.
const AutoDebitMaxConsecutiveMisses = 3

const secondsPerDayAutoDebit = 86_400

// TotalAutoDebitCycles returns how many interest installments a loan of the
// given tenure bills across its term -- ceil(tenureDays / AutoDebitCycleLengthDays),
// so any tenure (not just the 30/90-day defaults; the rate schedule is
// governance-adjustable to other lengths) collapses correctly: a tenure
// shorter than one cycle still gets exactly one installment, due at
// maturity.
func TotalAutoDebitCycles(tenureDays uint64) uint32 {
	if tenureDays == 0 {
		return 0
	}
	cycles := (tenureDays + AutoDebitCycleLengthDays - 1) / AutoDebitCycleLengthDays
	if cycles == 0 {
		cycles = 1
	}
	return uint32(cycles)
}

// autoDebitCycleDueTime returns the timestamp (Unix seconds) the given
// 1-based cycle is due -- IssuedAtTime plus cycle*AutoDebitCycleLengthDays,
// capped at MaturityTime so the final cycle never falls after the loan's
// own term ends.
func autoDebitCycleDueTime(loan *FixedTermLoan, cycle uint32) uint64 {
	if loan == nil || cycle == 0 {
		return 0
	}
	due := loan.IssuedAtTime + uint64(cycle)*AutoDebitCycleLengthDays*secondsPerDayAutoDebit
	if due > loan.MaturityTime {
		due = loan.MaturityTime
	}
	return due
}

// cumulativeInterestByCycle returns the cumulative interest that should have
// accrued by the end of the given 1-based cycle out of totalInterest owed
// across tenureDays: totalInterest scaled by how much of the tenure has
// elapsed at that cycle's due time, floored (favors whichever side owes
// less-than-proportionally: never demands more, or promises more, than
// what's proportionally due by that point). The final cycle's value is
// exactly totalInterest (elapsedDays == tenureDays), so the whole term's
// interest always fully reconciles across all cycles with no rounding
// shortfall left over. Shared by both directions of the fixed-term cycle
// mechanism: autoDebitTargetInterestWei (loan interest collected FROM a
// borrower) and depositInterestTargetWei (deposit interest paid TO a
// depositor) -- identical math, opposite direction of money flow.
func cumulativeInterestByCycle(totalInterest *big.Int, tenureDays uint64, cycle uint32) *big.Int {
	if totalInterest == nil || tenureDays == 0 {
		return big.NewInt(0)
	}
	elapsedDays := uint64(cycle) * AutoDebitCycleLengthDays
	if elapsedDays > tenureDays {
		elapsedDays = tenureDays
	}
	target := new(big.Int).Mul(totalInterest, new(big.Int).SetUint64(elapsedDays))
	return target.Quo(target, new(big.Int).SetUint64(tenureDays))
}

// autoDebitTargetInterestWei returns the cumulative interest that should
// have been collected by the end of the given 1-based cycle -- see
// cumulativeInterestByCycle for the shared formula.
func autoDebitTargetInterestWei(loan *FixedTermLoan) func(cycle uint32) *big.Int {
	return func(cycle uint32) *big.Int {
		if loan == nil {
			return big.NewInt(0)
		}
		return cumulativeInterestByCycle(loan.TotalInterestWei, loan.TenureDays, cycle)
	}
}

// AutoDebitDecision is the pure, deterministic outcome of attempting one
// auto-debit cycle against a fixed-term loan, mirroring
// native/subscriptions.ChargeDecision's shape: no trie/account access, fully
// unit-testable without a live chain. core/lending_autodebit_settlement.go
// owns reading the live balance and applying whatever this decision implies.
type AutoDebitDecision struct {
	// CycleDue is false when nothing is actually owed for this cycle (the
	// borrower already repaid ahead of schedule via a manual RepayFixedTerm)
	// -- the settlement hook advances NextAutoDebitCycle with no debit
	// attempt and no effect on ConsecutiveMissedAutoDebits either way.
	CycleDue bool
	// InstallmentWei is the amount due this cycle, zero when !CycleDue.
	InstallmentWei *big.Int
	// Success is only meaningful when CycleDue is true.
	Success bool
	// NewConsecutiveMissed is the value ConsecutiveMissedAutoDebits should
	// be set to after this attempt.
	NewConsecutiveMissed uint32
	// Delinquent is true the moment NewConsecutiveMissed reaches
	// AutoDebitMaxConsecutiveMisses -- the settlement hook marks the loan
	// FixedTermLoanStatusDelinquent and stops scheduling further attempts,
	// instead of re-bucketing for retry.
	Delinquent bool
	// AdvanceToCycle is the cycle number NextAutoDebitCycle should be set to
	// -- unchanged (same cycle, for a same-cycle retry) on failure, cycle+1
	// on success or when nothing was due.
	AdvanceToCycle uint32
	// NextAttemptAt is the timestamp to bucket the next attempt at -- the
	// next cycle's own due time on success/not-due, or now+AutoDebitRetryIntervalSeconds
	// on a non-delinquent failure. Zero (nothing to schedule) once
	// AdvanceToCycle exceeds the loan's total cycle count, or once
	// Delinquent is true.
	NextAttemptAt uint64
}

// DecideAutoDebit computes the outcome of one settlement attempt against a
// fixed-term loan's current cycle. balanceWei is the borrower's live NHB
// balance; now is the settlement hook's own deterministic block timestamp.
func DecideAutoDebit(loan *FixedTermLoan, cycle uint32, balanceWei *big.Int, now uint64) AutoDebitDecision {
	if loan == nil {
		return AutoDebitDecision{}
	}
	totalCycles := TotalAutoDebitCycles(loan.TenureDays)
	targetFn := autoDebitTargetInterestWei(loan)

	alreadyCollected := loan.RepaidWei
	if alreadyCollected == nil {
		alreadyCollected = big.NewInt(0)
	}
	if loan.TotalInterestWei != nil && alreadyCollected.Cmp(loan.TotalInterestWei) > 0 {
		alreadyCollected = loan.TotalInterestWei
	}

	target := targetFn(cycle)
	installment := new(big.Int).Sub(target, alreadyCollected)

	nextCycleAttemptAt := func(nextCycle uint32) uint64 {
		if nextCycle > totalCycles {
			return 0
		}
		return autoDebitCycleDueTime(loan, nextCycle)
	}

	if installment.Sign() <= 0 {
		// Already prepaid at or beyond this cycle's target -- nothing to
		// collect, no strike either way.
		return AutoDebitDecision{
			CycleDue:             false,
			InstallmentWei:       big.NewInt(0),
			NewConsecutiveMissed: loan.ConsecutiveMissedAutoDebits,
			AdvanceToCycle:       cycle + 1,
			NextAttemptAt:        nextCycleAttemptAt(cycle + 1),
		}
	}

	balance := balanceWei
	if balance == nil {
		balance = big.NewInt(0)
	}
	if balance.Cmp(installment) >= 0 {
		return AutoDebitDecision{
			CycleDue:             true,
			InstallmentWei:       installment,
			Success:              true,
			NewConsecutiveMissed: 0,
			AdvanceToCycle:       cycle + 1,
			NextAttemptAt:        nextCycleAttemptAt(cycle + 1),
		}
	}

	missed := loan.ConsecutiveMissedAutoDebits + 1
	if missed >= AutoDebitMaxConsecutiveMisses {
		return AutoDebitDecision{
			CycleDue:             true,
			InstallmentWei:       installment,
			Success:              false,
			NewConsecutiveMissed: missed,
			Delinquent:           true,
			AdvanceToCycle:       cycle,
			NextAttemptAt:        0,
		}
	}
	return AutoDebitDecision{
		CycleDue:             true,
		InstallmentWei:       installment,
		Success:              false,
		NewConsecutiveMissed: missed,
		AdvanceToCycle:       cycle,
		NextAttemptAt:        now + AutoDebitRetryIntervalSeconds,
	}
}
