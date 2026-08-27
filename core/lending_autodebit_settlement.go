package core

import (
	"errors"
	"fmt"
	"math/big"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
)

// settleLendingAutoDebits is called unconditionally at the top of
// ProcessBlockLifecycle (core/epochs.go), every block -- mirrors
// settleSubscriptionCharges exactly, including its day-watermark shape (see
// that function's own doc comment for the full "why unconditional, why
// day-gated, why catch-up-safe" reasoning, which applies identically here:
// fixed-term billing cadence, lending.AutoDebitCycleLengthDays, has no
// natural relationship to epochConfig.Length).
//
// THE CORE SAFETY ARGUMENT for debiting a borrower's account with zero
// fresh signature at collection time is the same "bounded standing
// authorization" already established for subscriptions: the amount
// collected each cycle is a strict fraction of FixedTermLoan.TotalInterestWei,
// itself computed once and locked in at issuance -- explicitly disclosed
// and authorized by the borrower's own envelope signature on the original
// TxTypeLendingBorrowFixedTerm transaction. Never an open-ended or
// system-chosen amount, and RepayFixedTerm (which this reuses to actually
// apply the debit) caps whatever it's given at the loan's own
// OutstandingWei() regardless.
//
// A failure to collect one installment (insufficient balance) NEVER
// returns an error from this function -- that would abort block production
// over an ordinary, expected business outcome, exactly like a failed
// subscription charge. Only genuine internal/storage errors propagate.
func (sp *StateProcessor) settleLendingAutoDebits(timestamp int64) error {
	if timestamp < 0 {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	today := uint64(timestamp) / secondsPerDay

	// Same reasoning as settleSubscriptionCharges: today's bucket can still
	// receive new entries later in the same calendar day, so it's rescanned
	// every block while it remains "today"; only a fully-elapsed day is
	// closed for good.
	lastClosed, hasWatermark, err := manager.LendingAutoDebitLastProcessedDay()
	if err != nil {
		return err
	}
	startDay := uint64(0)
	if hasWatermark {
		startDay = lastClosed + 1
	}
	if startDay > today {
		startDay = today
	}

	for day := startDay; day <= today; day++ {
		if err := sp.settleLendingAutoDebitsDueOnDay(manager, day, uint64(timestamp)); err != nil {
			return err
		}
	}

	if today == 0 {
		return nil
	}
	newWatermark := today - 1
	if hasWatermark && newWatermark <= lastClosed {
		return nil
	}
	return manager.LendingAutoDebitSetLastProcessedDay(newWatermark)
}

func (sp *StateProcessor) settleLendingAutoDebitsDueOnDay(manager *nhbstate.Manager, day uint64, now uint64) error {
	due, err := manager.LendingAutoDebitDueOnDay(day)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	for _, loanID := range due {
		if err := sp.settleOneLendingAutoDebit(manager, loanID, now); err != nil {
			return err
		}
	}
	return manager.LendingAutoDebitClearDue(day)
}

func (sp *StateProcessor) settleOneLendingAutoDebit(manager *nhbstate.Manager, loanID [32]byte, now uint64) error {
	loan, exists, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		return fmt.Errorf("lending autodebit: load loan %x: %w", loanID, err)
	}
	if !exists || loan == nil {
		// Nothing to do -- not an error (mirrors settleOneSubscriptionCharge).
		return nil
	}
	// A loan can reach a terminal status (repaid, or already delinquent)
	// while still sitting in a due-index bucket -- e.g. the borrower
	// manually repaid in full between scheduling and this attempt. Skip
	// silently: expected, not a bug, same as subscriptions' equivalent
	// guard.
	if loan.Status != lending.FixedTermLoanStatusActive {
		return nil
	}
	if !loan.AutoDebitEnabled {
		return nil
	}

	cycle := loan.NextAutoDebitCycle
	if cycle == 0 {
		cycle = 1
	}
	totalCycles := lending.TotalAutoDebitCycles(loan.TenureDays)
	if cycle > totalCycles {
		// Fully auto-billed already -- nothing left to schedule. The
		// borrower still owes any remaining principal (and whatever
		// interest auto-debit didn't collect) via a manual RepayFixedTerm.
		return nil
	}

	borrowerBytes := loan.Borrower.Bytes()
	borrowerAcc, err := sp.getAccount(borrowerBytes)
	if err != nil {
		return fmt.Errorf("lending autodebit: load borrower %x: %w", borrowerBytes, err)
	}
	balance := borrowerAcc.BalanceNHB
	if balance == nil {
		balance = big.NewInt(0)
	}

	decision := lending.DecideAutoDebit(loan, cycle, balance, now)

	if !decision.CycleDue {
		loan.NextAutoDebitCycle = decision.AdvanceToCycle
		if err := manager.LendingPutFixedTermLoan(loan); err != nil {
			return err
		}
		return sp.scheduleNextLendingAutoDebit(manager, loanID, decision.NextAttemptAt)
	}

	if decision.Success {
		return sp.applySuccessfulLendingAutoDebit(manager, loan, loanID, cycle, decision, now)
	}
	return sp.applyFailedLendingAutoDebit(manager, loan, loanID, cycle, decision, now)
}

func (sp *StateProcessor) applySuccessfulLendingAutoDebit(manager *nhbstate.Manager, loan *lending.FixedTermLoan, loanID [32]byte, cycle uint32, decision lending.AutoDebitDecision, now uint64) error {
	engine, _, err := sp.lendingEngine(loan.PoolID)
	if err != nil {
		return err
	}
	// Reuses RepayFixedTerm verbatim -- same interest-first accounting,
	// same pool-routing (SupplyIndex bump) for the interest portion, same
	// balance debit -- rather than duplicating any of that here. Caps
	// whatever it's given at the loan's own OutstandingWei() regardless, so
	// even a rounding bug in decision.InstallmentWei could never overcharge.
	if _, err := engine.RepayFixedTerm(loan.Borrower, decision.InstallmentWei); err != nil {
		if isExpectedAutoDebitBusinessError(err) {
			// DecideAutoDebit already confirmed sufficient balance, but
			// something ordinary and reversible (e.g. an operator pausing
			// lending.Repay, or the loan's own status having changed
			// between scheduling and this attempt) blocked the actual
			// debit. Treat exactly like an insufficient-balance miss --
			// re-invoking DecideAutoDebit with a zero balance deterministically
			// walks the SAME failure branch (same installment target, same
			// ConsecutiveMissedAutoDebits/delinquency math) rather than
			// duplicating that logic here. Only a genuinely unexpected
			// internal/storage error may abort the block -- see this
			// function's own doc comment.
			failure := lending.DecideAutoDebit(loan, cycle, big.NewInt(0), now)
			return sp.applyFailedLendingAutoDebit(manager, loan, loanID, cycle, failure, now)
		}
		return fmt.Errorf("lending autodebit: apply installment for loan %x: %w", loanID, err)
	}

	// RepayFixedTerm just persisted its own updated RepaidWei/Status --
	// reload before layering the auto-debit-specific fields on top so this
	// never clobbers what it just wrote.
	reloaded, exists, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		return err
	}
	if !exists || reloaded == nil {
		return fmt.Errorf("lending autodebit: loan %x vanished after repay", loanID)
	}
	reloaded.ConsecutiveMissedAutoDebits = 0
	reloaded.NextAutoDebitCycle = decision.AdvanceToCycle
	if err := manager.LendingPutFixedTermLoan(reloaded); err != nil {
		return err
	}

	if evt := (events.LendingAutoDebitSucceeded{
		LoanID:         loanID,
		Borrower:       borrowerBytes20(loan),
		PoolID:         loan.PoolID,
		Cycle:          cycle,
		InstallmentWei: decision.InstallmentWei,
		NextCycle:      decision.AdvanceToCycle,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	if reloaded.Status != lending.FixedTermLoanStatusActive {
		// Repaid in full by this very installment -- nothing more to
		// schedule.
		return nil
	}
	return sp.scheduleNextLendingAutoDebit(manager, loanID, decision.NextAttemptAt)
}

func (sp *StateProcessor) applyFailedLendingAutoDebit(manager *nhbstate.Manager, loan *lending.FixedTermLoan, loanID [32]byte, cycle uint32, decision lending.AutoDebitDecision, now uint64) error {
	loan.ConsecutiveMissedAutoDebits = decision.NewConsecutiveMissed
	if decision.Delinquent {
		loan.Status = lending.FixedTermLoanStatusDelinquent
	}
	if err := manager.LendingPutFixedTermLoan(loan); err != nil {
		return err
	}

	if decision.Delinquent {
		if evt := (events.LendingFixedTermLoanDelinquent{
			LoanID:         loanID,
			Borrower:       borrowerBytes20(loan),
			PoolID:         loan.PoolID,
			OutstandingWei: loan.OutstandingWei(),
		}).Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return nil
	}

	if evt := (events.LendingAutoDebitFailed{
		LoanID:            loanID,
		Borrower:          borrowerBytes20(loan),
		PoolID:            loan.PoolID,
		Cycle:             cycle,
		InstallmentWei:    decision.InstallmentWei,
		ConsecutiveMissed: decision.NewConsecutiveMissed,
		NextAttemptAt:     decision.NextAttemptAt,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.scheduleNextLendingAutoDebit(manager, loanID, decision.NextAttemptAt)
}

func (sp *StateProcessor) scheduleNextLendingAutoDebit(manager *nhbstate.Manager, loanID [32]byte, nextAttemptAt uint64) error {
	if nextAttemptAt == 0 {
		return nil
	}
	nextDay := nextAttemptAt / secondsPerDay
	return manager.LendingAutoDebitAppendDue(nextDay, loanID)
}

func borrowerBytes20(loan *lending.FixedTermLoan) [20]byte {
	var out [20]byte
	copy(out[:], loan.Borrower.Bytes())
	return out
}

// isExpectedAutoDebitBusinessError reports whether err is one of
// RepayFixedTerm's ordinary, non-storage business/operational outcomes --
// an operator pause, a loan that reached a terminal status or an
// insufficient balance between DecideAutoDebit's own check and this
// attempt -- rather than a genuine internal/storage error. Adversarial
// review confirmed the earlier version of this file treated ANY error from
// RepayFixedTerm as fatal, which meant an ordinary operator pause of
// lending.Repay (or the whole lending module) would abort block production
// on every subsequent block-build/verify attempt for as long as any loan
// had an auto-debit installment due -- turning a fully reversible
// operational lever into a chain-wide halt. Only errors NOT matched here
// may still propagate as fatal.
func isExpectedAutoDebitBusinessError(err error) bool {
	return errors.Is(err, nativecommon.ErrModulePaused) ||
		errors.Is(err, lending.ErrRepayPaused) ||
		errors.Is(err, lending.ErrNoDebtToRepay) ||
		errors.Is(err, lending.ErrInsufficientBalance)
}
