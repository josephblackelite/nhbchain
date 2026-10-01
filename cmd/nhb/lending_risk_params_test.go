package main

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// fakeLendingState is a minimal in-memory implementation of the lending
// engine's unexported engineState interface (native/lending/engine.go),
// satisfied structurally -- Go interface satisfaction does not require the
// interface type itself to be exported. Mirrors native/lending's own
// mockEngineState test fixture (engine_accrual_test.go), redeclared here
// because that type is unexported and lives in a different package.
type fakeLendingState struct {
	market        *lending.Market
	users         map[string]*lending.UserAccount
	accounts      map[string]*types.Account
	fees          *lending.FeeAccrual
	loans         map[[32]byte]*lending.FixedTermLoan
	activeLoanIDs map[string][32]byte
	deposits      map[[32]byte]*lending.FixedTermDeposit
}

func newFakeLendingState() *fakeLendingState {
	return &fakeLendingState{
		users:         make(map[string]*lending.UserAccount),
		accounts:      make(map[string]*types.Account),
		loans:         make(map[[32]byte]*lending.FixedTermLoan),
		activeLoanIDs: make(map[string][32]byte),
		deposits:      make(map[[32]byte]*lending.FixedTermDeposit),
	}
}

func (f *fakeLendingState) key(addr crypto.Address) string { return string(addr.Bytes()) }

func (f *fakeLendingState) GetMarket(string) (*lending.Market, error) { return f.market, nil }

func (f *fakeLendingState) PutMarket(_ string, market *lending.Market) error {
	f.market = market
	return nil
}

func (f *fakeLendingState) GetUserAccount(_ string, addr crypto.Address) (*lending.UserAccount, error) {
	if acc, ok := f.users[f.key(addr)]; ok {
		return acc, nil
	}
	return nil, nil
}

func (f *fakeLendingState) PutUserAccount(_ string, account *lending.UserAccount) error {
	if account == nil {
		return nil
	}
	f.users[f.key(account.Address)] = account
	return nil
}

// GetAccount deliberately returns a zero-balance account rather than an
// error for an address that was never seeded, matching the real
// nhbstate.Manager.GetAccount's "always returns a usable default" behaviour
// (core/state/accounts.go) that the lending engine's loadAccount relies on
// in production.
func (f *fakeLendingState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if acc, ok := f.accounts[f.key(addr)]; ok {
		if acc.BalanceNHB == nil {
			acc.BalanceNHB = big.NewInt(0)
		}
		if acc.BalanceZNHB == nil {
			acc.BalanceZNHB = big.NewInt(0)
		}
		return acc, nil
	}
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}, nil
}

func (f *fakeLendingState) PutAccount(addr crypto.Address, account *types.Account) error {
	f.accounts[f.key(addr)] = account
	return nil
}

func (f *fakeLendingState) GetFeeAccrual(string) (*lending.FeeAccrual, error) { return f.fees, nil }

func (f *fakeLendingState) PutFeeAccrual(_ string, fees *lending.FeeAccrual) error {
	f.fees = fees
	return nil
}

func (f *fakeLendingState) GetFixedTermLoan(loanID [32]byte) (*lending.FixedTermLoan, error) {
	return f.loans[loanID], nil
}

func (f *fakeLendingState) PutFixedTermLoan(loan *lending.FixedTermLoan) error {
	if loan == nil {
		return nil
	}
	f.loans[loan.LoanID] = loan
	return nil
}

func (f *fakeLendingState) GetFixedTermDeposit(depositID [32]byte) (*lending.FixedTermDeposit, error) {
	return f.deposits[depositID], nil
}

func (f *fakeLendingState) PutFixedTermDeposit(deposit *lending.FixedTermDeposit) error {
	if deposit == nil {
		return nil
	}
	f.deposits[deposit.DepositID] = deposit
	return nil
}

func (f *fakeLendingState) GetActiveFixedTermLoanID(_ string, addr crypto.Address) ([32]byte, bool, error) {
	id, ok := f.activeLoanIDs[f.key(addr)]
	return id, ok, nil
}

func (f *fakeLendingState) SetActiveFixedTermLoanID(_ string, addr crypto.Address, loanID [32]byte) error {
	f.activeLoanIDs[f.key(addr)] = loanID
	return nil
}

func (f *fakeLendingState) ClearActiveFixedTermLoan(_ string, addr crypto.Address) error {
	delete(f.activeLoanIDs, f.key(addr))
	return nil
}

func lendingTestAddress(prefix crypto.AddressPrefix, suffix byte) crypto.Address {
	raw := make([]byte, 20)
	raw[len(raw)-1] = suffix
	return crypto.MustNewAddress(prefix, raw)
}

// TestLendingRiskParametersFromConfig_MapsAllFields proves the config ->
// RiskParameters conversion itself (not just that the TOML/JSON config
// struct parses) carries every field through, including the four
// (BorrowCaps, Pauses, CircuitBreakerActive, LiquidationBonus) that were
// previously silently dropped at the cmd/nhb/main.go call site.
func TestLendingRiskParametersFromConfig_MapsAllFields(t *testing.T) {
	cfg := lending.Config{
		MaxLTVBps:               7500,
		LiquidationThresholdBps: 8500,
		LiquidationBonusBps:     1000,
		CircuitBreakerActive:    true,
		DeveloperFeeBps:         250,
		BorrowCaps: lending.BorrowCaps{
			PerBlock:       big.NewInt(1_000_000),
			Total:          big.NewInt(50_000_000),
			UtilisationBps: 9000,
		},
		Pauses: lending.ActionPauses{
			Supply:    true,
			Borrow:    true,
			Repay:     false,
			Liquidate: true,
		},
		OracleMaxAgeBlocks:    100,
		OracleMaxDeviationBps: 500,
	}

	params := lendingRiskParametersFromConfig(cfg)

	if params.MaxLTV != cfg.MaxLTVBps {
		t.Fatalf("MaxLTV not wired: got %d want %d", params.MaxLTV, cfg.MaxLTVBps)
	}
	if params.LiquidationThreshold != cfg.LiquidationThresholdBps {
		t.Fatalf("LiquidationThreshold not wired: got %d want %d", params.LiquidationThreshold, cfg.LiquidationThresholdBps)
	}
	if params.LiquidationBonus != cfg.LiquidationBonusBps {
		t.Fatalf("LiquidationBonus not wired: got %d want %d", params.LiquidationBonus, cfg.LiquidationBonusBps)
	}
	if params.CircuitBreakerActive != cfg.CircuitBreakerActive {
		t.Fatalf("CircuitBreakerActive not wired: got %v want %v", params.CircuitBreakerActive, cfg.CircuitBreakerActive)
	}
	if params.DeveloperFeeCapBps != cfg.DeveloperFeeBps {
		t.Fatalf("DeveloperFeeCapBps not wired: got %d want %d", params.DeveloperFeeCapBps, cfg.DeveloperFeeBps)
	}
	if params.BorrowCaps.PerBlock == nil || params.BorrowCaps.PerBlock.Cmp(cfg.BorrowCaps.PerBlock) != 0 {
		t.Fatalf("BorrowCaps.PerBlock not wired: got %v want %v", params.BorrowCaps.PerBlock, cfg.BorrowCaps.PerBlock)
	}
	if params.BorrowCaps.Total == nil || params.BorrowCaps.Total.Cmp(cfg.BorrowCaps.Total) != 0 {
		t.Fatalf("BorrowCaps.Total not wired: got %v want %v", params.BorrowCaps.Total, cfg.BorrowCaps.Total)
	}
	if params.BorrowCaps.UtilisationBps != cfg.BorrowCaps.UtilisationBps {
		t.Fatalf("BorrowCaps.UtilisationBps not wired: got %d want %d", params.BorrowCaps.UtilisationBps, cfg.BorrowCaps.UtilisationBps)
	}
	if params.Pauses != cfg.Pauses {
		t.Fatalf("Pauses not wired: got %+v want %+v", params.Pauses, cfg.Pauses)
	}
	if params.Oracle.MaxAgeBlocks != cfg.OracleMaxAgeBlocks || params.Oracle.MaxDeviationBps != cfg.OracleMaxDeviationBps {
		t.Fatalf("Oracle bounds not wired: got %+v want age=%d dev=%d", params.Oracle, cfg.OracleMaxAgeBlocks, cfg.OracleMaxDeviationBps)
	}
}

// TestLendingRiskParametersFromConfig_PauseReachesEngine proves a configured
// Pauses.Supply value does not just parse, but actually reaches the running
// Engine's enforcement: Engine.Supply checks e.params.Pauses.Supply (see
// native/lending/engine.go) before touching any state at all, so a paused
// config must fail immediately while an unpaused one must get past that
// specific gate (even though it then fails for an unrelated reason, since
// this fake state has no market configured).
func TestLendingRiskParametersFromConfig_PauseReachesEngine(t *testing.T) {
	moduleAddr := lendingTestAddress(crypto.NHBPrefix, 0x01)
	collateralAddr := lendingTestAddress(crypto.ZNHBPrefix, 0x02)
	supplier := lendingTestAddress(crypto.NHBPrefix, 0x03)

	pausedCfg := lending.Config{Pauses: lending.ActionPauses{Supply: true}}
	pausedEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(pausedCfg))
	pausedEngine.SetState(newFakeLendingState())
	pausedEngine.SetPoolID("default")
	if _, err := pausedEngine.Supply(supplier, big.NewInt(1)); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected a configured Pauses.Supply to pause Engine.Supply, got %v", err)
	}

	unpausedCfg := lending.Config{}
	unpausedEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(unpausedCfg))
	unpausedEngine.SetState(newFakeLendingState())
	unpausedEngine.SetPoolID("default")
	err := func() error { _, err := unpausedEngine.Supply(supplier, big.NewInt(1)); return err }()
	if err == nil {
		t.Fatalf("expected the unpaused engine to still fail past the pause gate (no market configured), got nil")
	}
	if strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected an unconfigured Pauses.Supply to NOT pause Engine.Supply, got %v", err)
	}
}

// TestLendingRiskParametersFromConfig_CircuitBreakerReachesEngine is
// TestLendingRiskParametersFromConfig_PauseReachesEngine's counterpart for
// CircuitBreakerActive, gated on Engine.Borrow.
func TestLendingRiskParametersFromConfig_CircuitBreakerReachesEngine(t *testing.T) {
	moduleAddr := lendingTestAddress(crypto.NHBPrefix, 0x04)
	collateralAddr := lendingTestAddress(crypto.ZNHBPrefix, 0x05)
	borrower := lendingTestAddress(crypto.NHBPrefix, 0x06)

	trippedCfg := lending.Config{CircuitBreakerActive: true}
	trippedEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(trippedCfg))
	trippedEngine.SetState(newFakeLendingState())
	trippedEngine.SetPoolID("default")
	if _, err := trippedEngine.Borrow(borrower, big.NewInt(1), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected a configured CircuitBreakerActive to block Engine.Borrow, got %v", err)
	}

	clearCfg := lending.Config{}
	clearEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(clearCfg))
	clearEngine.SetState(newFakeLendingState())
	clearEngine.SetPoolID("default")
	_, err := clearEngine.Borrow(borrower, big.NewInt(1), crypto.Address{}, 0)
	if err == nil {
		t.Fatalf("expected the breaker-clear engine to still fail past the gate (no market configured), got nil")
	}
	if strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected an unconfigured CircuitBreakerActive to NOT block Engine.Borrow, got %v", err)
	}
}

// TestLendingRiskParametersFromConfig_BorrowCapReachesEngine proves a
// configured BorrowCaps.Total value reaches Engine.Borrow's enforcement,
// using two engines over otherwise-identical market state.
func TestLendingRiskParametersFromConfig_BorrowCapReachesEngine(t *testing.T) {
	moduleAddr := lendingTestAddress(crypto.NHBPrefix, 0x07)
	collateralAddr := lendingTestAddress(crypto.ZNHBPrefix, 0x08)
	borrower := lendingTestAddress(crypto.NHBPrefix, 0x09)

	baseMarket := func() *lending.Market {
		return &lending.Market{
			PoolID:           "default",
			TotalNHBSupplied: big.NewInt(1_000),
			TotalNHBBorrowed: big.NewInt(50),
		}
	}

	cappedCfg := lending.Config{BorrowCaps: lending.BorrowCaps{Total: big.NewInt(100)}}
	cappedState := newFakeLendingState()
	cappedState.market = baseMarket()
	cappedEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(cappedCfg))
	cappedEngine.SetState(cappedState)
	cappedEngine.SetPoolID("default")
	if _, err := cappedEngine.Borrow(borrower, big.NewInt(60), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "global cap") {
		t.Fatalf("expected a configured BorrowCaps.Total to block an over-cap borrow, got %v", err)
	}

	uncappedCfg := lending.Config{}
	uncappedState := newFakeLendingState()
	uncappedState.market = baseMarket()
	uncappedEngine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(uncappedCfg))
	uncappedEngine.SetState(uncappedState)
	uncappedEngine.SetPoolID("default")
	_, err := uncappedEngine.Borrow(borrower, big.NewInt(60), crypto.Address{}, 0)
	if err == nil {
		t.Fatalf("expected the uncapped borrow to still fail for an unrelated reason (uncollateralized), got nil")
	}
	if strings.Contains(err.Error(), "global cap") {
		t.Fatalf("expected an unconfigured BorrowCaps.Total to NOT block the borrow, got %v", err)
	}
}

// TestLendingRiskParametersFromConfig_LiquidationBonusReachesEngine proves a
// configured LiquidationBonusBps value changes the collateral amount
// Engine.Liquidate actually seizes, over otherwise-identical borrower state.
func TestLendingRiskParametersFromConfig_LiquidationBonusReachesEngine(t *testing.T) {
	moduleAddr := lendingTestAddress(crypto.NHBPrefix, 0x0A)
	collateralAddr := lendingTestAddress(crypto.ZNHBPrefix, 0x0B)
	liquidator := lendingTestAddress(crypto.NHBPrefix, 0x0C)
	borrower := lendingTestAddress(crypto.NHBPrefix, 0x0D)

	runLiquidation := func(t *testing.T, bonusBps uint64) *big.Int {
		t.Helper()
		cfg := lending.Config{LiquidationThresholdBps: 7000, LiquidationBonusBps: bonusBps}
		state := newFakeLendingState()
		state.market = &lending.Market{
			PoolID:           "default",
			TotalNHBSupplied: big.NewInt(10_000),
			TotalNHBBorrowed: big.NewInt(800),
		}
		state.accounts[state.key(liquidator)] = &types.Account{BalanceNHB: big.NewInt(5_000), BalanceZNHB: big.NewInt(0)}
		state.accounts[state.key(borrower)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		state.accounts[state.key(collateralAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(10_000)}
		state.users[state.key(borrower)] = &lending.UserAccount{
			Address:        borrower,
			CollateralZNHB: big.NewInt(1_000),
			DebtNHB:        big.NewInt(800),
			ScaledDebt:     big.NewInt(800),
		}

		engine := lending.NewEngine(moduleAddr, collateralAddr, lendingRiskParametersFromConfig(cfg))
		engine.SetState(state)
		engine.SetPoolID("default")

		_, seized, err := engine.Liquidate(liquidator, borrower)
		if err != nil {
			t.Fatalf("liquidate (bonus=%d bps): %v", bonusBps, err)
		}
		return seized
	}

	noBonus := runLiquidation(t, 0)
	withBonus := runLiquidation(t, 1000)

	if noBonus.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("expected a zero LiquidationBonus to seize exactly the repaid debt (800), got %s", noBonus)
	}
	if withBonus.Cmp(big.NewInt(880)) != 0 {
		t.Fatalf("expected a configured 1000bps LiquidationBonus to seize 880, got %s", withBonus)
	}
}
