package lending

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

type mockEngineState struct {
	market   *Market
	users    map[string]*UserAccount
	accounts map[string]*types.Account
	fees     *FeeAccrual
}

func newMockEngineState() *mockEngineState {
	return &mockEngineState{
		users:    make(map[string]*UserAccount),
		accounts: make(map[string]*types.Account),
	}
}

func (m *mockEngineState) key(addr crypto.Address) string {
	return string(addr.Bytes())
}

func (m *mockEngineState) GetMarket(string) (*Market, error) {
	return m.market, nil
}

func (m *mockEngineState) PutMarket(_ string, market *Market) error {
	m.market = market
	return nil
}

func (m *mockEngineState) GetUserAccount(_ string, addr crypto.Address) (*UserAccount, error) {
	if acc, ok := m.users[m.key(addr)]; ok {
		return acc, nil
	}
	return nil, nil
}

func (m *mockEngineState) PutUserAccount(_ string, account *UserAccount) error {
	if account == nil {
		return nil
	}
	m.users[m.key(account.Address)] = account
	return nil
}

func (m *mockEngineState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if acc, ok := m.accounts[m.key(addr)]; ok {
		if acc.BalanceNHB == nil {
			acc.BalanceNHB = big.NewInt(0)
		}
		if acc.BalanceZNHB == nil {
			acc.BalanceZNHB = big.NewInt(0)
		}
		return acc, nil
	}
	return nil, errInsufficientBalance
}

func (m *mockEngineState) PutAccount(addr crypto.Address, account *types.Account) error {
	m.accounts[m.key(addr)] = account
	return nil
}

func (m *mockEngineState) GetFeeAccrual(string) (*FeeAccrual, error) {
	return m.fees, nil
}

func (m *mockEngineState) PutFeeAccrual(_ string, fees *FeeAccrual) error {
	m.fees = fees
	return nil
}

func makeAddress(prefix crypto.AddressPrefix, suffix byte) crypto.Address {
	raw := make([]byte, 20)
	raw[len(raw)-1] = suffix
	return crypto.NewAddress(prefix, raw)
}

func TestAccrueInterestUpdatesIndexesAndFees(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x01)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x02)

	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{})
	engine.SetInterestModel(NewInterestModel(0, 1, 0, 1))
	engine.SetReserveFactor(2000)
	engine.SetProtocolFeeBps(1000)
	engine.SetBlockHeight(blocksPerYear)

	state := newMockEngineState()
	market := &Market{
		TotalNHBSupplied:  big.NewInt(1000),
		TotalSupplyShares: big.NewInt(1000),
		TotalNHBBorrowed:  big.NewInt(500),
		SupplyIndex:       new(big.Int).Set(ray),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	state.market = market
	engine.SetState(state)
	engine.SetPoolID("default")

	fees, changed, err := engine.accrueInterest(market)
	if err != nil {
		t.Fatalf("accrue interest: %v", err)
	}
	if !changed {
		t.Fatalf("expected accrual to change state")
	}

	expectedBorrowIndex := new(big.Int).Mul(ray, big.NewInt(3))
	expectedBorrowIndex = expectedBorrowIndex.Quo(expectedBorrowIndex, big.NewInt(2))
	if market.BorrowIndex.Cmp(expectedBorrowIndex) != 0 {
		t.Fatalf("unexpected borrow index: got %s want %s", market.BorrowIndex, expectedBorrowIndex)
	}

	expectedSupplyIndex := new(big.Int).Mul(ray, big.NewInt(1175))
	expectedSupplyIndex = expectedSupplyIndex.Quo(expectedSupplyIndex, big.NewInt(1000))
	if market.SupplyIndex.Cmp(expectedSupplyIndex) != 0 {
		t.Fatalf("unexpected supply index: got %s want %s", market.SupplyIndex, expectedSupplyIndex)
	}

	if market.TotalNHBBorrowed.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("unexpected total borrowed: got %s", market.TotalNHBBorrowed)
	}
	if market.TotalNHBSupplied.Cmp(big.NewInt(1250)) != 0 {
		t.Fatalf("unexpected total supplied: got %s", market.TotalNHBSupplied)
	}

	if fees == nil || fees.ProtocolFeesWei.Cmp(big.NewInt(75)) != 0 {
		t.Fatalf("unexpected protocol fees: got %v", fees)
	}
}

func TestWithdrawProtocolFees(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x03)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x04)
	recipient := makeAddress(crypto.NHBPrefix, 0x05)

	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{})
	state := newMockEngineState()
	state.market = &Market{TotalNHBSupplied: big.NewInt(500)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(400)}
	state.accounts[state.key(recipient)] = &types.Account{BalanceNHB: big.NewInt(0)}
	state.fees = &FeeAccrual{ProtocolFeesWei: big.NewInt(150), DeveloperFeesWei: big.NewInt(10)}
	engine.SetState(state)
	engine.SetPoolID("default")

	withdrawn, err := engine.WithdrawProtocolFees(recipient, big.NewInt(100))
	if err != nil {
		t.Fatalf("withdraw protocol fees: %v", err)
	}
	if withdrawn.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected withdrawn amount: %s", withdrawn)
	}

	moduleAcc := state.accounts[state.key(moduleAddr)]
	if moduleAcc.BalanceNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("unexpected module balance: %s", moduleAcc.BalanceNHB)
	}
	recipientAcc := state.accounts[state.key(recipient)]
	if recipientAcc.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected recipient balance: %s", recipientAcc.BalanceNHB)
	}
	if state.market.TotalNHBSupplied.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected total supplied after withdraw: %s", state.market.TotalNHBSupplied)
	}
	if state.fees == nil || state.fees.ProtocolFeesWei.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("unexpected protocol fees after withdraw: %v", state.fees)
	}
}
