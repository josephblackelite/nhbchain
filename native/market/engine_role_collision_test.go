package market

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// roleCollisionState mirrors the production state adapter: every read returns
// a fresh copy of the stored account and every write stores a copy, so two
// loads of one address are independent objects. The shared-pointer
// mockMarketState used by the other tests in this package hides exactly that:
// a buyer and a seller that are one address mutate one object there.
type roleCollisionState struct {
	*mockMarketState
}

func roleCollisionCopy(acc *types.Account) *types.Account {
	return &types.Account{
		BalanceNHB:  new(big.Int).Set(acc.BalanceNHB),
		BalanceZNHB: new(big.Int).Set(acc.BalanceZNHB),
	}
}

func (s roleCollisionState) GetAccount(addr crypto.Address) (*types.Account, error) {
	acc, err := s.mockMarketState.GetAccount(addr)
	if err != nil || acc == nil {
		return acc, err
	}
	return roleCollisionCopy(acc), nil
}

func (s roleCollisionState) PutAccount(addr crypto.Address, acc *types.Account) error {
	return s.mockMarketState.PutAccount(addr, roleCollisionCopy(acc))
}

// roleCollisionPartitions returns every way n roles can be split into groups
// of roles that share one address, as role -> group index slices (restricted
// growth strings: [0 0 1] is "roles 0 and 1 share an address, role 2 has its
// own"). n=2 yields 2 partitions, n=4 yields 15.
func roleCollisionPartitions(n int) [][]int {
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

type roleCollisionBalance struct{ nhb, znhb *big.Int }

type roleCollisionFixture struct {
	engine  *Engine
	state   roleCollisionState
	addrs   map[int]crypto.Address // group -> address
	initial map[string]roleCollisionBalance
	// expected accumulates the analytically expected balance per address.
	expected map[string]roleCollisionBalance
}

// newRoleCollisionFixture builds an engine whose seller, buyer, fee collector
// and escrow account are assigned to addresses according to roles (buyer,
// seller, fee collector, escrow -> group), and funds every distinct address.
func newRoleCollisionFixture(roles []int) *roleCollisionFixture {
	f := &roleCollisionFixture{
		addrs:    map[int]crypto.Address{},
		initial:  map[string]roleCollisionBalance{},
		expected: map[string]roleCollisionBalance{},
	}
	for _, group := range roles {
		if _, ok := f.addrs[group]; !ok {
			f.addrs[group] = makeTestAddress(crypto.NHBPrefix, byte(0x40+group))
		}
	}
	f.state = roleCollisionState{newMockMarketState()}
	f.engine = NewEngine(f.roleAddr(roles, 3), f.roleAddr(roles, 2))
	f.engine.SetState(f.state)
	for group, addr := range f.addrs {
		nhb := big.NewInt(50_000_000 + int64(group)*1_000)
		znhb := big.NewInt(9_000 + int64(group)*100)
		f.state.accounts[f.state.key(addr)] = &types.Account{BalanceNHB: new(big.Int).Set(nhb), BalanceZNHB: new(big.Int).Set(znhb)}
		f.initial[f.state.key(addr)] = roleCollisionBalance{nhb: new(big.Int).Set(nhb), znhb: new(big.Int).Set(znhb)}
		f.expected[f.state.key(addr)] = roleCollisionBalance{nhb: new(big.Int).Set(nhb), znhb: new(big.Int).Set(znhb)}
	}
	return f
}

func (f *roleCollisionFixture) roleAddr(roles []int, role int) crypto.Address {
	return f.addrs[roles[role]]
}

func (f *roleCollisionFixture) move(addr crypto.Address, nhb, znhb int64) {
	b := f.expected[f.state.key(addr)]
	b.nhb.Add(b.nhb, big.NewInt(nhb))
	b.znhb.Add(b.znhb, big.NewInt(znhb))
}

// assertMatchesModel checks every address against the expected balance and
// the total of both assets across all addresses against the starting total.
func (f *roleCollisionFixture) assertMatchesModel(t *testing.T, step string) {
	t.Helper()
	totalNHB, totalZNHB := new(big.Int), new(big.Int)
	startNHB, startZNHB := new(big.Int), new(big.Int)
	for _, addr := range f.addrs {
		key := f.state.key(addr)
		got := f.state.accounts[key]
		want := f.expected[key]
		if got.BalanceNHB.Cmp(want.nhb) != 0 || got.BalanceZNHB.Cmp(want.znhb) != 0 {
			t.Errorf("%s: address %x balances NHB=%s ZNHB=%s, want NHB=%s ZNHB=%s", step, addr.Bytes()[19], got.BalanceNHB, got.BalanceZNHB, want.nhb, want.znhb)
		}
		totalNHB.Add(totalNHB, got.BalanceNHB)
		totalZNHB.Add(totalZNHB, got.BalanceZNHB)
		startNHB.Add(startNHB, f.initial[key].nhb)
		startZNHB.Add(startZNHB, f.initial[key].znhb)
	}
	if totalNHB.Cmp(startNHB) != 0 {
		t.Errorf("%s: total NHB changed from %s to %s", step, startNHB, totalNHB)
	}
	if totalZNHB.Cmp(startZNHB) != 0 {
		t.Errorf("%s: total ZNHB changed from %s to %s", step, startZNHB, totalZNHB)
	}
}

// Whatever addresses the buyer, seller, fee collector and escrow account
// resolve to, creating, partly filling and cancelling a listing must move
// exactly the modelled amounts and leave the total of both assets across the
// touched addresses unchanged (the flat fee lands on the fee collector, so it
// stays inside the total). A buyer that is the listing's own seller is refused
// instead. Before each address was loaded once, the seller-side credit
// overwrote the buyer-side debit, so a self-fill created the cost and destroyed
// the escrowed ZNHB.
func TestRoleCollisionsConserveValue(t *testing.T) {
	const (
		listed = 1_000
		fill   = 400
		cost   = 600 // ceil(400 * 3 / 2) at 2 ZNHB per 3 NHB
		fee    = 100
	)
	// roles: 0 buyer, 1 seller, 2 fee collector, 3 escrow account.
	for _, roles := range roleCollisionPartitions(4) {
		roles := roles
		t.Run(fmt.Sprintf("buyer=%d seller=%d fee=%d escrow=%d", roles[0], roles[1], roles[2], roles[3]), func(t *testing.T) {
			f := newRoleCollisionFixture(roles)
			buyer, seller, feeCollector, escrow := f.roleAddr(roles, 0), f.roleAddr(roles, 1), f.roleAddr(roles, 2), f.roleAddr(roles, 3)

			listing, err := f.engine.CreateListing(seller, big.NewInt(listed), big.NewInt(2), big.NewInt(3), true)
			if err != nil {
				t.Fatalf("create listing: %v", err)
			}
			f.move(seller, 0, -listed)
			f.move(escrow, 0, listed)
			f.assertMatchesModel(t, "after create")

			_, err = f.engine.FillListing(buyer, listing.ID, big.NewInt(fill), big.NewInt(fee), [32]byte{})
			if roles[0] == roles[1] {
				if !errors.Is(err, ErrSelfFill) {
					t.Fatalf("a buyer filling its own listing must be refused with ErrSelfFill, got %v", err)
				}
				// The refusal must leave everything exactly as it was.
				f.assertMatchesModel(t, "after refused self-fill")
				stored, _ := f.state.GetListing(listing.ID)
				if stored.RemainingAmount.Cmp(big.NewInt(listed)) != 0 || stored.Status != ListingOpen {
					t.Fatalf("a refused self-fill must not change the listing: %+v", stored)
				}
			} else {
				if err != nil {
					t.Fatalf("fill: %v", err)
				}
				f.move(buyer, -(cost + fee), fill)
				f.move(seller, cost, 0)
				f.move(feeCollector, fee, 0)
				f.move(escrow, 0, -fill)
				f.assertMatchesModel(t, "after fill")
			}

			remaining := int64(listed)
			if roles[0] != roles[1] {
				remaining -= fill
			}
			if err := f.engine.CancelListing(seller, listing.ID); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			f.move(seller, 0, remaining)
			f.move(escrow, 0, -remaining)
			f.assertMatchesModel(t, "after cancel")
		})
	}
}

// The reported case in isolation: an address with NHB and one wei of ZNHB
// lists that wei at a price close to its whole NHB balance and fills its own
// listing. The fill is refused and the balances do not move.
func TestFillListingRefusesSelfFill(t *testing.T) {
	escrowAddr := makeTestAddress(crypto.ZNHBPrefix, 0xE0)
	feeAddr := makeTestAddress(crypto.NHBPrefix, 0xFE)
	seller := makeTestAddress(crypto.NHBPrefix, 0x71)
	engine := NewEngine(escrowAddr, feeAddr)
	state := roleCollisionState{newMockMarketState()}
	engine.SetState(state)
	state.accounts[state.key(escrowAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(feeAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	nhb := big.NewInt(1_000_000_000)
	state.accounts[state.key(seller)] = &types.Account{BalanceNHB: new(big.Int).Set(nhb), BalanceZNHB: big.NewInt(1)}

	// 1 wei ZNHB at a rate of 1/(balance-fee) ZNHB per NHB: cost = balance - fee.
	fee := big.NewInt(100_000_000)
	rateDenominator := new(big.Int).Sub(nhb, fee)
	listing, err := engine.CreateListing(seller, big.NewInt(1), big.NewInt(1), rateDenominator, false)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if _, err := engine.FillListing(seller, listing.ID, big.NewInt(1), fee, [32]byte{}); !errors.Is(err, ErrSelfFill) {
		t.Fatalf("expected ErrSelfFill, got %v", err)
	}
	got := state.accounts[state.key(seller)]
	if got.BalanceNHB.Cmp(nhb) != 0 || got.BalanceZNHB.Sign() != 0 {
		t.Fatalf("seller balances moved: NHB=%s ZNHB=%s", got.BalanceNHB, got.BalanceZNHB)
	}
	if escrow := state.accounts[state.key(escrowAddr)]; escrow.BalanceZNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("the escrowed ZNHB must stay in escrow, got %s", escrow.BalanceZNHB)
	}
	if collector := state.accounts[state.key(feeAddr)]; collector.BalanceNHB.Sign() != 0 {
		t.Fatalf("no fee may be collected on a refused fill, got %s", collector.BalanceNHB)
	}
}
