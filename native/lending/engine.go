package lending

import (
	"errors"
	"math/big"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

var (
	errNilState              = errors.New("lending engine: state not configured")
	errNilMarket             = errors.New("lending engine: market not initialised")
	errInvalidAmount         = errors.New("lending engine: amount must be positive")
	errInsufficientBalance   = errors.New("lending engine: insufficient balance")
	errInsufficientLiquidity = errors.New("lending engine: insufficient liquidity")
	errHealthCheckFailed     = errors.New("lending engine: borrower health factor below 1")
	errNoDebtToRepay         = errors.New("lending engine: no outstanding debt to repay")
	errNotLiquidatable       = errors.New("lending engine: borrower not eligible for liquidation")
	errDeveloperFeeRecipient = errors.New("lending engine: developer fee recipient not configured")
	errDeveloperFeeCap       = errors.New("lending engine: developer fee exceeds cap")
)

var (
	basisPoints = big.NewInt(10_000)
	ray         = big.NewInt(1_000_000_000_000_000_000)
)

const blocksPerYear = 31_536_000

type engineState interface {
	GetMarket() (*Market, error)
	PutMarket(*Market) error
	GetUserAccount(addr crypto.Address) (*UserAccount, error)
	PutUserAccount(*UserAccount) error
	GetAccount(addr crypto.Address) (*types.Account, error)
	PutAccount(addr crypto.Address, account *types.Account) error
	GetFeeAccrual() (*FeeAccrual, error)
	PutFeeAccrual(*FeeAccrual) error
}

// Engine orchestrates the primary state transitions for the lending module.
type Engine struct {
	state             engineState
	moduleAddress     crypto.Address
	collateralAddress crypto.Address
	params            RiskParameters
	interestModel     *InterestModel
	reserveFactorBps  uint64
	protocolFeeBps    uint64
	blockHeight       uint64
}

// NewEngine constructs a lending engine configured with the module treasury
// addresses and risk parameters.
func NewEngine(moduleAddr, collateralAddr crypto.Address, params RiskParameters) *Engine {
	return &Engine{
		moduleAddress:     moduleAddr,
		collateralAddress: collateralAddr,
		params:            params,
	}
}

// SetState wires the engine to the external persistence layer.
func (e *Engine) SetState(state engineState) { e.state = state }

// SetInterestModel configures the interest rate model used by the engine.
func (e *Engine) SetInterestModel(model *InterestModel) {
	if e == nil {
		return
	}
	if model != nil {
		e.interestModel = model.Clone()
	} else {
		e.interestModel = nil
	}
}

// SetReserveFactor wires the reserve factor basis points used when accruing interest.
func (e *Engine) SetReserveFactor(bps uint64) {
	if e == nil {
		return
	}
	e.reserveFactorBps = bps
}

// SetProtocolFeeBps configures the protocol fee basis points applied during accrual.
func (e *Engine) SetProtocolFeeBps(bps uint64) {
	if e == nil {
		return
	}
	e.protocolFeeBps = bps
}

// SetBlockHeight records the block height used when computing accrual deltas.
func (e *Engine) SetBlockHeight(height uint64) {
	if e == nil {
		return
	}
	e.blockHeight = height
}

// Supply transfers NHB from the supplier into the lending pool and mints LP
// shares based on the current supply index. The minted share amount is returned
// to the caller for downstream accounting.
func (e *Engine) Supply(supplier crypto.Address, amount *big.Int) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return nil, err
	}

	supplierAcc, err := e.loadAccount(supplier)
	if err != nil {
		return nil, err
	}
	if supplierAcc.BalanceNHB.Cmp(amount) < 0 {
		return nil, errInsufficientBalance
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}

	// Compute LP shares using the supply index, defaulting to 1e18 when no
	// liquidity exists yet.
	mintedShares := new(big.Int)
	if market.TotalSupplyShares.Sign() == 0 {
		mintedShares.Set(amount)
	} else {
		mintedShares.Mul(amount, ray)
		mintedShares = mintedShares.Quo(mintedShares, market.SupplyIndex)
		if mintedShares.Sign() == 0 {
			mintedShares = new(big.Int).Set(amount)
		}
	}

	// Adjust balances.
	supplierAcc.BalanceNHB = new(big.Int).Sub(supplierAcc.BalanceNHB, amount)
	moduleAcc.BalanceNHB = new(big.Int).Add(moduleAcc.BalanceNHB, amount)

	if err := e.persistAccount(supplier, supplierAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}

	user, err := e.ensureUserAccount(supplier)
	if err != nil {
		return nil, err
	}
	user.SupplyShares = new(big.Int).Add(user.SupplyShares, mintedShares)

	market.TotalNHBSupplied = new(big.Int).Add(market.TotalNHBSupplied, amount)
	market.TotalSupplyShares = new(big.Int).Add(market.TotalSupplyShares, mintedShares)

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return nil, err
		}
	}

	if err := e.state.PutUserAccount(user); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
	}

	return mintedShares, nil
}

// Withdraw burns LP shares and releases the corresponding NHB amount back to
// the supplier. The redeemed NHB value is returned.
func (e *Engine) Withdraw(supplier crypto.Address, amountLP *big.Int) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amountLP == nil || amountLP.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}
	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return nil, err
	}
	if market.TotalSupplyShares.Sign() == 0 {
		return nil, errInsufficientLiquidity
	}

	user, err := e.ensureUserAccount(supplier)
	if err != nil {
		return nil, err
	}
	if user.SupplyShares.Cmp(amountLP) < 0 {
		return nil, errInsufficientBalance
	}

	// Determine the underlying NHB using the current supply index.
	redeemAmount := new(big.Int).Mul(amountLP, market.SupplyIndex)
	redeemAmount = redeemAmount.Quo(redeemAmount, ray)

	liquidity := e.availableLiquidity(market)
	if liquidity.Cmp(redeemAmount) < 0 {
		return nil, errInsufficientLiquidity
	}

	supplierAcc, err := e.loadAccount(supplier)
	if err != nil {
		return nil, err
	}
	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}
	if moduleAcc.BalanceNHB.Cmp(redeemAmount) < 0 {
		return nil, errInsufficientLiquidity
	}

	moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, redeemAmount)
	supplierAcc.BalanceNHB = new(big.Int).Add(supplierAcc.BalanceNHB, redeemAmount)

	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(supplier, supplierAcc); err != nil {
		return nil, err
	}

	user.SupplyShares = new(big.Int).Sub(user.SupplyShares, amountLP)
	market.TotalSupplyShares = new(big.Int).Sub(market.TotalSupplyShares, amountLP)
	market.TotalNHBSupplied = new(big.Int).Sub(market.TotalNHBSupplied, redeemAmount)

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return nil, err
		}
	}

	if err := e.state.PutUserAccount(user); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
	}

	return redeemAmount, nil
}

// DepositCollateral locks ZNHB collateral for a borrower inside the lending
// module.
func (e *Engine) DepositCollateral(userAddr crypto.Address, amount *big.Int) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return errInvalidAmount
	}

	userAcc, err := e.loadAccount(userAddr)
	if err != nil {
		return err
	}
	if userAcc.BalanceZNHB.Cmp(amount) < 0 {
		return errInsufficientBalance
	}
	moduleAcc, err := e.loadAccount(e.collateralAddress)
	if err != nil {
		return err
	}

	userAcc.BalanceZNHB = new(big.Int).Sub(userAcc.BalanceZNHB, amount)
	moduleAcc.BalanceZNHB = new(big.Int).Add(moduleAcc.BalanceZNHB, amount)

	if err := e.persistAccount(userAddr, userAcc); err != nil {
		return err
	}
	if err := e.persistAccount(e.collateralAddress, moduleAcc); err != nil {
		return err
	}

	user, err := e.ensureUserAccount(userAddr)
	if err != nil {
		return err
	}
	user.CollateralZNHB = new(big.Int).Add(user.CollateralZNHB, amount)

	return e.state.PutUserAccount(user)
}

// WithdrawCollateral releases ZNHB collateral back to the user while ensuring
// the resulting position remains healthy.
func (e *Engine) WithdrawCollateral(userAddr crypto.Address, amount *big.Int) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return errInvalidAmount
	}

	user, err := e.ensureUserAccount(userAddr)
	if err != nil {
		return err
	}
	if user.CollateralZNHB.Cmp(amount) < 0 {
		return errInsufficientBalance
	}

	market, err := e.ensureMarket()
	if err != nil {
		return err
	}

	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return err
	}

	e.syncDebt(user, market)

	remaining := new(big.Int).Sub(user.CollateralZNHB, amount)
	if !e.positionHealthy(remaining, user.DebtNHB) {
		return errHealthCheckFailed
	}

	collateralAcc, err := e.loadAccount(e.collateralAddress)
	if err != nil {
		return err
	}
	if collateralAcc.BalanceZNHB.Cmp(amount) < 0 {
		return errInsufficientLiquidity
	}

	userAcc, err := e.loadAccount(userAddr)
	if err != nil {
		return err
	}

	collateralAcc.BalanceZNHB = new(big.Int).Sub(collateralAcc.BalanceZNHB, amount)
	userAcc.BalanceZNHB = new(big.Int).Add(userAcc.BalanceZNHB, amount)

	if err := e.persistAccount(e.collateralAddress, collateralAcc); err != nil {
		return err
	}
	if err := e.persistAccount(userAddr, userAcc); err != nil {
		return err
	}

	user.CollateralZNHB = remaining

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return err
		}
	}
	if err := e.state.PutUserAccount(user); err != nil {
		return err
	}
	return e.state.PutMarket(market)
}

// Borrow transfers NHB from the module to the borrower while charging a fee to
// the designated recipient. The method returns the fee that was paid.
func (e *Engine) Borrow(borrower crypto.Address, amount *big.Int, feeRecipient crypto.Address, feeBps uint64) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return nil, err
	}

	if feeBps > 0 {
		if len(feeRecipient.Bytes()) == 0 {
			return nil, errDeveloperFeeRecipient
		}
		cap := e.params.DeveloperFeeCapBps
		if cap == 0 || feeBps > cap {
			return nil, errDeveloperFeeCap
		}
	}

	feeAmount := new(big.Int)
	if feeBps > 0 {
		bps := new(big.Int).SetUint64(feeBps)
		feeAmount.Mul(amount, bps)
		feeAmount = feeAmount.Quo(feeAmount, basisPoints)
	}

	totalOut := new(big.Int).Add(amount, feeAmount)

	liquidity := e.availableLiquidity(market)
	if liquidity.Cmp(totalOut) < 0 {
		return nil, errInsufficientLiquidity
	}

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, err
	}

	e.syncDebt(borrowerUser, market)

	// Health factor check using the projected debt after borrowing.
	projectedDebt := new(big.Int).Add(borrowerUser.DebtNHB, totalOut)
	if !e.positionHealthy(borrowerUser.CollateralZNHB, projectedDebt) {
		return nil, errHealthCheckFailed
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}
	if moduleAcc.BalanceNHB.Cmp(totalOut) < 0 {
		return nil, errInsufficientLiquidity
	}

	borrowerAcc, err := e.loadAccount(borrower)
	if err != nil {
		return nil, err
	}
	var feeAcc *types.Account
	if feeAmount.Sign() > 0 {
		feeAcc, err = e.loadAccount(feeRecipient)
		if err != nil {
			return nil, err
		}
	}

	moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, totalOut)
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, amount)
	if feeAcc != nil {
		feeAcc.BalanceNHB = new(big.Int).Add(feeAcc.BalanceNHB, feeAmount)
	}

	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(borrower, borrowerAcc); err != nil {
		return nil, err
	}
	if feeAcc != nil {
		if err := e.persistAccount(feeRecipient, feeAcc); err != nil {
			return nil, err
		}
	}

	borrowerUser.DebtNHB = projectedDebt
	if borrowerUser.ScaledDebt == nil {
		borrowerUser.ScaledDebt = big.NewInt(0)
	}
	scaledIncrement := new(big.Int).Mul(totalOut, ray)
	scaledIncrement = scaledIncrement.Quo(scaledIncrement, market.BorrowIndex)
	if scaledIncrement.Sign() == 0 && totalOut.Sign() > 0 {
		scaledIncrement = big.NewInt(1)
	}
	borrowerUser.ScaledDebt = new(big.Int).Add(borrowerUser.ScaledDebt, scaledIncrement)
	borrowerUser.DebtNHB = new(big.Int).Mul(borrowerUser.ScaledDebt, market.BorrowIndex)
	borrowerUser.DebtNHB = borrowerUser.DebtNHB.Quo(borrowerUser.DebtNHB, ray)

	market.TotalNHBBorrowed = new(big.Int).Add(market.TotalNHBBorrowed, totalOut)

	if feeAmount.Sign() > 0 {
		fees.DeveloperFeesWei = new(big.Int).Add(fees.DeveloperFeesWei, feeAmount)
		feesChanged = true
	}

	if err := e.state.PutUserAccount(borrowerUser); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
	}

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return nil, err
		}
	}

	return feeAmount, nil
}

// Repay transfers NHB from the borrower back to the module and reduces their
// outstanding debt. The actual principal repaid is returned.
func (e *Engine) Repay(borrower crypto.Address, amount *big.Int) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return nil, err
	}

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, err
	}
	e.syncDebt(borrowerUser, market)
	if borrowerUser.DebtNHB.Sign() == 0 {
		return nil, errNoDebtToRepay
	}

	repayAmount := new(big.Int).Set(amount)
	if repayAmount.Cmp(borrowerUser.DebtNHB) > 0 {
		repayAmount = new(big.Int).Set(borrowerUser.DebtNHB)
	}

	borrowerAcc, err := e.loadAccount(borrower)
	if err != nil {
		return nil, err
	}
	if borrowerAcc.BalanceNHB.Cmp(repayAmount) < 0 {
		return nil, errInsufficientBalance
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}

	borrowerAcc.BalanceNHB = new(big.Int).Sub(borrowerAcc.BalanceNHB, repayAmount)
	moduleAcc.BalanceNHB = new(big.Int).Add(moduleAcc.BalanceNHB, repayAmount)

	if err := e.persistAccount(borrower, borrowerAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}

	if borrowerUser.ScaledDebt == nil {
		borrowerUser.ScaledDebt = big.NewInt(0)
	}
	scaledRepay := new(big.Int).Mul(repayAmount, ray)
	scaledRepay = scaledRepay.Quo(scaledRepay, market.BorrowIndex)
	if scaledRepay.Cmp(borrowerUser.ScaledDebt) > 0 {
		scaledRepay = new(big.Int).Set(borrowerUser.ScaledDebt)
	}
	borrowerUser.ScaledDebt = new(big.Int).Sub(borrowerUser.ScaledDebt, scaledRepay)
	borrowerUser.DebtNHB = new(big.Int).Mul(borrowerUser.ScaledDebt, market.BorrowIndex)
	borrowerUser.DebtNHB = borrowerUser.DebtNHB.Quo(borrowerUser.DebtNHB, ray)

	market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)

	if err := e.state.PutUserAccount(borrowerUser); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
	}

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return nil, err
		}
	}

	return repayAmount, nil
}

// Liquidate allows a third party to repay the borrower's debt in exchange for
// a discounted amount of their collateral. The repaid debt and seized
// collateral values are returned.
func (e *Engine) Liquidate(liquidator, borrower crypto.Address) (*big.Int, *big.Int, error) {
	if e == nil || e.state == nil {
		return nil, nil, errNilState
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, nil, err
	}

	fees, feesChanged, err := e.accrueInterest(market)
	if err != nil {
		return nil, nil, err
	}

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, nil, err
	}
	e.syncDebt(borrowerUser, market)
	if borrowerUser.DebtNHB.Sign() == 0 {
		return nil, nil, errNoDebtToRepay
	}
	if e.positionHealthy(borrowerUser.CollateralZNHB, borrowerUser.DebtNHB) {
		return nil, nil, errNotLiquidatable
	}

	repayAmount := new(big.Int).Set(borrowerUser.DebtNHB)

	liquidatorAcc, err := e.loadAccount(liquidator)
	if err != nil {
		return nil, nil, err
	}
	if liquidatorAcc.BalanceNHB.Cmp(repayAmount) < 0 {
		return nil, nil, errInsufficientBalance
	}

	borrowerAcc, err := e.loadAccount(borrower)
	if err != nil {
		return nil, nil, err
	}
	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, nil, err
	}

	// Transfer NHB from liquidator to module to cover the debt.
	liquidatorAcc.BalanceNHB = new(big.Int).Sub(liquidatorAcc.BalanceNHB, repayAmount)
	moduleAcc.BalanceNHB = new(big.Int).Add(moduleAcc.BalanceNHB, repayAmount)

	// Determine collateral seized with liquidation bonus.
	seizeAmount := new(big.Int).Mul(repayAmount, big.NewInt(int64(10_000+e.params.LiquidationBonus)))
	seizeAmount = seizeAmount.Quo(seizeAmount, basisPoints)
	if seizeAmount.Cmp(borrowerUser.CollateralZNHB) > 0 {
		seizeAmount = new(big.Int).Set(borrowerUser.CollateralZNHB)
	}

	collateralAcc, err := e.loadAccount(e.collateralAddress)
	if err != nil {
		return nil, nil, err
	}
	if collateralAcc.BalanceZNHB.Cmp(seizeAmount) < 0 {
		return nil, nil, errInsufficientLiquidity
	}

	collateralAcc.BalanceZNHB = new(big.Int).Sub(collateralAcc.BalanceZNHB, seizeAmount)
	liquidatorAcc.BalanceZNHB = new(big.Int).Add(liquidatorAcc.BalanceZNHB, seizeAmount)

	if err := e.persistAccount(liquidator, liquidatorAcc); err != nil {
		return nil, nil, err
	}
	if err := e.persistAccount(borrower, borrowerAcc); err != nil {
		return nil, nil, err
	}
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, nil, err
	}
	if err := e.persistAccount(e.collateralAddress, collateralAcc); err != nil {
		return nil, nil, err
	}

	borrowerUser.DebtNHB = big.NewInt(0)
	borrowerUser.ScaledDebt = big.NewInt(0)
	borrowerUser.CollateralZNHB = new(big.Int).Sub(borrowerUser.CollateralZNHB, seizeAmount)

	market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)

	if err := e.state.PutUserAccount(borrowerUser); err != nil {
		return nil, nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, nil, err
	}

	if feesChanged {
		if err := e.state.PutFeeAccrual(fees); err != nil {
			return nil, nil, err
		}
	}

	return repayAmount, seizeAmount, nil
}

func (e *Engine) ensureMarket() (*Market, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	market, err := e.state.GetMarket()
	if err != nil {
		return nil, err
	}
	if market == nil {
		return nil, errNilMarket
	}
	if market.TotalNHBSupplied == nil {
		market.TotalNHBSupplied = big.NewInt(0)
	}
	if market.TotalSupplyShares == nil {
		market.TotalSupplyShares = big.NewInt(0)
	}
	if market.TotalNHBBorrowed == nil {
		market.TotalNHBBorrowed = big.NewInt(0)
	}
	if market.SupplyIndex == nil || market.SupplyIndex.Sign() == 0 {
		market.SupplyIndex = new(big.Int).Set(ray)
	}
	if market.BorrowIndex == nil || market.BorrowIndex.Sign() == 0 {
		market.BorrowIndex = new(big.Int).Set(ray)
	}
	return market, nil
}

func (e *Engine) ensureUserAccount(addr crypto.Address) (*UserAccount, error) {
	user, err := e.state.GetUserAccount(addr)
	if err != nil {
		return nil, err
	}
	if user == nil {
		user = &UserAccount{Address: addr}
	}
	if user.CollateralZNHB == nil {
		user.CollateralZNHB = big.NewInt(0)
	}
	if user.SupplyShares == nil {
		user.SupplyShares = big.NewInt(0)
	}
	if user.DebtNHB == nil {
		user.DebtNHB = big.NewInt(0)
	}
	if user.ScaledDebt == nil {
		user.ScaledDebt = big.NewInt(0)
	}
	return user, nil
}

func (e *Engine) loadAccount(addr crypto.Address) (*types.Account, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	acc, err := e.state.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, errInsufficientBalance
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	return acc, nil
}

func (e *Engine) persistAccount(addr crypto.Address, acc *types.Account) error {
	return e.state.PutAccount(addr, acc)
}

func (e *Engine) availableLiquidity(market *Market) *big.Int {
	liquidity := new(big.Int).Sub(market.TotalNHBSupplied, market.TotalNHBBorrowed)
	if liquidity.Sign() < 0 {
		return big.NewInt(0)
	}
	return liquidity
}

func (e *Engine) positionHealthy(collateral, debt *big.Int) bool {
	if debt == nil || debt.Sign() == 0 {
		return true
	}
	if collateral == nil || collateral.Sign() == 0 {
		return false
	}
	num := new(big.Int).Mul(collateral, big.NewInt(int64(e.params.LiquidationThreshold)))
	den := new(big.Int).Mul(debt, basisPoints)
	return num.Cmp(den) >= 0
}

// WithdrawProtocolFees transfers accrued protocol fees to the provided recipient.
func (e *Engine) WithdrawProtocolFees(recipient crypto.Address, amount *big.Int) (*big.Int, error) {
	return e.withdrawFees(recipient, amount, true)
}

// WithdrawDeveloperFees transfers accrued developer fees to the provided recipient.
func (e *Engine) WithdrawDeveloperFees(recipient crypto.Address, amount *big.Int) (*big.Int, error) {
	return e.withdrawFees(recipient, amount, false)
}

func (e *Engine) withdrawFees(recipient crypto.Address, amount *big.Int, protocol bool) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	fees, err := e.ensureFeeAccrual()
	if err != nil {
		return nil, err
	}

	var available *big.Int
	if protocol {
		available = fees.ProtocolFeesWei
	} else {
		available = fees.DeveloperFeesWei
	}
	if available == nil || available.Cmp(amount) < 0 {
		return nil, errInsufficientLiquidity
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}
	if moduleAcc.BalanceNHB.Cmp(amount) < 0 {
		return nil, errInsufficientLiquidity
	}

	recipientAcc, err := e.loadAccount(recipient)
	if err != nil {
		return nil, err
	}

	moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, amount)
	recipientAcc.BalanceNHB = new(big.Int).Add(recipientAcc.BalanceNHB, amount)

	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(recipient, recipientAcc); err != nil {
		return nil, err
	}

	if protocol {
		fees.ProtocolFeesWei = new(big.Int).Sub(fees.ProtocolFeesWei, amount)
	} else {
		fees.DeveloperFeesWei = new(big.Int).Sub(fees.DeveloperFeesWei, amount)
	}

	market.TotalNHBSupplied = new(big.Int).Sub(market.TotalNHBSupplied, amount)

	if err := e.state.PutFeeAccrual(fees); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
	}

	return new(big.Int).Set(amount), nil
}

func (e *Engine) ensureFeeAccrual() (*FeeAccrual, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	fees, err := e.state.GetFeeAccrual()
	if err != nil {
		return nil, err
	}
	if fees == nil {
		fees = &FeeAccrual{}
	}
	if fees.ProtocolFeesWei == nil {
		fees.ProtocolFeesWei = big.NewInt(0)
	}
	if fees.DeveloperFeesWei == nil {
		fees.DeveloperFeesWei = big.NewInt(0)
	}
	return fees, nil
}

func (e *Engine) accrueInterest(market *Market) (*FeeAccrual, bool, error) {
	if e == nil || e.state == nil {
		return nil, false, errNilState
	}
	if market == nil {
		return nil, false, errNilMarket
	}

	if market.SupplyIndex == nil || market.SupplyIndex.Sign() == 0 {
		market.SupplyIndex = new(big.Int).Set(ray)
	}
	if market.BorrowIndex == nil || market.BorrowIndex.Sign() == 0 {
		market.BorrowIndex = new(big.Int).Set(ray)
	}
	market.ReserveFactor = e.reserveFactorBps

	fees, err := e.ensureFeeAccrual()
	if err != nil {
		return nil, false, err
	}

	if e.interestModel == nil {
		if e.blockHeight > market.LastUpdateBlock {
			market.LastUpdateBlock = e.blockHeight
		}
		return fees, false, nil
	}

	var delta uint64
	if e.blockHeight > market.LastUpdateBlock {
		delta = e.blockHeight - market.LastUpdateBlock
	}
	if delta == 0 || market.TotalNHBBorrowed == nil || market.TotalNHBBorrowed.Sign() == 0 {
		if e.blockHeight > market.LastUpdateBlock {
			market.LastUpdateBlock = e.blockHeight
		}
		return fees, false, nil
	}

	borrowAPR := e.interestModel.BorrowAPR(market.TotalNHBBorrowed, market.TotalNHBSupplied)
	if borrowAPR.Sign() == 0 {
		market.LastUpdateBlock = e.blockHeight
		return fees, false, nil
	}

	utilisation := e.interestModel.Utilisation(market.TotalNHBBorrowed, market.TotalNHBSupplied)
	reserveBps := e.reserveFactorBps
	protocolBps := e.protocolFeeBps
	combinedBps := reserveBps + protocolBps
	if combinedBps > 10_000 {
		combinedBps = 10_000
	}
	combined := new(big.Rat).SetFrac(big.NewInt(int64(combinedBps)), big.NewInt(10_000))
	oneMinus := new(big.Rat).Sub(big.NewRat(1, 1), combined)
	if oneMinus.Sign() < 0 {
		oneMinus.SetInt64(0)
	}
	supplyRate := new(big.Rat).Mul(borrowAPR, utilisation)
	supplyRate.Mul(supplyRate, oneMinus)

	borrowFactor := rateFactor(borrowAPR, delta)
	supplyFactor := rateFactor(supplyRate, delta)

	market.BorrowIndex = rayMul(market.BorrowIndex, borrowFactor)
	market.SupplyIndex = rayMul(market.SupplyIndex, supplyFactor)

	interestAmount := computeInterest(market.TotalNHBBorrowed, borrowAPR, delta)
	interestApplied := interestAmount.Sign() > 0
	if interestApplied {
		reserveShare := new(big.Int).Mul(interestAmount, new(big.Int).SetUint64(reserveBps))
		reserveShare = reserveShare.Quo(reserveShare, basisPoints)
		protocolShare := new(big.Int).Mul(interestAmount, new(big.Int).SetUint64(protocolBps))
		protocolShare = protocolShare.Quo(protocolShare, basisPoints)
		if reserveShare.Sign() > 0 {
			fees.ProtocolFeesWei = new(big.Int).Add(fees.ProtocolFeesWei, reserveShare)
		}
		if protocolShare.Sign() > 0 {
			fees.ProtocolFeesWei = new(big.Int).Add(fees.ProtocolFeesWei, protocolShare)
		}
		market.TotalNHBBorrowed = new(big.Int).Add(market.TotalNHBBorrowed, interestAmount)
		market.TotalNHBSupplied = new(big.Int).Add(market.TotalNHBSupplied, interestAmount)
	}

	market.LastUpdateBlock = e.blockHeight
	return fees, interestApplied, nil
}

func rateFactor(rate *big.Rat, delta uint64) *big.Int {
	if rate == nil || rate.Sign() == 0 || delta == 0 {
		return new(big.Int).Set(ray)
	}
	perBlock := new(big.Rat).Set(rate)
	perBlock.Quo(perBlock, new(big.Rat).SetUint64(blocksPerYear))
	perBlock.Mul(perBlock, new(big.Rat).SetUint64(delta))
	factor := new(big.Rat).Add(big.NewRat(1, 1), perBlock)
	return ratToRay(factor)
}

func rayMul(a, b *big.Int) *big.Int {
	if a == nil || b == nil {
		return big.NewInt(0)
	}
	result := new(big.Int).Mul(a, b)
	result = result.Quo(result, ray)
	return result
}

func ratToRay(r *big.Rat) *big.Int {
	if r == nil {
		return new(big.Int).Set(ray)
	}
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(ray))
	out := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if out.Sign() == 0 {
		return new(big.Int).Set(ray)
	}
	return out
}

func computeInterest(totalBorrowed *big.Int, rate *big.Rat, delta uint64) *big.Int {
	if totalBorrowed == nil || totalBorrowed.Sign() == 0 || rate == nil || rate.Sign() == 0 || delta == 0 {
		return big.NewInt(0)
	}
	perBlock := new(big.Rat).Set(rate)
	perBlock.Quo(perBlock, new(big.Rat).SetUint64(blocksPerYear))
	perBlock.Mul(perBlock, new(big.Rat).SetUint64(delta))
	interest := new(big.Rat).Mul(perBlock, new(big.Rat).SetInt(totalBorrowed))
	return new(big.Int).Quo(interest.Num(), interest.Denom())
}

func (e *Engine) syncDebt(user *UserAccount, market *Market) {
	if user == nil || market == nil {
		return
	}
	if user.ScaledDebt == nil || user.ScaledDebt.Sign() == 0 {
		user.DebtNHB = big.NewInt(0)
		return
	}
	if market.BorrowIndex == nil || market.BorrowIndex.Sign() == 0 {
		user.DebtNHB = big.NewInt(0)
		return
	}
	actual := new(big.Int).Mul(user.ScaledDebt, market.BorrowIndex)
	actual = actual.Quo(actual, ray)
	user.DebtNHB = actual
}
