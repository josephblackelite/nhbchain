package modules

import (
	"encoding/hex"
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
	if markets == nil {
		markets = []*lending.Market{}
	}
	return markets, params, nil
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
		}
		return nil
	})
	if err != nil {
		return nil, m.wrapError(err)
	}
	return account, nil
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
		engine.SetState(adapter)
		engine.SetPoolID(id)
		engine.SetInterestModel(m.node.LendingInterestModel())
		engine.SetReserveFactor(m.node.LendingReserveFactorBps())
		engine.SetProtocolFeeBps(m.node.LendingProtocolFeeBps())
		engine.SetBlockHeight(m.node.GetHeight())
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
	return crypto.NewAddress(crypto.NHBPrefix, append([]byte(nil), raw[:]...))
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
