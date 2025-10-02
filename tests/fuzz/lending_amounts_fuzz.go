package fuzz

import (
	"math"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

var (
	lendingRay     = mustBig("1000000000000000000000000000")
	lendingHalfRay = new(big.Int).Rsh(lendingRay, 1)
)

type lendingFuzzState struct {
	market   *lending.Market
	users    map[string]*lending.UserAccount
	accounts map[string]*types.Account
	fees     *lending.FeeAccrual
}

func newLendingFuzzState() *lendingFuzzState {
	return &lendingFuzzState{
		users:    make(map[string]*lending.UserAccount),
		accounts: make(map[string]*types.Account),
	}
}

func (s *lendingFuzzState) key(addr crypto.Address) string {
	return string(addr.Bytes())
}

func (s *lendingFuzzState) GetMarket(string) (*lending.Market, error) { return s.market, nil }

func (s *lendingFuzzState) PutMarket(_ string, market *lending.Market) error {
	s.market = market
	return nil
}

func (s *lendingFuzzState) GetUserAccount(_ string, addr crypto.Address) (*lending.UserAccount, error) {
	if account, ok := s.users[s.key(addr)]; ok {
		return account, nil
	}
	return nil, nil
}

func (s *lendingFuzzState) PutUserAccount(_ string, account *lending.UserAccount) error {
	if account == nil {
		return nil
	}
	s.users[s.key(account.Address)] = account
	return nil
}

func (s *lendingFuzzState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if account, ok := s.accounts[s.key(addr)]; ok {
		if account.BalanceNHB == nil {
			account.BalanceNHB = big.NewInt(0)
		}
		if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
		}
		return account, nil
	}
	return nil, nil
}

func (s *lendingFuzzState) PutAccount(addr crypto.Address, account *types.Account) error {
	s.accounts[s.key(addr)] = account
	return nil
}

func (s *lendingFuzzState) GetFeeAccrual(string) (*lending.FeeAccrual, error) { return s.fees, nil }

func (s *lendingFuzzState) PutFeeAccrual(_ string, fees *lending.FeeAccrual) error {
	s.fees = fees
	return nil
}

func FuzzLendingSupplyWithdrawAmounts(f *testing.F) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x51)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x52)
	supplier := makeAddress(crypto.NHBPrefix, 0x53)

	f.Add(int64(1_000_000_000_000), int64(7), int64(3))
	f.Add(int64(42), int64(13), int64(17))

	f.Fuzz(func(t *testing.T, depositRaw int64, _ int64, indexScale int64) {
		amount := big.NewInt(absInt64(depositRaw)%1_000_000_000_000_000 + 1)
		scale := absInt64(indexScale)%50 + 1
		index := new(big.Int).Mul(lendingRay, big.NewInt(scale))
		initialShares := mustBig("1000000000000000000000")
		initialSupply := liquidityFromShares(initialShares, index)

		state := newLendingFuzzState()
		state.market = &lending.Market{
			PoolID:            "default",
			TotalNHBSupplied:  new(big.Int).Set(initialSupply),
			TotalSupplyShares: new(big.Int).Set(initialShares),
			TotalNHBBorrowed:  big.NewInt(0),
			SupplyIndex:       new(big.Int).Set(index),
			BorrowIndex:       new(big.Int).Set(lendingRay),
		}
		state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: new(big.Int).Set(initialSupply), BalanceZNHB: big.NewInt(0)}
		buffer := mustBig("100000000000000000000")
		supplierBalance := new(big.Int).Add(buffer, amount)
		state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: supplierBalance, BalanceZNHB: big.NewInt(0)}

		engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
		engine.SetPoolID("default")
		engine.SetState(state)

		minted, err := engine.Supply(supplier, amount)
		if err != nil {
			if state.market.TotalNHBSupplied.Cmp(initialSupply) != 0 {
				t.Fatalf("supply failure mutated liquidity: got %s want %s", state.market.TotalNHBSupplied, initialSupply)
			}
			return
		}
		if minted == nil || minted.Sign() <= 0 {
			t.Fatalf("minted shares must be positive")
		}

		expectedShares := new(big.Int).Add(initialShares, minted)
		if state.market.TotalSupplyShares.Cmp(expectedShares) != 0 {
			t.Fatalf("share tally mismatch: got %s want %s", state.market.TotalSupplyShares, expectedShares)
		}
		if state.market.TotalNHBSupplied.Sign() <= 0 {
			t.Fatalf("total supplied became non-positive")
		}
		moduleAcc, _ := state.GetAccount(moduleAddr)
		if moduleAcc == nil || moduleAcc.BalanceNHB.Sign() < 0 {
			t.Fatalf("module balance invalid")
		}
		userAccount, _ := state.GetUserAccount("default", supplier)
		if userAccount == nil || userAccount.SupplyShares.Cmp(minted) != 0 {
			t.Fatalf("user shares not tracked: got %v want %v", userAccount, minted)
		}

		redeemed, err := engine.Withdraw(supplier, minted)
		if err != nil {
			t.Fatalf("withdrawing freshly minted shares failed: %v", err)
		}
		if redeemed == nil || redeemed.Sign() <= 0 {
			t.Fatalf("redeemed amount must be positive")
		}
		if state.market.TotalSupplyShares.Cmp(initialShares) != 0 {
			t.Fatalf("total shares not restored: got %s want %s", state.market.TotalSupplyShares, initialShares)
		}
		if state.market.TotalNHBSupplied.Cmp(initialSupply) < 0 {
			t.Fatalf("total supplied underflow: got %s want >= %s", state.market.TotalNHBSupplied, initialSupply)
		}
		moduleAfter, _ := state.GetAccount(moduleAddr)
		if moduleAfter == nil || moduleAfter.BalanceNHB.Sign() < 0 {
			t.Fatalf("module balance negative after withdraw")
		}
		if moduleAfter.BalanceNHB.Cmp(state.market.TotalNHBSupplied) != 0 {
			t.Fatalf("module balance and market supply diverged: %s vs %s", moduleAfter.BalanceNHB, state.market.TotalNHBSupplied)
		}
		if userAfter, _ := state.GetUserAccount("default", supplier); userAfter != nil && userAfter.SupplyShares.Sign() != 0 {
			t.Fatalf("user shares not cleared after withdraw: %s", userAfter.SupplyShares)
		}
	})
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

func absInt64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}

func liquidityFromShares(shares, index *big.Int) *big.Int {
	if shares == nil || shares.Sign() <= 0 || index == nil || index.Sign() == 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Int).Mul(shares, index)
	scaled.Add(scaled, lendingHalfRay)
	scaled.Quo(scaled, lendingRay)
	return scaled
}
