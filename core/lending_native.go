package core

import (
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
