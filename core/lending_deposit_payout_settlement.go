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

// settleLendingDepositPayouts is called unconditionally at the top of
// ProcessBlockLifecycle, alongside settleLendingAutoDebits -- Milestone 3's
// mirror-image settlement hook, in the opposite direction of money flow
// (paying a fixed-term depositor rather than collecting from a fixed-term
// borrower). Shares the exact same day-watermark shape as
// settleLendingAutoDebits/settleSubscriptionCharges -- see either's own doc
// comment for the full "why unconditional, why day-gated, why
// catch-up-safe" reasoning.
//
// Unlike auto-debit (which pulls money FROM a borrower and therefore needs
// the "bounded standing authorization" safety argument), this function only
// ever pushes money TO a depositor -- their own principal and interest,
// nothing taken from anyone without consent. The only real risk here is
// timing (the fixed-term deposit reserve or general pool liquidity hasn't
// caught up with an obligation that's come due yet), handled as a soft,
// retryable outcome -- never as an error that aborts block production.
func (sp *StateProcessor) settleLendingDepositPayouts(timestamp int64) error {
	if timestamp < 0 {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	today := uint64(timestamp) / secondsPerDay

	lastClosed, hasWatermark, err := manager.LendingDepositPayoutLastProcessedDay()
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
		if err := sp.settleLendingDepositPayoutsDueOnDay(manager, day, uint64(timestamp)); err != nil {
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
	return manager.LendingDepositPayoutSetLastProcessedDay(newWatermark)
}

func (sp *StateProcessor) settleLendingDepositPayoutsDueOnDay(manager *nhbstate.Manager, day uint64, now uint64) error {
	due, err := manager.LendingDepositPayoutDueOnDay(day)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	for _, depositID := range due {
		if err := sp.settleOneLendingDepositPayout(manager, depositID, now); err != nil {
			return err
		}
	}
	return manager.LendingDepositPayoutClearDue(day)
}

func (sp *StateProcessor) settleOneLendingDepositPayout(manager *nhbstate.Manager, depositID [32]byte, now uint64) error {
	deposit, exists, err := manager.LendingGetFixedTermDeposit(depositID)
	if err != nil {
		return fmt.Errorf("lending deposit payout: load deposit %x: %w", depositID, err)
	}
	if !exists || deposit == nil {
		// Nothing to do -- not an error (mirrors settleOneLendingAutoDebit).
		return nil
	}
	if deposit.Status != lending.FixedTermDepositStatusActive {
		return nil
	}

	decision := lending.DecideDepositPayout(deposit)

	engine, market, err := sp.lendingEngine(deposit.PoolID)
	if err != nil {
		return err
	}
	if market == nil {
		return fmt.Errorf("lending deposit payout: market %q not found", deposit.PoolID)
	}

	interestPaid := big.NewInt(0)
	if decision.InterestDue != nil && decision.InterestDue.Sign() > 0 {
		if err := engine.PayFixedTermDepositInterest(deposit, market, decision.InterestDue); err != nil {
			if isExpectedDepositPayoutBusinessError(err) {
				return sp.delayLendingDepositPayout(manager, deposit, market, depositID, err, now)
			}
			return fmt.Errorf("lending deposit payout: pay interest for deposit %x: %w", depositID, err)
		}
		interestPaid = decision.InterestDue
		// Persist NOW, before ever attempting the separately-failable
		// principal step below. PayFixedTermDepositInterest already moved
		// real NHB to the depositor -- that transfer cannot be undone -- so
		// its bookkeeping mutation (PaidInterestWei, the reserve and
		// TotalFixedTermDepositInterestOwedWei decrements) must be durable
		// before any chance of a delayed retry. Deferring both persists to
		// the end (as this used to) meant a principal-step failure discarded
		// this mutation entirely: a retry would reload the pre-payment
		// deposit, re-decide the same installment as still fully owed, and
		// pay it a second time out of the reserve.
		if err := manager.LendingPutFixedTermDeposit(deposit); err != nil {
			return err
		}
		if err := manager.LendingPutMarket(deposit.PoolID, market); err != nil {
			return err
		}
	}

	principalPaid := big.NewInt(0)
	if decision.PrincipalDue {
		if err := engine.PayFixedTermDepositPrincipal(deposit, market); err != nil {
			if isExpectedDepositPayoutBusinessError(err) {
				// Interest (if any due this step) already succeeded above
				// and was ALREADY persisted (see the interest branch above)
				// -- only principal needs a retry. Recomputing the decision
				// on that retry will correctly show zero interest still due
				// (PaidInterestWei was durably bumped), so this never
				// double-pays.
				return sp.delayLendingDepositPayout(manager, deposit, market, depositID, err, now)
			}
			return fmt.Errorf("lending deposit payout: pay principal for deposit %x: %w", depositID, err)
		}
		principalPaid = new(big.Int).Set(deposit.PrincipalWei)
	} else {
		deposit.NextPayoutCycle = decision.AdvanceToCycle
	}

	if err := manager.LendingPutFixedTermDeposit(deposit); err != nil {
		return err
	}
	if err := manager.LendingPutMarket(deposit.PoolID, market); err != nil {
		return err
	}

	if evt := (events.LendingDepositPayoutSucceeded{
		DepositID:        depositID,
		Depositor:        depositorBytes20(deposit),
		PoolID:           deposit.PoolID,
		InterestPaidWei:  interestPaid,
		PrincipalPaidWei: principalPaid,
		Matured:          deposit.Status == lending.FixedTermDepositStatusMatured,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	if deposit.Status == lending.FixedTermDepositStatusActive && decision.NextAttemptAt > 0 {
		return sp.scheduleNextLendingDepositPayout(manager, depositID, decision.NextAttemptAt)
	}
	return nil
}

// delayLendingDepositPayout reschedules a payout attempt that hit a soft,
// expected timing mismatch. This function itself never persists deposit or
// market -- any step's mutation that needs to survive a delayed retry must
// already have been persisted by its own caller before delayLendingDepositPayout
// is ever invoked (see the interest branch in settleOneLendingDepositPayout,
// which persists immediately upon success, precisely so that an interest
// payment already applied to real balances is never silently replayed on a
// later principal-only retry).
func (sp *StateProcessor) delayLendingDepositPayout(manager *nhbstate.Manager, deposit *lending.FixedTermDeposit, market *lending.Market, depositID [32]byte, cause error, now uint64) error {
	nextAttemptAt := now + lending.AutoDebitRetryIntervalSeconds
	if evt := (events.LendingDepositPayoutDelayed{
		DepositID:     depositID,
		Depositor:     depositorBytes20(deposit),
		PoolID:        deposit.PoolID,
		Reason:        cause.Error(),
		NextAttemptAt: nextAttemptAt,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.scheduleNextLendingDepositPayout(manager, depositID, nextAttemptAt)
}

func (sp *StateProcessor) scheduleNextLendingDepositPayout(manager *nhbstate.Manager, depositID [32]byte, nextAttemptAt uint64) error {
	if nextAttemptAt == 0 {
		return nil
	}
	nextDay := nextAttemptAt / secondsPerDay
	return manager.LendingDepositPayoutAppendDue(nextDay, depositID)
}

func depositorBytes20(deposit *lending.FixedTermDeposit) [20]byte {
	var out [20]byte
	copy(out[:], deposit.Depositor.Bytes())
	return out
}

// isExpectedDepositPayoutBusinessError reports whether err is one of
// PayFixedTermDepositInterest/PayFixedTermDepositPrincipal's ordinary,
// non-storage business/operational outcomes -- an operator pause, or a
// genuine timing mismatch between when fixed-term loan interest has
// actually been collected (or general pool liquidity is available) and
// when a deposit payout comes due -- rather than a genuine internal/storage
// error. Mirrors isExpectedAutoDebitBusinessError's same reasoning on the
// collection side.
func isExpectedDepositPayoutBusinessError(err error) bool {
	return errors.Is(err, nativecommon.ErrModulePaused) ||
		errors.Is(err, lending.ErrFixedTermDepositReserveInsufficient) ||
		errors.Is(err, lending.ErrInsufficientLiquidity)
}
