package lending

import "math/big"

// depositCycleDueTime returns the timestamp (Unix seconds) the given 1-based
// cycle is due for a periodic-payout deposit -- same formula as
// autoDebitCycleDueTime, mirrored for the deposit side.
func depositCycleDueTime(deposit *FixedTermDeposit, cycle uint32) uint64 {
	if deposit == nil || cycle == 0 {
		return 0
	}
	due := deposit.IssuedAtTime + uint64(cycle)*AutoDebitCycleLengthDays*secondsPerDayAutoDebit
	if due > deposit.MaturityTime {
		due = deposit.MaturityTime
	}
	return due
}

// DepositPayoutDecision is the pure, deterministic outcome of attempting one
// settlement step against a fixed-term deposit, mirroring AutoDebitDecision's
// shape on the payout side: no trie/account access here, fully
// unit-testable without a live chain.
type DepositPayoutDecision struct {
	// InterestDue is the interest installment to attempt paying this step
	// (zero if none due).
	InterestDue *big.Int
	// PrincipalDue is true when principal should also be paid THIS SAME
	// step: for FixedTermDepositPayoutLumpSumAtMaturity, always (there is
	// only ever one step); for FixedTermDepositPayoutPeriodic, only once
	// the final interest cycle is reached. Principal is deliberately never
	// deferred to a separate follow-up step scheduled at the SAME
	// timestamp as the final interest cycle -- the settlement day-bucket
	// drain/clear pattern (settleLendingDepositPayoutsDueOnDay) snapshots a
	// day's due entries once and clears the whole bucket after processing,
	// so a second entry appended to that SAME day mid-pass would be
	// silently wiped by that same clear. Paying both together in one step
	// avoids ever needing that same-day re-schedule.
	PrincipalDue bool
	// AdvanceToCycle is the next NextPayoutCycle value (periodic payout
	// only; unused/zero for lump-sum).
	AdvanceToCycle uint32
	// NextAttemptAt is when to schedule the next attempt, 0 if none (the
	// deposit is fully settled once this step succeeds).
	NextAttemptAt uint64
}

// DecideDepositPayout computes what settleLendingDepositPayouts should
// attempt for this deposit at its current step.
func DecideDepositPayout(deposit *FixedTermDeposit) DepositPayoutDecision {
	if deposit == nil {
		return DepositPayoutDecision{InterestDue: big.NewInt(0)}
	}

	if deposit.Payout == FixedTermDepositPayoutLumpSumAtMaturity {
		// Single event, at maturity: everything at once.
		return DepositPayoutDecision{
			InterestDue:  deposit.OutstandingInterestWei(),
			PrincipalDue: true,
		}
	}

	// Periodic.
	totalCycles := TotalAutoDebitCycles(deposit.TenureDays)
	cycle := deposit.NextPayoutCycle
	if cycle == 0 {
		cycle = 1
	}

	target := cumulativeInterestByCycle(deposit.TotalInterestOwedWei, deposit.TenureDays, cycle)
	paid := deposit.PaidInterestWei
	if paid == nil {
		paid = big.NewInt(0)
	}
	installment := new(big.Int).Sub(target, paid)
	if installment.Sign() < 0 {
		installment = big.NewInt(0)
	}

	isFinalCycle := cycle >= totalCycles
	decision := DepositPayoutDecision{
		InterestDue:    installment,
		PrincipalDue:   isFinalCycle,
		AdvanceToCycle: cycle + 1,
	}
	if !isFinalCycle {
		decision.NextAttemptAt = depositCycleDueTime(deposit, cycle+1)
	}
	// isFinalCycle: NextAttemptAt stays 0 -- interest and principal are
	// handled together in this one step; nothing more to schedule once it
	// succeeds.
	return decision
}
