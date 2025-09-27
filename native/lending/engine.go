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

type engineState interface {
	GetMarket() (*Market, error)
	PutMarket(*Market) error
	GetUserAccount(addr crypto.Address) (*UserAccount, error)
	PutUserAccount(*UserAccount) error
	GetAccount(addr crypto.Address) (*types.Account, error)
	PutAccount(addr crypto.Address, account *types.Account) error
}

// Engine orchestrates the primary state transitions for the lending module.
type Engine struct {
	state             engineState
	moduleAddress     crypto.Address
	collateralAddress crypto.Address
	params            RiskParameters
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
	}
	if mintedShares.Sign() == 0 {
		mintedShares = new(big.Int).Set(amount)
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
	return e.state.PutUserAccount(user)
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
	borrowerUser.ScaledDebt = new(big.Int).Set(projectedDebt)

	market.TotalNHBBorrowed = new(big.Int).Add(market.TotalNHBBorrowed, totalOut)

	if err := e.state.PutUserAccount(borrowerUser); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
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

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, err
	}
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

	borrowerUser.DebtNHB = new(big.Int).Sub(borrowerUser.DebtNHB, repayAmount)
	borrowerUser.ScaledDebt = new(big.Int).Set(borrowerUser.DebtNHB)

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}
	market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)

	if err := e.state.PutUserAccount(borrowerUser); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(market); err != nil {
		return nil, err
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
	_ = market // ensure market loaded for consistency although not directly used.

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, nil, err
	}
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
