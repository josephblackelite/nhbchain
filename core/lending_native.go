package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

const defaultLendingPoolID = "default"

var lendingLegacyRay = mustLegacyBigInt("1000000000000000000000000000")

type lendingNativePayload struct {
	PoolID          string `json:"poolId,omitempty"`
	UseDeveloperFee bool   `json:"useDeveloperFee,omitempty"`
}

func cloneLendingRiskParameters(params lending.RiskParameters) lending.RiskParameters {
	clone := lending.RiskParameters{
		MaxLTV:               params.MaxLTV,
		LiquidationThreshold: params.LiquidationThreshold,
		LiquidationBonus:     params.LiquidationBonus,
		CircuitBreakerActive: params.CircuitBreakerActive,
		DeveloperFeeCapBps:   params.DeveloperFeeCapBps,
		BorrowCaps:           params.BorrowCaps.Clone(),
		Oracle:               params.Oracle,
		Pauses:               params.Pauses,
	}
	if params.OracleAddress.Bytes() != nil {
		clone.OracleAddress = cloneAddress(params.OracleAddress)
	}
	return clone
}

func cloneLendingInterestModel(model *lending.InterestModel) *lending.InterestModel {
	if model == nil {
		return nil
	}
	return model.Clone()
}

func (sp *StateProcessor) SetLendingAddresses(moduleAddr, collateralAddr crypto.Address) {
	if sp == nil {
		return
	}
	if moduleAddr.Bytes() != nil {
		sp.lendingModuleAddr = cloneAddress(moduleAddr)
	}
	if collateralAddr.Bytes() != nil {
		sp.lendingCollateralAddr = cloneAddress(collateralAddr)
	}
}

func (sp *StateProcessor) SetLendingRiskParameters(params lending.RiskParameters) {
	if sp == nil {
		return
	}
	sp.lendingParams = cloneLendingRiskParameters(params)
}

func (sp *StateProcessor) SetLendingAccrualConfig(reserveBps, protocolFeeBps uint64, model *lending.InterestModel) {
	if sp == nil {
		return
	}
	sp.lendingReserveFactorBps = reserveBps
	sp.lendingProtocolFeeBps = protocolFeeBps
	sp.lendingInterestModel = cloneLendingInterestModel(model)
}

func (sp *StateProcessor) SetLendingDeveloperFee(bps uint64, collector crypto.Address) {
	if sp == nil {
		return
	}
	sp.lendingDeveloperFeeBps = bps
	sp.lendingDeveloperCollector = cloneAddress(collector)
}

func (sp *StateProcessor) SetLendingCollateralRouting(routing lending.CollateralRouting) {
	if sp == nil {
		return
	}
	sp.lendingCollateralRouting = routing.Clone()
}

func (sp *StateProcessor) decodeLendingPayload(data []byte) (*lendingNativePayload, error) {
	payload := &lendingNativePayload{PoolID: defaultLendingPoolID}
	if len(data) == 0 {
		return payload, nil
	}
	if err := json.Unmarshal(data, payload); err != nil {
		return nil, fmt.Errorf("invalid lending payload: %w", err)
	}
	payload.PoolID = strings.TrimSpace(payload.PoolID)
	if payload.PoolID == "" {
		payload.PoolID = defaultLendingPoolID
	}
	return payload, nil
}

func (sp *StateProcessor) lendingStateAdapter(poolID string) *lendingStateAdapter {
	return &lendingStateAdapter{
		manager:   nhbstate.NewManager(sp.Trie),
		poolID:    normalizeLendingPoolID(poolID),
		processor: sp,
	}
}

func normalizeLendingPoolID(poolID string) string {
	trimmed := strings.TrimSpace(poolID)
	if trimmed == "" {
		return defaultLendingPoolID
	}
	return trimmed
}

func mustLegacyBigInt(value string) *big.Int {
	out, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	if !ok {
		panic("invalid lending legacy integer constant")
	}
	return out
}

func (sp *StateProcessor) ensureLendingMarket(adapter *lendingStateAdapter) (*lending.Market, error) {
	if sp == nil || adapter == nil {
		return nil, fmt.Errorf("lending: state unavailable")
	}
	market, err := adapter.GetMarket(adapter.poolID)
	if err != nil {
		return nil, err
	}
	if market != nil {
		return market, nil
	}
	market = &lending.Market{
		PoolID:                adapter.poolID,
		DeveloperOwner:        cloneAddress(sp.lendingModuleAddr),
		DeveloperFeeCollector: cloneAddress(sp.lendingDeveloperCollector),
		DeveloperFeeBps:       sp.lendingDeveloperFeeBps,
		ReserveFactor:         sp.lendingReserveFactorBps,
		LastUpdateBlock:       sp.blockHeight(),
		TotalNHBSupplied:      big.NewInt(0),
		TotalSupplyShares:     big.NewInt(0),
		TotalNHBBorrowed:      big.NewInt(0),
	}
	if err := adapter.PutMarket(adapter.poolID, market); err != nil {
		return nil, err
	}
	return market, nil
}

func (sp *StateProcessor) lendingEngine(poolID string) (*lending.Engine, *lending.Market, error) {
	if sp == nil || sp.Trie == nil {
		return nil, nil, fmt.Errorf("lending: state unavailable")
	}
	adapter := sp.lendingStateAdapter(poolID)
	if err := adapter.reconcileLegacyPoolState(); err != nil {
		return nil, nil, err
	}
	market, err := sp.ensureLendingMarket(adapter)
	if err != nil {
		return nil, nil, err
	}
	engine := lending.NewEngine(cloneAddress(sp.lendingModuleAddr), cloneAddress(sp.lendingCollateralAddr), cloneLendingRiskParameters(sp.lendingParams))
	engine.SetPauses(sp.pauses)
	engine.SetState(adapter)
	engine.SetPoolID(adapter.poolID)
	engine.SetInterestModel(cloneLendingInterestModel(sp.lendingInterestModel))
	engine.SetReserveFactor(sp.lendingReserveFactorBps)
	engine.SetProtocolFeeBps(sp.lendingProtocolFeeBps)
	engine.SetBlockHeight(sp.blockHeight())
	engine.SetBlockTimestamp(sp.blockTimestamp().Unix())
	engine.SetCollateralRouting(sp.lendingCollateralRouting.Clone())
	if market != nil {
		engine.SetDeveloperFee(market.DeveloperFeeBps, market.DeveloperFeeCollector)
	} else {
		engine.SetDeveloperFee(sp.lendingDeveloperFeeBps, cloneAddress(sp.lendingDeveloperCollector))
	}
	return engine, market, nil
}

func (sp *StateProcessor) applyLendingSupplyNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending supply amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if _, err := engine.Supply(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingWithdrawNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending withdraw amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if _, err := engine.Withdraw(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingDepositZNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending collateral amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if err := engine.DepositCollateral(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingWithdrawZNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending collateral withdrawal amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if err := engine.WithdrawCollateral(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingBorrowNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending borrow amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, market, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	feeRecipient := crypto.Address{}
	feeBps := uint64(0)
	if payload.UseDeveloperFee {
		if market == nil || market.DeveloperFeeBps == 0 || len(market.DeveloperFeeCollector.Bytes()) == 0 {
			return fmt.Errorf("lending engine: developer fee disabled")
		}
		feeRecipient = market.DeveloperFeeCollector
		feeBps = market.DeveloperFeeBps
	}
	if _, err := engine.Borrow(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value, feeRecipient, feeBps); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingRepayNHB(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending repay amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if _, err := engine.Repay(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

type lendingFixedTermBorrowPayload struct {
	PoolID     string `json:"poolId,omitempty"`
	TenureDays uint64 `json:"tenureDays"`
}

func (sp *StateProcessor) decodeLendingFixedTermBorrowPayload(data []byte) (*lendingFixedTermBorrowPayload, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("lending fixed-term borrow payload required")
	}
	payload := &lendingFixedTermBorrowPayload{PoolID: defaultLendingPoolID}
	if err := json.Unmarshal(data, payload); err != nil {
		return nil, fmt.Errorf("invalid lending fixed-term borrow payload: %w", err)
	}
	payload.PoolID = strings.TrimSpace(payload.PoolID)
	if payload.PoolID == "" {
		payload.PoolID = defaultLendingPoolID
	}
	if payload.TenureDays == 0 {
		return nil, fmt.Errorf("lending fixed-term borrow payload requires a tenure")
	}
	return payload, nil
}

// applyLendingBorrowFixedTerm originates a new locked-rate, fixed-tenure
// loan. loanID is derived from this transaction's own hash -- a pure
// function of the transaction's own immutable bytes, computed identically
// by every validator that processes it, never from wall-clock time (see
// native/lending's FixedTermLoan doc comment for the incident class this
// avoids).
func (sp *StateProcessor) applyLendingBorrowFixedTerm(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending fixed-term borrow amount must be positive")
	}
	payload, err := sp.decodeLendingFixedTermBorrowPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	// Resolved here, not inside the shared lendingEngine() constructor, so a
	// corrupted/tampered stored schedule value can only ever fail fixed-term
	// borrow origination -- not every other lending tx type (supply/withdraw/
	// collateral/repay/liquidate), which never consult it. Mirrors
	// core/swap_risk_params.go's effectiveRedeemRiskParameters precedent of
	// being read only from the specific code path that needs it.
	rateSchedule, err := sp.effectiveFixedTermRateSchedule(nhbstate.NewManager(sp.Trie))
	if err != nil {
		return err
	}
	engine.SetFixedTermRateSchedule(rateSchedule)
	txHash, err := tx.Hash()
	if err != nil {
		return fmt.Errorf("lending fixed-term borrow: compute tx hash: %w", err)
	}
	var loanID [32]byte
	copy(loanID[:], txHash)
	loan, err := engine.BorrowFixedTerm(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), loanID, payload.TenureDays, tx.Value)
	if err != nil {
		return err
	}
	// Schedules the loan's first auto-debit installment (cycle 1) --
	// settleLendingAutoDebits (core/lending_autodebit_settlement.go) picks
	// it up from this due-index bucket once its day arrives. Scheduling
	// here rather than inside the engine itself keeps native/lending free
	// of core/state's due-index storage concern, matching how the engine
	// already has no knowledge of block-lifecycle scheduling anywhere else.
	firstCycleDue := lending.TotalAutoDebitCycles(loan.TenureDays)
	if firstCycleDue > 0 {
		dueAt := loan.IssuedAtTime + lending.AutoDebitCycleLengthDays*86400
		if dueAt > loan.MaturityTime {
			dueAt = loan.MaturityTime
		}
		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.LendingAutoDebitAppendDue(dueAt/secondsPerDay, loanID); err != nil {
			return fmt.Errorf("lending fixed-term borrow: schedule auto-debit: %w", err)
		}
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyLendingRepayFixedTerm(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending fixed-term repay amount must be positive")
	}
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	if _, err := engine.RepayFixedTerm(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), tx.Value); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

type lendingFixedTermSupplyPayload struct {
	PoolID     string `json:"poolId,omitempty"`
	TenureDays uint64 `json:"tenureDays"`
	Payout     string `json:"payout"`
}

func (sp *StateProcessor) decodeLendingFixedTermSupplyPayload(data []byte) (*lendingFixedTermSupplyPayload, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("lending fixed-term supply payload required")
	}
	payload := &lendingFixedTermSupplyPayload{PoolID: defaultLendingPoolID}
	if err := json.Unmarshal(data, payload); err != nil {
		return nil, fmt.Errorf("invalid lending fixed-term supply payload: %w", err)
	}
	payload.PoolID = strings.TrimSpace(payload.PoolID)
	if payload.PoolID == "" {
		payload.PoolID = defaultLendingPoolID
	}
	if payload.TenureDays == 0 {
		return nil, fmt.Errorf("lending fixed-term supply payload requires a tenure")
	}
	payload.Payout = strings.TrimSpace(payload.Payout)
	switch lending.FixedTermDepositPayout(payload.Payout) {
	case lending.FixedTermDepositPayoutLumpSumAtMaturity, lending.FixedTermDepositPayoutPeriodic:
	default:
		return nil, fmt.Errorf("lending fixed-term supply payload has an invalid payout preference %q", payload.Payout)
	}
	return payload, nil
}

// applyLendingSupplyFixedTerm originates a new locked-rate, fixed-tenure
// deposit -- Milestone 3, the mirror image of applyLendingBorrowFixedTerm on
// the pool's liability side. depositID is derived from this transaction's
// own hash, exactly like applyLendingBorrowFixedTerm's loanID -- never from
// wall-clock time.
func (sp *StateProcessor) applyLendingSupplyFixedTerm(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("lending fixed-term supply amount must be positive")
	}
	payload, err := sp.decodeLendingFixedTermSupplyPayload(tx.Data)
	if err != nil {
		return err
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	// Resolved here, not inside the shared lendingEngine() constructor --
	// same reasoning as applyLendingBorrowFixedTerm's own rate-schedule
	// resolution: a corrupted/tampered stored schedule value can only ever
	// fail fixed-term deposit origination, not every other lending tx type.
	depositRateSchedule, err := sp.effectiveFixedTermDepositRateSchedule(nhbstate.NewManager(sp.Trie))
	if err != nil {
		return err
	}
	engine.SetFixedTermDepositRateSchedule(depositRateSchedule)
	txHash, err := tx.Hash()
	if err != nil {
		return fmt.Errorf("lending fixed-term supply: compute tx hash: %w", err)
	}
	var depositID [32]byte
	copy(depositID[:], txHash)
	deposit, err := engine.SupplyFixedTerm(crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)), depositID, payload.TenureDays, tx.Value, lending.FixedTermDepositPayout(payload.Payout))
	if err != nil {
		return err
	}
	// Schedules the deposit's first payout step -- settleLendingDepositPayouts
	// (core/lending_deposit_payout_settlement.go) picks it up from this
	// due-index bucket once its day arrives. Lump-sum: a single step at
	// maturity. Periodic: cycle 1's own due date.
	manager := nhbstate.NewManager(sp.Trie)
	var dueAt uint64
	if deposit.Payout == lending.FixedTermDepositPayoutLumpSumAtMaturity {
		dueAt = deposit.MaturityTime
	} else {
		dueAt = deposit.IssuedAtTime + lending.AutoDebitCycleLengthDays*86400
		if dueAt > deposit.MaturityTime {
			dueAt = deposit.MaturityTime
		}
	}
	if err := manager.LendingDepositPayoutAppendDue(dueAt/secondsPerDay, depositID); err != nil {
		return fmt.Errorf("lending fixed-term supply: schedule payout: %w", err)
	}
	return sp.incrementNativeAccountNonce(sender)
}

type lendingLiquidatePayload struct {
	PoolID   string `json:"poolId,omitempty"`
	Borrower string `json:"borrower"`
}

func (sp *StateProcessor) decodeLendingLiquidatePayload(data []byte) (*lendingLiquidatePayload, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("lending liquidate payload required")
	}
	payload := &lendingLiquidatePayload{PoolID: defaultLendingPoolID}
	if err := json.Unmarshal(data, payload); err != nil {
		return nil, fmt.Errorf("invalid lending liquidate payload: %w", err)
	}
	payload.PoolID = strings.TrimSpace(payload.PoolID)
	if payload.PoolID == "" {
		payload.PoolID = defaultLendingPoolID
	}
	payload.Borrower = strings.TrimSpace(payload.Borrower)
	if payload.Borrower == "" {
		return nil, fmt.Errorf("lending liquidate payload requires a borrower address")
	}
	return payload, nil
}

// applyLendingLiquidate lets any third party ("the liquidator") repay an
// unhealthy borrower's debt in exchange for a discounted share of their
// collateral. Unlike the other lending handlers, the acted-upon address
// (the borrower) is not the transaction sender -- liquidation is inherently
// a permissionless action against someone else's unhealthy position, so the
// borrower's own signature is neither required nor meaningful here. The
// liquidator's signature only authorizes spending the liquidator's own NHB.
func (sp *StateProcessor) applyLendingLiquidate(tx *types.Transaction, sender []byte) error {
	payload, err := sp.decodeLendingLiquidatePayload(tx.Data)
	if err != nil {
		return err
	}
	borrowerAddr, err := crypto.DecodeAddress(payload.Borrower)
	if err != nil {
		return fmt.Errorf("invalid borrower address: %w", err)
	}
	if bytes.Equal(borrowerAddr.Bytes(), sender) {
		return fmt.Errorf("lending: a borrower cannot liquidate their own position; use repay instead")
	}
	engine, _, err := sp.lendingEngine(payload.PoolID)
	if err != nil {
		return err
	}
	liquidatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...))
	if _, _, err := engine.Liquidate(liquidatorAddr, borrowerAddr); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) incrementNativeAccountNonce(sender []byte) error {
	if sp == nil {
		return fmt.Errorf("state unavailable")
	}
	account, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	account.Nonce++
	return sp.setAccount(sender, account)
}

type lendingStateAdapter struct {
	manager   *nhbstate.Manager
	poolID    string
	processor *StateProcessor
}

func (a *lendingStateAdapter) reconcileLegacyPoolState() error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	accounts, err := a.manager.AccountList()
	if err != nil {
		return err
	}
	for _, addr := range accounts {
		if _, err := a.reconcileLegacyUserAccount(crypto.MustNewAddress(crypto.NHBPrefix, addr[:])); err != nil {
			return err
		}
	}
	return nil
}

func (a *lendingStateAdapter) reconcileLegacyUserAccount(addr crypto.Address) (*lending.UserAccount, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	if account, ok, err := a.manager.LendingGetUserAccount(a.poolID, raw); err != nil {
		return nil, err
	} else if ok {
		if account.Address.Bytes() == nil {
			account.Address = addr
		}
		return account, nil
	}

	legacyAccount, err := a.manager.GetAccount(addr.Bytes())
	if err != nil {
		return nil, err
	}
	user, supplyAmount, debtAmount, ok := a.legacyLendingPosition(addr, legacyAccount)
	if !ok {
		return nil, nil
	}

	market, okMarket, err := a.manager.LendingGetMarket(a.poolID)
	if err != nil {
		return nil, err
	}
	if !okMarket || market == nil {
		market = a.defaultMarket()
	}
	if market.SupplyIndex == nil || market.SupplyIndex.Sign() == 0 {
		market.SupplyIndex = normalizedLendingIndexLegacy(legacyAccount.LendingSnapshot.SupplyIndex)
	}
	if market.BorrowIndex == nil || market.BorrowIndex.Sign() == 0 {
		market.BorrowIndex = normalizedLendingIndexLegacy(legacyAccount.LendingSnapshot.BorrowIndex)
	}
	if market.BorrowedThisBlock == nil {
		market.BorrowedThisBlock = big.NewInt(0)
	}
	if market.OracleMedianWei == nil {
		market.OracleMedianWei = big.NewInt(0)
	}
	if market.OraclePrevMedianWei == nil {
		market.OraclePrevMedianWei = big.NewInt(0)
	}
	market.TotalSupplyShares = sumBigIntLegacy(market.TotalSupplyShares, user.SupplyShares)
	market.TotalNHBSupplied = sumBigIntLegacy(market.TotalNHBSupplied, supplyAmount)
	market.TotalNHBBorrowed = sumBigIntLegacy(market.TotalNHBBorrowed, debtAmount)
	if a.processor != nil {
		market.LastUpdateBlock = a.processor.blockHeight()
	}

	if err := a.manager.LendingPutMarket(a.poolID, market); err != nil {
		return nil, err
	}
	if err := a.manager.LendingPutUserAccount(a.poolID, user); err != nil {
		return nil, err
	}
	if err := a.manager.PutAccount(addr.Bytes(), legacyAccount); err != nil {
		return nil, err
	}
	return user, nil
}

func (a *lendingStateAdapter) defaultMarket() *lending.Market {
	market := &lending.Market{
		PoolID:              a.poolID,
		LastUpdateBlock:     0,
		TotalNHBSupplied:    big.NewInt(0),
		TotalSupplyShares:   big.NewInt(0),
		TotalNHBBorrowed:    big.NewInt(0),
		SupplyIndex:         new(big.Int).Set(lendingLegacyRay),
		BorrowIndex:         new(big.Int).Set(lendingLegacyRay),
		BorrowedThisBlock:   big.NewInt(0),
		OracleMedianWei:     big.NewInt(0),
		OraclePrevMedianWei: big.NewInt(0),
	}
	if a.processor != nil {
		market.LastUpdateBlock = a.processor.blockHeight()
		market.ReserveFactor = a.processor.lendingReserveFactorBps
		market.DeveloperFeeBps = a.processor.lendingDeveloperFeeBps
		market.DeveloperOwner = cloneAddress(a.processor.lendingModuleAddr)
		market.DeveloperFeeCollector = cloneAddress(a.processor.lendingDeveloperCollector)
	}
	return market
}

func (a *lendingStateAdapter) legacyLendingPosition(addr crypto.Address, account *types.Account) (*lending.UserAccount, *big.Int, *big.Int, bool) {
	if account == nil {
		return nil, nil, nil, false
	}
	collateral := cloneBigIntLegacy(account.CollateralBalance)
	supplyShares := cloneBigIntLegacy(account.SupplyShares)
	debt := cloneBigIntLegacy(account.DebtPrincipal)
	if collateral.Sign() == 0 && supplyShares.Sign() == 0 && debt.Sign() == 0 {
		return nil, nil, nil, false
	}
	supplyIndex := normalizedLendingIndexLegacy(account.LendingSnapshot.SupplyIndex)
	borrowIndex := normalizedLendingIndexLegacy(account.LendingSnapshot.BorrowIndex)
	user := &lending.UserAccount{
		Address:        addr,
		CollateralZNHB: collateral,
		SupplyShares:   supplyShares,
		DebtNHB:        debt,
		ScaledDebt:     scaledDebtFromAmountLegacy(debt, borrowIndex),
	}
	return user, liquidityFromSharesLegacy(supplyShares, supplyIndex), debt, true
}

func normalizedLendingIndexLegacy(index *big.Int) *big.Int {
	if index == nil || index.Sign() == 0 {
		return new(big.Int).Set(lendingLegacyRay)
	}
	return new(big.Int).Set(index)
}

func cloneBigIntLegacy(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}

func sumBigIntLegacy(dst, add *big.Int) *big.Int {
	out := cloneBigIntLegacy(dst)
	if add == nil {
		return out
	}
	return out.Add(out, add)
}

func liquidityFromSharesLegacy(shares, index *big.Int) *big.Int {
	if shares == nil || shares.Sign() <= 0 {
		return big.NewInt(0)
	}
	normalized := normalizedLendingIndexLegacy(index)
	scaled := new(big.Int).Mul(shares, normalized)
	scaled.Add(scaled, new(big.Int).Rsh(new(big.Int).Set(lendingLegacyRay), 1))
	scaled.Quo(scaled, lendingLegacyRay)
	return scaled
}

func scaledDebtFromAmountLegacy(amount, index *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 {
		return big.NewInt(0)
	}
	normalized := normalizedLendingIndexLegacy(index)
	scaled := new(big.Int).Mul(amount, lendingLegacyRay)
	scaled.Add(scaled, halfUpLegacy(normalized))
	scaled.Quo(scaled, normalized)
	if scaled.Sign() == 0 {
		return big.NewInt(1)
	}
	return scaled
}

func halfUpLegacy(x *big.Int) *big.Int {
	if x == nil || x.Sign() <= 0 {
		return big.NewInt(0)
	}
	half := new(big.Int).Add(x, big.NewInt(1))
	half.Rsh(half, 1)
	return half
}

func (a *lendingStateAdapter) GetMarket(string) (*lending.Market, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	market, ok, err := a.manager.LendingGetMarket(a.poolID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return market, nil
}

func (a *lendingStateAdapter) PutMarket(_ string, market *lending.Market) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	return a.manager.LendingPutMarket(a.poolID, market)
}

func (a *lendingStateAdapter) GetUserAccount(_ string, addr crypto.Address) (*lending.UserAccount, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	account, ok, err := a.manager.LendingGetUserAccount(a.poolID, raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return a.reconcileLegacyUserAccount(addr)
	}
	if account.Address.Bytes() == nil {
		account.Address = addr
	}
	return account, nil
}

func (a *lendingStateAdapter) PutUserAccount(_ string, account *lending.UserAccount) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	if account == nil {
		return fmt.Errorf("lending: user account must not be nil")
	}
	return a.manager.LendingPutUserAccount(a.poolID, account)
}

func (a *lendingStateAdapter) GetFeeAccrual(string) (*lending.FeeAccrual, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	fees, ok, err := a.manager.LendingGetFeeAccrual(a.poolID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return fees, nil
}

func (a *lendingStateAdapter) PutFeeAccrual(_ string, fees *lending.FeeAccrual) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	if fees == nil {
		return fmt.Errorf("lending: fee accrual must not be nil")
	}
	return a.manager.LendingPutFeeAccrual(a.poolID, fees)
}

func (a *lendingStateAdapter) GetFixedTermLoan(loanID [32]byte) (*lending.FixedTermLoan, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	loan, ok, err := a.manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return loan, nil
}

func (a *lendingStateAdapter) PutFixedTermLoan(loan *lending.FixedTermLoan) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	return a.manager.LendingPutFixedTermLoan(loan)
}

func (a *lendingStateAdapter) GetFixedTermDeposit(depositID [32]byte) (*lending.FixedTermDeposit, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	deposit, ok, err := a.manager.LendingGetFixedTermDeposit(depositID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return deposit, nil
}

func (a *lendingStateAdapter) PutFixedTermDeposit(deposit *lending.FixedTermDeposit) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	return a.manager.LendingPutFixedTermDeposit(deposit)
}

func (a *lendingStateAdapter) GetActiveFixedTermLoanID(_ string, addr crypto.Address) ([32]byte, bool, error) {
	if a == nil || a.manager == nil {
		return [32]byte{}, false, fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	return a.manager.LendingGetActiveFixedTermLoanID(a.poolID, raw)
}

func (a *lendingStateAdapter) SetActiveFixedTermLoanID(_ string, addr crypto.Address, loanID [32]byte) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	return a.manager.LendingSetActiveFixedTermLoanID(a.poolID, raw, loanID)
}

func (a *lendingStateAdapter) ClearActiveFixedTermLoan(_ string, addr crypto.Address) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	return a.manager.LendingClearActiveFixedTermLoan(a.poolID, raw)
}

func (a *lendingStateAdapter) GetAccount(addr crypto.Address) (*types.Account, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	return a.manager.GetAccount(addr.Bytes())
}

func (a *lendingStateAdapter) PutAccount(addr crypto.Address, account *types.Account) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	if account == nil {
		return fmt.Errorf("lending: account must not be nil")
	}
	return a.manager.PutAccount(addr.Bytes(), account)
}

// applyLendingCreatePoolTransaction handles TxTypeLendingCreatePool. This
// replaces LendingModule.CreatePool's old behavior of writing a brand new
// market straight into the live pending state trie via Node.WithState
// (rpc/modules/lending.go) -- a write invisible to every other validator,
// which never received the RPC call, guaranteed to diverge state roots the
// moment more than one validator exists. The new pool's DeveloperOwner is
// always the transaction's own recovered signer (sender), never a
// client-supplied field: the old RPC accepted an arbitrary developerOwner
// bech32 string with zero proof the caller controlled that address's key,
// letting anyone hand a stranger's address perpetual developer-fee rights
// over a pool they never asked for.
func (sp *StateProcessor) applyLendingCreatePoolTransaction(tx *types.Transaction, sender []byte) error {
	payload, err := sp.decodeLendingPayload(tx.Data)
	if err != nil {
		return err
	}
	// decodeLendingPayload defaults an empty poolId to defaultLendingPoolID,
	// so this also catches the empty-payload case -- the implicit "default"
	// pool is bootstrapped elsewhere (ensureMarket/defaultMarket) and must
	// never be shadowed by an explicit CreatePool of the same name.
	poolID := strings.TrimSpace(payload.PoolID)
	if poolID == defaultLendingPoolID {
		return fmt.Errorf("lendingCreatePool: a non-default poolId is required")
	}

	manager := nhbstate.NewManager(sp.Trie)
	if existing, ok, err := manager.LendingGetMarket(poolID); err != nil {
		return fmt.Errorf("lendingCreatePool: check existing pool: %w", err)
	} else if ok && existing != nil {
		return fmt.Errorf("lendingCreatePool: pool %q already exists", poolID)
	}

	market := &lending.Market{
		PoolID:                poolID,
		DeveloperOwner:        crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)),
		DeveloperFeeBps:       sp.lendingDeveloperFeeBps,
		DeveloperFeeCollector: sp.lendingDeveloperCollector,
		ReserveFactor:         sp.lendingReserveFactorBps,
		LastUpdateBlock:       sp.blockHeight(),
		TotalNHBSupplied:      big.NewInt(0),
		TotalSupplyShares:     big.NewInt(0),
		TotalNHBBorrowed:      big.NewInt(0),
	}
	if err := manager.LendingPutMarket(poolID, market); err != nil {
		return fmt.Errorf("lendingCreatePool: persist market: %w", err)
	}

	return sp.incrementNativeAccountNonce(sender)
}
