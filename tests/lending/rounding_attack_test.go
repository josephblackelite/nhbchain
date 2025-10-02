package lending_test

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

type mockEngineState struct {
	market   *lending.Market
	users    map[string]*lending.UserAccount
	accounts map[string]*types.Account
	fees     *lending.FeeAccrual
}

func newMockEngineState() *mockEngineState {
	return &mockEngineState{
		users:    make(map[string]*lending.UserAccount),
		accounts: make(map[string]*types.Account),
	}
}

func (m *mockEngineState) key(addr crypto.Address) string {
	return string(addr.Bytes())
}

func (m *mockEngineState) GetMarket(string) (*lending.Market, error) {
	return m.market, nil
}

func (m *mockEngineState) PutMarket(_ string, market *lending.Market) error {
	m.market = market
	return nil
}

func (m *mockEngineState) GetUserAccount(_ string, addr crypto.Address) (*lending.UserAccount, error) {
	if account, ok := m.users[m.key(addr)]; ok {
		return account, nil
	}
	return nil, nil
}

func (m *mockEngineState) PutUserAccount(_ string, account *lending.UserAccount) error {
	if account == nil {
		return nil
	}
	m.users[m.key(account.Address)] = account
	return nil
}

func (m *mockEngineState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if account, ok := m.accounts[m.key(addr)]; ok {
		if account.BalanceNHB == nil {
			account.BalanceNHB = big.NewInt(0)
		}
		if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
		}
		return account, nil
	}
	return nil, errors.New("account not found")
}

func (m *mockEngineState) PutAccount(addr crypto.Address, account *types.Account) error {
	m.accounts[m.key(addr)] = account
	return nil
}

func (m *mockEngineState) GetFeeAccrual(string) (*lending.FeeAccrual, error) {
	return m.fees, nil
}

func (m *mockEngineState) PutFeeAccrual(_ string, fees *lending.FeeAccrual) error {
	m.fees = fees
	return nil
}

func makeAddress(prefix crypto.AddressPrefix, suffix byte) crypto.Address {
	raw := make([]byte, 20)
	raw[len(raw)-1] = suffix
	return crypto.MustNewAddress(prefix, raw)
}

func mustBig(value string) *big.Int {
	out, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid big int constant")
	}
	return out
}

func TestSupplyRoundingAttackBlocked(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x01)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x02)
	supplier := makeAddress(crypto.NHBPrefix, 0x03)

	engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
	engine.SetPoolID("default")

	state := newMockEngineState()
	ray := mustBig("1000000000000000000000000000")
	state.market = &lending.Market{
		PoolID:            "default",
		TotalNHBSupplied:  big.NewInt(1_000_000_000_000),
		TotalSupplyShares: big.NewInt(1_000_000_000_000),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       new(big.Int).Mul(ray, big.NewInt(10)),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: big.NewInt(10), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)

	if _, err := engine.Supply(supplier, big.NewInt(1)); err == nil {
		t.Fatalf("expected small deposit to be rejected when supply index is large")
	}

	if state.market.TotalNHBSupplied.Cmp(big.NewInt(1_000_000_000_000)) != 0 {
		t.Fatalf("expected supply totals to remain unchanged, got %s", state.market.TotalNHBSupplied)
	}
}

func TestBootstrapRequiresMinimumLiquidity(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x10)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x11)
	supplier := makeAddress(crypto.NHBPrefix, 0x12)

	engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
	engine.SetPoolID("default")

	state := newMockEngineState()
	ray := mustBig("1000000000000000000000000000")
	state.market = &lending.Market{
		PoolID:            "default",
		TotalNHBSupplied:  big.NewInt(0),
		TotalSupplyShares: big.NewInt(0),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       new(big.Int).Set(ray),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: mustBig("2000000000000000000"), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)

	small := mustBig("1000000000000000")
	if _, err := engine.Supply(supplier, small); err == nil {
		t.Fatalf("expected bootstrap deposit below minimum to fail")
	}

	min := mustBig("1000000000000000000")
	minted, err := engine.Supply(supplier, min)
	if err != nil {
		t.Fatalf("expected minimum bootstrap deposit to succeed: %v", err)
	}
	if minted.Cmp(min) != 0 {
		t.Fatalf("expected minted shares to equal deposit, got %s", minted)
	}
	if state.market.TotalSupplyShares.Cmp(min) != 0 {
		t.Fatalf("unexpected total shares: %s", state.market.TotalSupplyShares)
	}
}

func TestSupplyHalfUpRoundingAllowsDustyDeposits(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x21)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x22)
	supplier := makeAddress(crypto.NHBPrefix, 0x23)

	engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
	engine.SetPoolID("default")

	state := newMockEngineState()
	ray := mustBig("1000000000000000000000000000")
	supplyIndex := mustBig("1500000000000000000000000000")
	totalShares := mustBig("3000000000000000000000")
	totalSupplied := new(big.Int).Mul(totalShares, supplyIndex)
	totalSupplied.Quo(totalSupplied, ray)

	state.market = &lending.Market{
		PoolID:            "default",
		TotalNHBSupplied:  totalSupplied,
		TotalSupplyShares: new(big.Int).Set(totalShares),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       new(big.Int).Set(supplyIndex),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: new(big.Int).Set(totalSupplied), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: big.NewInt(10), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)

	minted, err := engine.Supply(supplier, big.NewInt(1))
	if err != nil {
		t.Fatalf("expected dusty deposit to succeed, got %v", err)
	}
	if minted.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("expected a single share minted, got %s", minted)
	}

	expectedSupply := new(big.Int).Add(totalSupplied, big.NewInt(1))
	if state.market.TotalNHBSupplied.Cmp(expectedSupply) != 0 {
		t.Fatalf("unexpected total supplied: got %s want %s", state.market.TotalNHBSupplied, expectedSupply)
	}

	expectedShares := new(big.Int).Add(totalShares, minted)
	if state.market.TotalSupplyShares.Cmp(expectedShares) != 0 {
		t.Fatalf("unexpected total shares: got %s want %s", state.market.TotalSupplyShares, expectedShares)
	}
}
