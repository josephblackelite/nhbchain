package lending

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// lendingCollisionState mirrors the production state adapter: every read
// returns a fresh copy of the stored account and every write stores a copy, so
// two loads of one address are independent objects. The shared-pointer
// mockEngineState used by the other tests in this package hides exactly that.
type lendingCollisionState struct {
	*mockEngineState
}

func lendingCollisionCopy(acc *types.Account) *types.Account {
	out := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	if acc.BalanceNHB != nil {
		out.BalanceNHB.Set(acc.BalanceNHB)
	}
	if acc.BalanceZNHB != nil {
		out.BalanceZNHB.Set(acc.BalanceZNHB)
	}
	return out
}

func (s lendingCollisionState) GetAccount(addr crypto.Address) (*types.Account, error) {
	acc, err := s.mockEngineState.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	return lendingCollisionCopy(acc), nil
}

func (s lendingCollisionState) PutAccount(addr crypto.Address, acc *types.Account) error {
	return s.mockEngineState.PutAccount(addr, lendingCollisionCopy(acc))
}

// lendingCollisionPartitions returns every way n roles can be split into
// groups of roles that share one address, as role -> group index slices
// (restricted growth strings: [0 0 1] is "roles 0 and 1 share an address, role
// 2 has its own"). n=2 yields 2 partitions, n=3 yields 5, n=6 yields 203.
func lendingCollisionPartitions(n int) [][]int {
	var out [][]int
	var rec func(cur []int, max int)
	rec = func(cur []int, max int) {
		if len(cur) == n {
			out = append(out, append([]int(nil), cur...))
			return
		}
		for g := 0; g <= max+1; g++ {
			next := max
			if g > max {
				next = g
			}
			rec(append(cur, g), next)
		}
	}
	rec(nil, -1)
	return out
}

// lendingCollisionLedger funds one address per group and tracks the balance
// each address is expected to hold after the modelled movements.
type lendingCollisionLedger struct {
	state    lendingCollisionState
	addrs    map[int]crypto.Address
	initial  map[string][2]*big.Int
	expected map[string][2]*big.Int
}

func newLendingCollisionLedger(roles []int) *lendingCollisionLedger {
	l := &lendingCollisionLedger{
		state:    lendingCollisionState{newMockEngineState()},
		addrs:    map[int]crypto.Address{},
		initial:  map[string][2]*big.Int{},
		expected: map[string][2]*big.Int{},
	}
	for _, group := range roles {
		if _, ok := l.addrs[group]; ok {
			continue
		}
		addr := makeAddress(crypto.NHBPrefix, byte(0x60+group))
		l.addrs[group] = addr
		nhb := big.NewInt(1_000_000 + int64(group)*1_000)
		znhb := big.NewInt(500_000 + int64(group)*1_000)
		key := l.state.key(addr)
		l.state.accounts[key] = &types.Account{BalanceNHB: new(big.Int).Set(nhb), BalanceZNHB: new(big.Int).Set(znhb)}
		l.initial[key] = [2]*big.Int{new(big.Int).Set(nhb), new(big.Int).Set(znhb)}
		l.expected[key] = [2]*big.Int{new(big.Int).Set(nhb), new(big.Int).Set(znhb)}
	}
	return l
}

func (l *lendingCollisionLedger) at(roles []int, role int) crypto.Address {
	return l.addrs[roles[role]]
}

func (l *lendingCollisionLedger) move(addr crypto.Address, nhb, znhb *big.Int) {
	b := l.expected[l.state.key(addr)]
	if nhb != nil {
		b[0].Add(b[0], nhb)
	}
	if znhb != nil {
		b[1].Add(b[1], znhb)
	}
}

// assertMatchesModel checks every address against its expected balances and
// the total of both assets against the starting total.
func (l *lendingCollisionLedger) assertMatchesModel(t *testing.T) {
	t.Helper()
	totals := [2]*big.Int{new(big.Int), new(big.Int)}
	starts := [2]*big.Int{new(big.Int), new(big.Int)}
	for _, addr := range l.addrs {
		key := l.state.key(addr)
		got := l.state.accounts[key]
		want := l.expected[key]
		if got.BalanceNHB.Cmp(want[0]) != 0 || got.BalanceZNHB.Cmp(want[1]) != 0 {
			t.Errorf("address %x balances NHB=%s ZNHB=%s, want NHB=%s ZNHB=%s", addr.Bytes()[19], got.BalanceNHB, got.BalanceZNHB, want[0], want[1])
		}
		totals[0].Add(totals[0], got.BalanceNHB)
		totals[1].Add(totals[1], got.BalanceZNHB)
		starts[0].Add(starts[0], l.initial[key][0])
		starts[1].Add(starts[1], l.initial[key][1])
	}
	if totals[0].Cmp(starts[0]) != 0 {
		t.Errorf("total NHB changed from %s to %s", starts[0], totals[0])
	}
	if totals[1].Cmp(starts[1]) != 0 {
		t.Errorf("total ZNHB changed from %s to %s", starts[1], totals[1])
	}
}

// A liquidation moves the repaid NHB from the liquidator to the module and the
// seized ZNHB from the collateral vault to the liquidator, the developer target
// and the protocol target. Whichever of those six roles share an address (all
// 203 combinations), each delta must land on the one account and the totals of
// both assets must not change. A liquidator that is the borrower is refused
// outright: the borrower's stale copy used to overwrite the liquidator's debit
// and credit while the module still received the repayment.
func TestLiquidateRoleCollisionsConserveValue(t *testing.T) {
	// roles: 0 liquidator, 1 borrower, 2 module, 3 collateral vault,
	// 4 developer target, 5 protocol target.
	for _, roles := range lendingCollisionPartitions(6) {
		roles := roles
		t.Run(fmt.Sprint(roles), func(t *testing.T) {
			ledger := newLendingCollisionLedger(roles)
			liquidator, borrower := ledger.at(roles, 0), ledger.at(roles, 1)
			moduleAddr, vaultAddr := ledger.at(roles, 2), ledger.at(roles, 3)
			developer, protocol := ledger.at(roles, 4), ledger.at(roles, 5)

			engine := NewEngine(moduleAddr, vaultAddr, RiskParameters{LiquidationThreshold: 7500, LiquidationBonus: 1000})
			engine.SetCollateralRouting(CollateralRouting{
				LiquidatorBps:   7000,
				DeveloperBps:    2000,
				DeveloperTarget: developer,
				ProtocolBps:     1000,
				ProtocolTarget:  protocol,
			})
			engine.SetPoolID("default")
			ledger.state.market = &Market{
				PoolID:           "default",
				TotalNHBSupplied: big.NewInt(10_000),
				TotalNHBBorrowed: big.NewInt(800),
				SupplyIndex:      new(big.Int).Set(ray),
				BorrowIndex:      new(big.Int).Set(ray),
			}
			ledger.state.users[ledger.state.key(borrower)] = &UserAccount{
				Address:        borrower,
				CollateralZNHB: big.NewInt(1_000),
				DebtNHB:        big.NewInt(800),
				ScaledDebt:     big.NewInt(800),
			}
			engine.SetState(ledger.state)

			repaid, seized, err := engine.Liquidate(liquidator, borrower)
			if roles[0] == roles[1] {
				if !errors.Is(err, ErrSelfLiquidation) {
					t.Fatalf("a borrower liquidating its own position must be refused with ErrSelfLiquidation, got %v", err)
				}
				// The refusal must leave everything exactly as it was.
				ledger.assertMatchesModel(t)
				user := ledger.state.users[ledger.state.key(borrower)]
				if user.DebtNHB.Cmp(big.NewInt(800)) != 0 || user.CollateralZNHB.Cmp(big.NewInt(1_000)) != 0 {
					t.Fatalf("a refused self-liquidation must not touch the position: debt=%s collateral=%s", user.DebtNHB, user.CollateralZNHB)
				}
				return
			}
			if err != nil {
				t.Fatalf("liquidate: %v", err)
			}
			if repaid.Cmp(big.NewInt(800)) != 0 || seized.Sign() <= 0 {
				t.Fatalf("unexpected liquidation result: repaid=%s seized=%s", repaid, seized)
			}

			developerShare := new(big.Int).Quo(new(big.Int).Mul(seized, big.NewInt(2000)), big.NewInt(10_000))
			protocolShare := new(big.Int).Quo(new(big.Int).Mul(seized, big.NewInt(1000)), big.NewInt(10_000))
			liquidatorShare := new(big.Int).Sub(new(big.Int).Sub(new(big.Int).Set(seized), developerShare), protocolShare)
			ledger.move(liquidator, new(big.Int).Neg(repaid), liquidatorShare)
			ledger.move(moduleAddr, repaid, nil)
			ledger.move(vaultAddr, nil, new(big.Int).Neg(seized))
			ledger.move(developer, nil, developerShare)
			ledger.move(protocol, nil, protocolShare)
			ledger.assertMatchesModel(t)

			user := ledger.state.users[ledger.state.key(borrower)]
			if user.DebtNHB.Sign() != 0 || user.CollateralZNHB.Cmp(new(big.Int).Sub(big.NewInt(1_000), seized)) != 0 {
				t.Fatalf("unexpected position after liquidation: debt=%s collateral=%s", user.DebtNHB, user.CollateralZNHB)
			}
		})
	}
}

// A borrow pays the amount to the borrower and the developer fee to the fee
// recipient out of the module account. Whichever of those three roles share an
// address, each delta must land on the one account and no NHB may be created or
// destroyed.
func TestBorrowRoleCollisionsConserveValue(t *testing.T) {
	// roles: 0 borrower, 1 fee recipient, 2 module.
	for _, roles := range lendingCollisionPartitions(3) {
		roles := roles
		t.Run(fmt.Sprint(roles), func(t *testing.T) {
			ledger := newLendingCollisionLedger(roles)
			borrower, feeRecipient, moduleAddr := ledger.at(roles, 0), ledger.at(roles, 1), ledger.at(roles, 2)
			vaultAddr := makeAddress(crypto.ZNHBPrefix, 0x11)
			ledger.state.accounts[ledger.state.key(vaultAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

			engine := NewEngine(moduleAddr, vaultAddr, RiskParameters{MaxLTV: 9000, LiquidationThreshold: 9500, DeveloperFeeCapBps: 500})
			engine.SetPoolID("default")
			ledger.state.market = &Market{
				PoolID:           "default",
				TotalNHBSupplied: big.NewInt(1_000_000),
				TotalNHBBorrowed: big.NewInt(0),
				SupplyIndex:      new(big.Int).Set(ray),
				BorrowIndex:      new(big.Int).Set(ray),
			}
			ledger.state.users[ledger.state.key(borrower)] = &UserAccount{
				Address:        borrower,
				CollateralZNHB: big.NewInt(1_000),
				DebtNHB:        big.NewInt(0),
				ScaledDebt:     big.NewInt(0),
			}
			engine.SetState(ledger.state)

			fee, err := engine.Borrow(borrower, big.NewInt(500), feeRecipient, 100)
			if err != nil {
				t.Fatalf("borrow: %v", err)
			}
			if fee.Cmp(big.NewInt(5)) != 0 {
				t.Fatalf("unexpected fee %s", fee)
			}
			ledger.move(moduleAddr, big.NewInt(-505), nil)
			ledger.move(borrower, big.NewInt(500), nil)
			ledger.move(feeRecipient, big.NewInt(5), nil)
			ledger.assertMatchesModel(t)
		})
	}
}

// Withdrawing accrued fees moves them from the module account to the
// recipient; a recipient that is the module account itself must be a no-op.
func TestWithdrawFeesRoleCollisionsConserveValue(t *testing.T) {
	// roles: 0 module, 1 recipient.
	for _, roles := range lendingCollisionPartitions(2) {
		roles := roles
		t.Run(fmt.Sprint(roles), func(t *testing.T) {
			ledger := newLendingCollisionLedger(roles)
			moduleAddr, recipient := ledger.at(roles, 0), ledger.at(roles, 1)
			vaultAddr := makeAddress(crypto.ZNHBPrefix, 0x11)

			engine := NewEngine(moduleAddr, vaultAddr, RiskParameters{})
			engine.SetPoolID("default")
			ledger.state.market = &Market{
				PoolID:            "default",
				TotalNHBSupplied:  big.NewInt(10_000),
				TotalSupplyShares: big.NewInt(10_000),
				TotalNHBBorrowed:  big.NewInt(0),
				SupplyIndex:       new(big.Int).Set(ray),
				BorrowIndex:       new(big.Int).Set(ray),
			}
			ledger.state.fees = &FeeAccrual{ProtocolFeesWei: big.NewInt(100), DeveloperFeesWei: big.NewInt(100)}
			engine.SetState(ledger.state)

			if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(50)); err != nil {
				t.Fatalf("withdraw protocol fees: %v", err)
			}
			ledger.move(moduleAddr, big.NewInt(-50), nil)
			ledger.move(recipient, big.NewInt(50), nil)
			ledger.assertMatchesModel(t)

			if _, err := engine.WithdrawDeveloperFees(recipient, big.NewInt(30)); err != nil {
				t.Fatalf("withdraw developer fees: %v", err)
			}
			ledger.move(moduleAddr, big.NewInt(-30), nil)
			ledger.move(recipient, big.NewInt(30), nil)
			ledger.assertMatchesModel(t)
		})
	}
}
