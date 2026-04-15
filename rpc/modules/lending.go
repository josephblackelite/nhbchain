package modules

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

type LendingModule struct {
	node *core.Node
}

func NewLendingModule(node *core.Node) *LendingModule {
	return &LendingModule{node: node}
}

const defaultLendingPoolID = "default"

var lendingRay = mustBigInt("1000000000000000000000000000")

type lendingReplayPayload struct {
	PoolID string `json:"poolId,omitempty"`
}

func (m *LendingModule) moduleUnavailable() *ModuleError {
	return &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "lending module not available"}
}

func (m *LendingModule) GetMarket(poolID string) (*lending.Market, lending.RiskParameters, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, lending.RiskParameters{}, m.moduleUnavailable()
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	params := m.node.LendingRiskParameters()
	var market *lending.Market
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetMarket(id)
		if err != nil {
			return err
		}
		if ok {
			market = stored
		}
		return nil
	})
	if err != nil {
		return nil, params, m.wrapError(err)
	}
	if marketNeedsRebuild(market) {
		if err := m.rebuildCommittedPoolState(id); err != nil {
			return nil, params, m.wrapError(err)
		}
		err = m.node.WithState(func(manager *nhbstate.Manager) error {
			stored, ok, err := manager.LendingGetMarket(id)
			if err != nil {
				return err
			}
			if ok {
				market = stored
			}
			return nil
		})
		if err != nil {
			return nil, params, m.wrapError(err)
		}
	}
	if err := m.reconcileLegacyPoolState(id); err != nil {
		return nil, lending.RiskParameters{}, m.wrapError(err)
	}
	if market == nil && id == defaultLendingPoolID {
		market = m.defaultMarket(id)
	}
	return market, params, nil
}

func (m *LendingModule) GetPools() ([]*lending.Market, lending.RiskParameters, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, lending.RiskParameters{}, m.moduleUnavailable()
	}
	params := m.node.LendingRiskParameters()
	var markets []*lending.Market
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		list, err := manager.LendingListMarkets()
		if err != nil {
			return err
		}
		markets = list
		return nil
	})
	if err != nil {
		return nil, params, m.wrapError(err)
	}
	if len(markets) == 0 || allMarketsNeedRebuild(markets) {
		if err := m.rebuildCommittedPoolState(defaultLendingPoolID); err != nil {
			return nil, params, m.wrapError(err)
		}
		err = m.node.WithState(func(manager *nhbstate.Manager) error {
			list, err := manager.LendingListMarkets()
			if err != nil {
				return err
			}
			markets = list
			return nil
		})
		if err != nil {
			return nil, params, m.wrapError(err)
		}
	}
	if err := m.reconcileLegacyPoolState(defaultLendingPoolID); err != nil {
		return nil, lending.RiskParameters{}, m.wrapError(err)
	}
	if len(markets) == 0 {
		markets = []*lending.Market{m.defaultMarket(defaultLendingPoolID)}
	}
	return markets, params, nil
}

func (m *LendingModule) defaultMarket(poolID string) *lending.Market {
	if m == nil || m.node == nil {
		return nil
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	feeBps, collector := m.node.LendingDeveloperFeeConfig()
	return &lending.Market{
		PoolID:                id,
		DeveloperOwner:        m.node.LendingModuleAddress(),
		DeveloperFeeBps:       feeBps,
		DeveloperFeeCollector: collector,
		ReserveFactor:         m.node.LendingReserveFactorBps(),
		LastUpdateBlock:       m.node.GetHeight(),
		TotalNHBSupplied:      big.NewInt(0),
		TotalSupplyShares:     big.NewInt(0),
		TotalNHBBorrowed:      big.NewInt(0),
	}
}

func (m *LendingModule) CreatePool(poolID string, owner [20]byte) (*lending.Market, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, m.moduleUnavailable()
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "poolId required"}
	}
	ownerAddr := toCryptoAddress(owner)
	var created *lending.Market
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		existing, ok, err := manager.LendingGetMarket(id)
		if err != nil {
			return err
		}
		if ok && existing != nil {
			return fmt.Errorf("lending: pool %s already exists", id)
		}
		bps, collector := m.node.LendingDeveloperFeeConfig()
		market := &lending.Market{
			PoolID:                id,
			DeveloperOwner:        ownerAddr,
			DeveloperFeeBps:       bps,
			DeveloperFeeCollector: collector,
			ReserveFactor:         m.node.LendingReserveFactorBps(),
			LastUpdateBlock:       m.node.GetHeight(),
			TotalNHBSupplied:      big.NewInt(0),
			TotalSupplyShares:     big.NewInt(0),
			TotalNHBBorrowed:      big.NewInt(0),
		}
		if err := manager.LendingPutMarket(id, market); err != nil {
			return err
		}
		created = market
		return nil
	})
	if err != nil {
		return nil, m.wrapError(err)
	}
	return created, nil
}

func (m *LendingModule) GetUserAccount(poolID string, addr [20]byte) (*lending.UserAccount, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, m.moduleUnavailable()
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	var account *lending.UserAccount
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetUserAccount(id, addr)
		if err != nil {
			return err
		}
		if ok {
			account = stored
			return nil
		}
		migrated, migratedErr := m.reconcileLegacyUserAccount(manager, id, addr)
		if migratedErr != nil {
			return migratedErr
		}
		account = migrated
		return nil
	})
	if err != nil {
		return nil, m.wrapError(err)
	}
	if account == nil {
		if err := m.rebuildCommittedPoolState(id); err != nil {
			return nil, m.wrapError(err)
		}
		err = m.node.WithState(func(manager *nhbstate.Manager) error {
			stored, ok, err := manager.LendingGetUserAccount(id, addr)
			if err != nil {
				return err
			}
			if ok {
				account = stored
			}
			return nil
		})
		if err != nil {
			return nil, m.wrapError(err)
		}
	}
	return account, nil
}

func marketNeedsRebuild(market *lending.Market) bool {
	if market == nil {
		return true
	}
	if market.TotalNHBSupplied != nil && market.TotalNHBSupplied.Sign() > 0 {
		return false
	}
	if market.TotalNHBBorrowed != nil && market.TotalNHBBorrowed.Sign() > 0 {
		return false
	}
	return true
}

func allMarketsNeedRebuild(markets []*lending.Market) bool {
	if len(markets) == 0 {
		return true
	}
	for _, market := range markets {
		if !marketNeedsRebuild(market) {
			return false
		}
	}
	return true
}

func (m *LendingModule) rebuildCommittedPoolState(poolID string) error {
	if m == nil || m.node == nil {
		return fmt.Errorf("lending module unavailable")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	market, users, err := m.replayCommittedPoolState(id)
	if err != nil {
		return err
	}
	if market == nil {
		return nil
	}
	return m.node.WithState(func(manager *nhbstate.Manager) error {
		if err := manager.LendingPutMarket(id, market); err != nil {
			return err
		}
		for _, user := range users {
			if err := manager.LendingPutUserAccount(id, user); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *LendingModule) replayCommittedPoolState(poolID string) (*lending.Market, map[[20]byte]*lending.UserAccount, error) {
	if m == nil || m.node == nil {
		return nil, nil, fmt.Errorf("lending module unavailable")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	market := m.defaultMarket(id)
	market.SupplyIndex = new(big.Int).Set(lendingRay)
	market.BorrowIndex = new(big.Int).Set(lendingRay)
	market.BorrowedThisBlock = big.NewInt(0)
	market.OracleMedianWei = big.NewInt(0)
	market.OraclePrevMedianWei = big.NewInt(0)
	users := make(map[[20]byte]*lending.UserAccount)
	var sawLendingTx bool
	height := m.node.GetHeight()
	for blockHeight := uint64(1); blockHeight <= height; blockHeight++ {
		block, err := m.node.GetBlockByHeight(blockHeight)
		if err != nil || block == nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx == nil || !isCommittedLendingTxType(tx.Type) {
				continue
			}
			txPoolID, err := lendingPoolIDFromTxData(tx.Data)
			if err != nil || txPoolID != id {
				continue
			}
			from, err := tx.From()
			if err != nil || len(from) == 0 {
				continue
			}
			var addr [20]byte
			copy(addr[:], from)
			user := users[addr]
			if user == nil {
				user = &lending.UserAccount{
					Address:        toCryptoAddress(addr),
					CollateralZNHB: big.NewInt(0),
					SupplyShares:   big.NewInt(0),
					DebtNHB:        big.NewInt(0),
					ScaledDebt:     big.NewInt(0),
				}
				users[addr] = user
			}
			amount := cloneBigInt(tx.Value)
			switch tx.Type {
			case types.TxTypeLendingSupplyNHB:
				user.SupplyShares = sumBigInt(user.SupplyShares, amount)
				market.TotalSupplyShares = sumBigInt(market.TotalSupplyShares, amount)
				market.TotalNHBSupplied = sumBigInt(market.TotalNHBSupplied, amount)
			case types.TxTypeLendingWithdrawNHB:
				if user.SupplyShares.Cmp(amount) < 0 {
					user.SupplyShares = big.NewInt(0)
				} else {
					user.SupplyShares = new(big.Int).Sub(user.SupplyShares, amount)
				}
				if market.TotalSupplyShares.Cmp(amount) < 0 {
					market.TotalSupplyShares = big.NewInt(0)
				} else {
					market.TotalSupplyShares = new(big.Int).Sub(market.TotalSupplyShares, amount)
				}
				if market.TotalNHBSupplied.Cmp(amount) < 0 {
					market.TotalNHBSupplied = big.NewInt(0)
				} else {
					market.TotalNHBSupplied = new(big.Int).Sub(market.TotalNHBSupplied, amount)
				}
			case types.TxTypeLendingDepositZNHB:
				user.CollateralZNHB = sumBigInt(user.CollateralZNHB, amount)
			case types.TxTypeLendingWithdrawZNHB:
				if user.CollateralZNHB.Cmp(amount) < 0 {
					user.CollateralZNHB = big.NewInt(0)
				} else {
					user.CollateralZNHB = new(big.Int).Sub(user.CollateralZNHB, amount)
				}
			case types.TxTypeLendingBorrowNHB:
				user.DebtNHB = sumBigInt(user.DebtNHB, amount)
				user.ScaledDebt = sumBigInt(user.ScaledDebt, amount)
				market.TotalNHBBorrowed = sumBigInt(market.TotalNHBBorrowed, amount)
			case types.TxTypeLendingRepayNHB:
				repayAmount := amount
				if user.DebtNHB.Cmp(repayAmount) < 0 {
					repayAmount = new(big.Int).Set(user.DebtNHB)
				}
				if repayAmount.Sign() > 0 {
					user.DebtNHB = new(big.Int).Sub(user.DebtNHB, repayAmount)
					if user.ScaledDebt.Cmp(repayAmount) < 0 {
						user.ScaledDebt = big.NewInt(0)
					} else {
						user.ScaledDebt = new(big.Int).Sub(user.ScaledDebt, repayAmount)
					}
					if market.TotalNHBBorrowed.Cmp(repayAmount) < 0 {
						market.TotalNHBBorrowed = big.NewInt(0)
					} else {
						market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)
					}
				}
			}
			market.LastUpdateBlock = blockHeight
			sawLendingTx = true
		}
	}
	if !sawLendingTx {
		return nil, nil, nil
	}
	return market, users, nil
}

func isCommittedLendingTxType(txType types.TxType) bool {
	switch txType {
	case types.TxTypeLendingSupplyNHB,
		types.TxTypeLendingWithdrawNHB,
		types.TxTypeLendingDepositZNHB,
		types.TxTypeLendingWithdrawZNHB,
		types.TxTypeLendingBorrowNHB,
		types.TxTypeLendingRepayNHB:
		return true
	default:
		return false
	}
}

func lendingPoolIDFromTxData(data []byte) (string, error) {
	if len(data) == 0 {
		return defaultLendingPoolID, nil
	}
	var payload lendingReplayPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	id := strings.TrimSpace(payload.PoolID)
	if id == "" {
		return defaultLendingPoolID, nil
	}
	return id, nil
}

func (m *LendingModule) reconcileLegacyPoolState(poolID string) error {
	if m == nil || m.node == nil {
		return fmt.Errorf("lending module unavailable")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	return m.node.WithState(func(manager *nhbstate.Manager) error {
		accounts, err := manager.AccountList()
		if err != nil {
			return err
		}
		for _, addr := range accounts {
			if _, err := m.reconcileLegacyUserAccount(manager, id, addr); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *LendingModule) reconcileLegacyUserAccount(manager *nhbstate.Manager, poolID string, addr [20]byte) (*lending.UserAccount, error) {
	if manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	if existing, ok, err := manager.LendingGetUserAccount(poolID, addr); err != nil {
		return nil, err
	} else if ok {
		return existing, nil
	}

	account, err := manager.GetAccount(addr[:])
	if err != nil {
		return nil, err
	}
	user, supplyAmount, debtAmount, ok := m.legacyLendingPosition(addr, account)
	if !ok {
		return nil, nil
	}

	market, marketFound, err := manager.LendingGetMarket(poolID)
	if err != nil {
		return nil, err
	}
	if !marketFound || market == nil {
		market = m.defaultMarket(poolID)
	}
	if market.SupplyIndex == nil || market.SupplyIndex.Sign() == 0 {
		market.SupplyIndex = normalizedLendingIndex(account.LendingSnapshot.SupplyIndex)
	}
	if market.BorrowIndex == nil || market.BorrowIndex.Sign() == 0 {
		market.BorrowIndex = normalizedLendingIndex(account.LendingSnapshot.BorrowIndex)
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
	market.TotalSupplyShares = sumBigInt(market.TotalSupplyShares, user.SupplyShares)
	market.TotalNHBSupplied = sumBigInt(market.TotalNHBSupplied, supplyAmount)
	market.TotalNHBBorrowed = sumBigInt(market.TotalNHBBorrowed, debtAmount)
	market.LastUpdateBlock = m.node.GetHeight()

	if err := manager.LendingPutMarket(poolID, market); err != nil {
		return nil, err
	}
	if err := manager.LendingPutUserAccount(poolID, user); err != nil {
		return nil, err
	}
	if err := manager.PutAccount(addr[:], account); err != nil {
		return nil, err
	}
	return user, nil
}

func (m *LendingModule) legacyLendingPosition(addr [20]byte, account *types.Account) (*lending.UserAccount, *big.Int, *big.Int, bool) {
	if account == nil {
		return nil, nil, nil, false
	}
	collateral := cloneBigInt(account.CollateralBalance)
	supplyShares := cloneBigInt(account.SupplyShares)
	debt := cloneBigInt(account.DebtPrincipal)
	if collateral.Sign() == 0 && supplyShares.Sign() == 0 && debt.Sign() == 0 {
		return nil, nil, nil, false
	}
	supplyIndex := normalizedLendingIndex(account.LendingSnapshot.SupplyIndex)
	borrowIndex := normalizedLendingIndex(account.LendingSnapshot.BorrowIndex)
	user := &lending.UserAccount{
		Address:        toCryptoAddress(addr),
		CollateralZNHB: collateral,
		SupplyShares:   supplyShares,
		DebtNHB:        debt,
		ScaledDebt:     scaledDebtFromAmountLegacy(debt, borrowIndex),
	}
	return user, liquidityFromSharesLegacy(supplyShares, supplyIndex), debt, true
}

func normalizedLendingIndex(index *big.Int) *big.Int {
	if index == nil || index.Sign() == 0 {
		return new(big.Int).Set(lendingRay)
	}
	return new(big.Int).Set(index)
}

func liquidityFromSharesLegacy(shares, index *big.Int) *big.Int {
	if shares == nil || shares.Sign() <= 0 {
		return big.NewInt(0)
	}
	normalized := normalizedLendingIndex(index)
	scaled := new(big.Int).Mul(shares, normalized)
	scaled.Add(scaled, new(big.Int).Rsh(new(big.Int).Set(lendingRay), 1))
	scaled.Quo(scaled, lendingRay)
	return scaled
}

func scaledDebtFromAmountLegacy(amount, index *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 {
		return big.NewInt(0)
	}
	normalized := normalizedLendingIndex(index)
	scaled := new(big.Int).Mul(amount, lendingRay)
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

func sumBigInt(dst, add *big.Int) *big.Int {
	out := cloneBigInt(dst)
	if add == nil {
		return out
	}
	return out.Add(out, add)
}

func cloneBigInt(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}

func mustBigInt(value string) *big.Int {
	out, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	if !ok {
		panic("invalid big integer constant")
	}
	return out
}

func (m *LendingModule) SupplyNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var minted *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		shares, err := engine.Supply(toCryptoAddress(addr), amount)
		if err != nil {
			return err
		}
		minted = shares
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("supply", formatHexAddress(addr), amount, minted), nil
}

func (m *LendingModule) WithdrawNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var redeemed *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		nhb, err := engine.Withdraw(toCryptoAddress(addr), amount)
		if err != nil {
			return err
		}
		redeemed = nhb
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("withdraw", formatHexAddress(addr), amount, redeemed), nil
}

func (m *LendingModule) DepositZNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		return engine.DepositCollateral(toCryptoAddress(addr), amount)
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("deposit-collateral", formatHexAddress(addr), amount), nil
}

func (m *LendingModule) WithdrawZNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		return engine.WithdrawCollateral(toCryptoAddress(addr), amount)
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("withdraw-collateral", formatHexAddress(addr), amount), nil
}

func (m *LendingModule) BorrowNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var fee *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		paidFee, err := engine.Borrow(toCryptoAddress(addr), amount, toCryptoAddress(addr), 0)
		if err != nil {
			return err
		}
		fee = paidFee
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("borrow", formatHexAddress(addr), amount, fee), nil
}

func (m *LendingModule) BorrowNHBWithFee(poolID string, borrower [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	market, _, moduleErr := m.GetMarket(poolID)
	if moduleErr != nil {
		return "", moduleErr
	}
	if market == nil {
		return "", &ModuleError{HTTPStatus: http.StatusNotFound, Code: codeInvalidParams, Message: "pool not initialised"}
	}
	feeBps := market.DeveloperFeeBps
	feeCollector := market.DeveloperFeeCollector
	if feeBps == 0 {
		return "", &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "developer fee disabled"}
	}
	if len(feeCollector.Bytes()) == 0 {
		return "", &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "developer fee collector not configured"}
	}
	if !m.node.IsTreasuryAllowListed(feeCollector) {
		return "", &ModuleError{HTTPStatus: http.StatusForbidden, Code: codeInvalidParams, Message: "developer fee collector not authorised"}
	}
	var feeCollectorRaw [20]byte
	copy(feeCollectorRaw[:], feeCollector.Bytes())
	var fee *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		paidFee, err := engine.Borrow(toCryptoAddress(borrower), amount, feeCollector, feeBps)
		if err != nil {
			return err
		}
		fee = paidFee
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	primary := fmt.Sprintf("%s:%s", formatHexAddress(borrower), formatHexAddress(feeCollectorRaw))
	return m.makeTxHash("borrow-with-fee", primary, amount, fee, big.NewInt(int64(feeBps))), nil
}

func (m *LendingModule) RepayNHB(poolID string, addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var repaid *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		settled, err := engine.Repay(toCryptoAddress(addr), amount)
		if err != nil {
			return err
		}
		repaid = settled
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("repay", formatHexAddress(addr), amount, repaid), nil
}

func (m *LendingModule) Liquidate(poolID string, liquidator [20]byte, borrower [20]byte) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var repaid, seized *big.Int
	err := m.withEngine(poolID, func(engine *lending.Engine, _ *lending.Market) error {
		debt, collateral, err := engine.Liquidate(toCryptoAddress(liquidator), toCryptoAddress(borrower))
		if err != nil {
			return err
		}
		repaid = debt
		seized = collateral
		return nil
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	primary := fmt.Sprintf("%s:%s", formatHexAddress(liquidator), formatHexAddress(borrower))
	return m.makeTxHash("liquidate", primary, repaid, seized), nil
}

func (m *LendingModule) withEngine(poolID string, fn func(*lending.Engine, *lending.Market) error) error {
	if fn == nil {
		return fmt.Errorf("lending: callback required")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	return m.node.WithState(func(manager *nhbstate.Manager) error {
		adapter := &lendingStateAdapter{manager: manager, poolID: id}
		engine := lending.NewEngine(m.node.LendingModuleAddress(), m.node.LendingCollateralAddress(), m.node.LendingRiskParameters())
		engine.SetPauses(m.node)
		engine.SetState(adapter)
		engine.SetPoolID(id)
		engine.SetInterestModel(m.node.LendingInterestModel())
		engine.SetReserveFactor(m.node.LendingReserveFactorBps())
		engine.SetProtocolFeeBps(m.node.LendingProtocolFeeBps())
		engine.SetBlockHeight(m.node.GetHeight())
		engine.SetCollateralRouting(m.node.LendingCollateralRouting())
		var market *lending.Market
		stored, ok, err := manager.LendingGetMarket(id)
		if err != nil {
			return err
		}
		if ok {
			market = stored
			engine.SetDeveloperFee(stored.DeveloperFeeBps, stored.DeveloperFeeCollector)
		} else {
			bps, collector := m.node.LendingDeveloperFeeConfig()
			engine.SetDeveloperFee(bps, collector)
		}
		return fn(engine, market)
	})
}

func (m *LendingModule) makeTxHash(kind, primary string, amount *big.Int, extras ...*big.Int) string {
	parts := []string{kind, primary}
	if amount != nil {
		parts = append(parts, amount.String())
	}
	for _, extra := range extras {
		if extra != nil {
			parts = append(parts, extra.String())
		}
	}
	parts = append(parts, fmt.Sprintf("%d", m.node.GetHeight()))
	parts = append(parts, fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	payload := strings.Join(parts, "|")
	hash := ethcrypto.Keccak256([]byte(payload))
	return "0x" + hex.EncodeToString(hash)
}

func (m *LendingModule) wrapError(err error) *ModuleError {
	if err == nil {
		return nil
	}
	status := http.StatusInternalServerError
	code := codeServerError
	message := err.Error()
	if strings.HasPrefix(message, "lending engine:") {
		status = http.StatusBadRequest
		code = codeInvalidParams
	}
	return &ModuleError{HTTPStatus: status, Code: code, Message: message}
}

func toCryptoAddress(raw [20]byte) crypto.Address {
	return crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), raw[:]...))
}

func formatHexAddress(raw [20]byte) string {
	return hex.EncodeToString(raw[:])
}

type lendingStateAdapter struct {
	manager *nhbstate.Manager
	poolID  string
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
		return nil, nil
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
