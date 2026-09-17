package lending

import (
	"math/big"

	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
)

// SupplyFixedTerm originates a new locked-rate, fixed-tenure deposit --
// Milestone 3, the mirror image of BorrowFixedTerm on the pool's liability
// side. depositID must be derived by the caller from the originating
// transaction's own hash, exactly like BorrowFixedTerm's loanID -- never
// from wall-clock time or any other process-local value.
//
// THE CORE SAFETY INVARIANT (see FixedTermDeposit's own doc comment): this
// deposit's locked-in interest promise is only accepted if the pool's
// aggregate fixed-term deposit interest obligation, including this new
// deposit, does not exceed Market.TotalFixedTermLoanInterestReceivableWei
// -- the aggregate interest the pool is itself still owed by active
// fixed-term LOANS. The flexible pool's variable performance is never what
// backs this guarantee; the fixed/fixed spread is.
func (e *Engine) SupplyFixedTerm(depositor crypto.Address, depositID [32]byte, tenureDays uint64, amount *big.Int, payout FixedTermDepositPayout) (*FixedTermDeposit, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if e.params.Pauses.Supply {
		return nil, errSupplyPaused
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	if payout != FixedTermDepositPayoutLumpSumAtMaturity && payout != FixedTermDepositPayoutPeriodic {
		return nil, errFixedTermDepositInvalidPayout
	}
	rateBps, tenureAllowed := e.fixedTermDepositRates[tenureDays]
	if !tenureAllowed || rateBps == 0 {
		return nil, errFixedTermDepositTenureNotAllowed
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	newDepositInterest := computeFixedTermInterest(amount, rateBps, tenureDays)

	depositOwed := market.TotalFixedTermDepositInterestOwedWei
	if depositOwed == nil {
		depositOwed = big.NewInt(0)
	}
	receivable := market.TotalFixedTermLoanInterestReceivableWei
	if receivable == nil {
		receivable = big.NewInt(0)
	}
	projectedOwed := new(big.Int).Add(depositOwed, newDepositInterest)
	if projectedOwed.Cmp(receivable) > 0 {
		return nil, errFixedTermDepositCapacityExceeded
	}

	depositorAcc, err := e.loadAccount(depositor)
	if err != nil {
		return nil, err
	}
	if depositorAcc.BalanceNHB.Cmp(amount) < 0 {
		return nil, errInsufficientBalance
	}
	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}

	depositorAcc.BalanceNHB = new(big.Int).Sub(depositorAcc.BalanceNHB, amount)
	moduleAcc.BalanceNHB = new(big.Int).Add(moduleAcc.BalanceNHB, amount)

	if err := e.persistAccount(depositor, depositorAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}

	issuedAt := uint64(0)
	if e.blockTimestamp > 0 {
		issuedAt = uint64(e.blockTimestamp)
	}
	deposit := &FixedTermDeposit{
		DepositID:            depositID,
		Depositor:            depositor,
		PoolID:               e.poolID,
		TenureDays:           tenureDays,
		RateBps:              rateBps,
		PrincipalWei:         new(big.Int).Set(amount),
		TotalInterestOwedWei: newDepositInterest,
		PaidInterestWei:      big.NewInt(0),
		Payout:               payout,
		IssuedAtBlock:        e.blockHeight,
		IssuedAtTime:         issuedAt,
		MaturityTime:         issuedAt + tenureDays*86400,
		Status:               FixedTermDepositStatusActive,
	}
	if payout == FixedTermDepositPayoutPeriodic {
		deposit.NextPayoutCycle = 1
	}

	principalAgg := market.TotalFixedTermDepositPrincipalWei
	if principalAgg == nil {
		principalAgg = big.NewInt(0)
	}
	market.TotalFixedTermDepositPrincipalWei = new(big.Int).Add(principalAgg, amount)
	market.TotalFixedTermDepositInterestOwedWei = projectedOwed

	if err := e.state.PutFixedTermDeposit(deposit); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(e.poolID, market); err != nil {
		return nil, err
	}

	return deposit, nil
}

// PayFixedTermDepositInterest is the exported entry point
// core/lending_deposit_payout_settlement.go uses to apply one interest
// installment -- see payFixedTermDepositInterest for the mechanics.
// Mutates deposit and market in place; the caller (the settlement hook,
// which already holds both via its own state manager) is responsible for
// persisting them afterward -- mirrors how DecideAutoDebit/DecideDepositPayout
// stay pure while core/*_settlement.go owns all state I/O.
func (e *Engine) PayFixedTermDepositInterest(deposit *FixedTermDeposit, market *Market, installmentWei *big.Int) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	if deposit == nil {
		return errFixedTermDepositNotFound
	}
	if market == nil {
		return errNilMarket
	}
	return e.payFixedTermDepositInterest(deposit, market, installmentWei)
}

// PayFixedTermDepositPrincipal is the exported entry point
// core/lending_deposit_payout_settlement.go uses to return a matured
// deposit's principal -- see payFixedTermDepositPrincipal for the
// mechanics.
func (e *Engine) PayFixedTermDepositPrincipal(deposit *FixedTermDeposit, market *Market) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	if deposit == nil {
		return errFixedTermDepositNotFound
	}
	if market == nil {
		return errNilMarket
	}
	return e.payFixedTermDepositPrincipal(deposit, market)
}

// payFixedTermDepositInterest pays out installmentWei of interest against
// depositID, drawing from Market.FixedTermDepositReserveWei (never from
// general pool liquidity directly -- the reserve is specifically the
// portion of collected fixed-term loan interest earmarked for this
// purpose). Returns errFixedTermDepositReserveInsufficient if the reserve
// cannot currently cover it -- a genuine but expected timing mismatch (loan
// interest hasn't caught up yet), not a storage error; callers must treat
// it as a soft, retryable outcome, never as fatal.
func (e *Engine) payFixedTermDepositInterest(deposit *FixedTermDeposit, market *Market, installmentWei *big.Int) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	if installmentWei == nil || installmentWei.Sign() <= 0 {
		return nil
	}
	reserve := market.FixedTermDepositReserveWei
	if reserve == nil {
		reserve = big.NewInt(0)
	}
	if reserve.Cmp(installmentWei) < 0 {
		return errFixedTermDepositReserveInsufficient
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return err
	}
	if moduleAcc.BalanceNHB.Cmp(installmentWei) < 0 {
		return errFixedTermDepositReserveInsufficient
	}
	depositorAcc, err := e.loadAccount(deposit.Depositor)
	if err != nil {
		return err
	}

	moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, installmentWei)
	depositorAcc.BalanceNHB = new(big.Int).Add(depositorAcc.BalanceNHB, installmentWei)
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return err
	}
	if err := e.persistAccount(deposit.Depositor, depositorAcc); err != nil {
		return err
	}

	market.FixedTermDepositReserveWei = new(big.Int).Sub(reserve, installmentWei)
	depositOwed := market.TotalFixedTermDepositInterestOwedWei
	if depositOwed == nil {
		depositOwed = big.NewInt(0)
	}
	depositOwed = new(big.Int).Sub(depositOwed, installmentWei)
	if depositOwed.Sign() < 0 {
		depositOwed = big.NewInt(0)
	}
	market.TotalFixedTermDepositInterestOwedWei = depositOwed

	deposit.PaidInterestWei = new(big.Int).Add(deposit.PaidInterestWei, installmentWei)
	return nil
}

// payFixedTermDepositPrincipal returns a matured deposit's principal from
// general pool liquidity (not the interest reserve -- principal was never
// part of that earmarked pool) and marks the deposit Matured.
// errInsufficientLiquidity (a soft, retryable outcome, not fatal -- see
// this function's caller) if the pool cannot currently cover it.
func (e *Engine) payFixedTermDepositPrincipal(deposit *FixedTermDeposit, market *Market) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	principal := deposit.PrincipalWei
	if principal == nil {
		principal = big.NewInt(0)
	}
	if principal.Sign() > 0 {
		// Checked against AvailableLiquidity, not just the module's raw
		// balance -- the raw balance also holds FixedTermDepositReserveWei
		// (earmarked for OTHER depositors' interest) and any uncollected
		// ProtocolFeesWei, neither of which this payout may spend. Every
		// other draw on pool cash (Withdraw, Borrow, BorrowFixedTerm) is
		// gated on AvailableLiquidity first, with the raw-balance check
		// only as a defensive backstop; this path used to check only the
		// raw balance, which could silently pay a matured deposit's
		// principal out of cash the ledger has earmarked elsewhere.
		if e.AvailableLiquidity(market).Cmp(principal) < 0 {
			return errInsufficientLiquidity
		}
		moduleAcc, err := e.loadAccount(e.moduleAddress)
		if err != nil {
			return err
		}
		if moduleAcc.BalanceNHB.Cmp(principal) < 0 {
			return errInsufficientLiquidity
		}
		depositorAcc, err := e.loadAccount(deposit.Depositor)
		if err != nil {
			return err
		}
		moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, principal)
		depositorAcc.BalanceNHB = new(big.Int).Add(depositorAcc.BalanceNHB, principal)
		if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
			return err
		}
		if err := e.persistAccount(deposit.Depositor, depositorAcc); err != nil {
			return err
		}

		principalAgg := market.TotalFixedTermDepositPrincipalWei
		if principalAgg == nil {
			principalAgg = big.NewInt(0)
		}
		principalAgg = new(big.Int).Sub(principalAgg, principal)
		if principalAgg.Sign() < 0 {
			principalAgg = big.NewInt(0)
		}
		market.TotalFixedTermDepositPrincipalWei = principalAgg
	}
	deposit.Status = FixedTermDepositStatusMatured
	return nil
}
