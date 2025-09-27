package modules

import (
	"math/big"
	"net/http"

	"nhbchain/core"
	"nhbchain/native/lending"
)

type LendingModule struct {
	node *core.Node
}

func NewLendingModule(node *core.Node) *LendingModule {
	return &LendingModule{node: node}
}

func (m *LendingModule) moduleUnavailable() *ModuleError {
	return &ModuleError{HTTPStatus: http.StatusNotImplemented, Code: codeServerError, Message: "lending module not available"}
}

func (m *LendingModule) GetMarket() (*lending.Market, lending.RiskParameters, *ModuleError) {
	return nil, lending.RiskParameters{}, m.moduleUnavailable()
}

func (m *LendingModule) GetUserAccount(_ [20]byte) (*lending.UserAccount, *ModuleError) {
	return nil, m.moduleUnavailable()
}

func (m *LendingModule) SupplyNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) WithdrawNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) DepositZNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) WithdrawZNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) BorrowNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) BorrowNHBWithFee(_ [20]byte, _ *big.Int, _ [20]byte, _ uint64) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) RepayNHB(_ [20]byte, _ *big.Int) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}

func (m *LendingModule) Liquidate(_ [20]byte, _ [20]byte) (string, *ModuleError) {
	return "", m.moduleUnavailable()
}
