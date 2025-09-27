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

func (m *LendingModule) moduleUnavailable() *ModuleError {
	return &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "lending module not available"}
}

func (m *LendingModule) GetMarket() (*lending.Market, lending.RiskParameters, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, lending.RiskParameters{}, m.moduleUnavailable()
	}
	params := m.node.LendingRiskParameters()
	var market *lending.Market
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetMarket()
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

func (m *LendingModule) GetUserAccount(addr [20]byte) (*lending.UserAccount, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, m.moduleUnavailable()
	}
	var account *lending.UserAccount
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetUserAccount(addr)
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

func (m *LendingModule) SupplyNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var minted *big.Int
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) WithdrawNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var redeemed *big.Int
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) DepositZNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	err := m.withEngine(func(engine *lending.Engine) error {
		return engine.DepositCollateral(toCryptoAddress(addr), amount)
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("deposit-collateral", formatHexAddress(addr), amount), nil
}

func (m *LendingModule) WithdrawZNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	err := m.withEngine(func(engine *lending.Engine) error {
		return engine.WithdrawCollateral(toCryptoAddress(addr), amount)
	})
	if err != nil {
		return "", m.wrapError(err)
	}
	return m.makeTxHash("withdraw-collateral", formatHexAddress(addr), amount), nil
}

func (m *LendingModule) BorrowNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var fee *big.Int
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) BorrowNHBWithFee(borrower [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	feeBps, feeCollector := m.node.LendingDeveloperFeeConfig()
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
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) RepayNHB(addr [20]byte, amount *big.Int) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var repaid *big.Int
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) Liquidate(liquidator [20]byte, borrower [20]byte) (string, *ModuleError) {
	if m == nil || m.node == nil {
		return "", m.moduleUnavailable()
	}
	var repaid, seized *big.Int
	err := m.withEngine(func(engine *lending.Engine) error {
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

func (m *LendingModule) withEngine(fn func(*lending.Engine) error) error {
	if fn == nil {
		return fmt.Errorf("lending: callback required")
	}
	return m.node.WithState(func(manager *nhbstate.Manager) error {
		engine := lending.NewEngine(m.node.LendingModuleAddress(), m.node.LendingCollateralAddress(), m.node.LendingRiskParameters())
		engine.SetState(&lendingStateAdapter{manager: manager})
		engine.SetInterestModel(m.node.LendingInterestModel())
		engine.SetReserveFactor(m.node.LendingReserveFactorBps())
		engine.SetProtocolFeeBps(m.node.LendingProtocolFeeBps())
		engine.SetBlockHeight(m.node.GetHeight())
		return fn(engine)
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
}

func (a *lendingStateAdapter) GetMarket() (*lending.Market, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	market, ok, err := a.manager.LendingGetMarket()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return market, nil
}

func (a *lendingStateAdapter) PutMarket(market *lending.Market) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	return a.manager.LendingPutMarket(market)
}

func (a *lendingStateAdapter) GetUserAccount(addr crypto.Address) (*lending.UserAccount, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	account, ok, err := a.manager.LendingGetUserAccount(raw)
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

func (a *lendingStateAdapter) PutUserAccount(account *lending.UserAccount) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	if account == nil {
		return fmt.Errorf("lending: user account must not be nil")
	}
	return a.manager.LendingPutUserAccount(account)
}

func (a *lendingStateAdapter) GetFeeAccrual() (*lending.FeeAccrual, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("lending: state manager unavailable")
	}
	fees, ok, err := a.manager.LendingGetFeeAccrual()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return fees, nil
}

func (a *lendingStateAdapter) PutFeeAccrual(fees *lending.FeeAccrual) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("lending: state manager unavailable")
	}
	if fees == nil {
		return fmt.Errorf("lending: fee accrual must not be nil")
	}
	return a.manager.LendingPutFeeAccrual(fees)
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
