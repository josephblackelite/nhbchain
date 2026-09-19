package market

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// copyingMarketState is mockMarketState with the property of the real state
// that mockMarketState's shared pointers hide: every load returns a fresh
// copy of the account and every write stores a copy. Two loads of one address
// are then two independent objects, so an engine that loads a role twice and
// writes both back overwrites its own changes.
type copyingMarketState struct {
	*mockMarketState
}

func copyAccount(acc *types.Account) *types.Account {
	return &types.Account{
		BalanceNHB:  new(big.Int).Set(acc.BalanceNHB),
		BalanceZNHB: new(big.Int).Set(acc.BalanceZNHB),
	}
}

func (c copyingMarketState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if acc, ok := c.accounts[c.key(addr)]; ok {
		return copyAccount(acc), nil
	}
	return nil, nil
}

func (c copyingMarketState) PutAccount(addr crypto.Address, account *types.Account) error {
	c.accounts[c.key(addr)] = copyAccount(account)
	return nil
}

// aliasedRoles are the accounts one fill involves: the buyer, the seller and
// the fee collector, each named by an index; roles with the same index are
// one address.
type aliasedRoles struct{ buyer, seller, collector int }

func (r aliasedRoles) String() string {
	return fmt.Sprintf("buyer=%d seller=%d feeCollector=%d", r.buyer, r.seller, r.collector)
}

// everyAliasing is every way three roles can share addresses.
var everyAliasing = []aliasedRoles{
	{0, 1, 2}, // all distinct
	{0, 0, 1}, // buyer fills its own listing
	{0, 1, 0}, // buyer is the fee collector
	{0, 1, 1}, // seller is the fee collector
	{0, 0, 0}, // one account in every role
}

func newAliasedHarness(roles aliasedRoles) (*Engine, *copyingMarketState, map[int]crypto.Address) {
	accounts := map[int]crypto.Address{
		0: makeTestAddress(crypto.NHBPrefix, 0x01),
		1: makeTestAddress(crypto.NHBPrefix, 0x02),
		2: makeTestAddress(crypto.NHBPrefix, 0x03),
	}
	escrowAddr := makeTestAddress(crypto.ZNHBPrefix, 0xE0)
	engine := NewEngine(escrowAddr, accounts[roles.collector])
	state := &copyingMarketState{newMockMarketState()}
	engine.SetState(state)
	engine.SetNowFunc(func() int64 { return 1_700_000_000 })
	state.accounts[state.key(escrowAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	for _, addr := range accounts {
		state.accounts[state.key(addr)] = &types.Account{BalanceNHB: big.NewInt(10_000), BalanceZNHB: big.NewInt(10_000)}
	}
	return engine, state, accounts
}

// totals sums both assets over every account in the state, escrow included.
func (c *copyingMarketState) totals() (nhb, znhb *big.Int) {
	nhb, znhb = new(big.Int), new(big.Int)
	for _, acc := range c.accounts {
		nhb.Add(nhb, acc.BalanceNHB)
		znhb.Add(znhb, acc.BalanceZNHB)
	}
	return nhb, znhb
}

// snapshot records both balances of every account in the state.
func (c *copyingMarketState) snapshot() map[string][2]string {
	out := make(map[string][2]string, len(c.accounts))
	for k, acc := range c.accounts {
		out[k] = [2]string{acc.BalanceNHB.String(), acc.BalanceZNHB.String()}
	}
	return out
}

// TestFillListingRolesConserveBalances fills a listing with the buyer, the
// seller and the fee collector assigned to addresses every possible way. The
// fill must move exactly the cost from buyer to seller, the flat fee from
// buyer to collector and the listed ZNHB from escrow to buyer, whichever of
// those are one address -- so total NHB and total ZNHB never change, and each
// address ends where its roles put it. A buyer filling its own listing had the
// seller's write overwrite the buyer's: the cost was paid to itself but the
// debit vanished (NHB minted) and the escrowed ZNHB it bought was lost. That
// aliasing is now refused outright with ErrSelfFill, and the refusal must
// leave every balance as it was.
func TestFillListingRolesConserveBalances(t *testing.T) {
	const (
		listed   = 100
		cost     = 200 // listed at 1 ZNHB per 2 NHB
		flatFee  = 7
		startBal = 10_000
	)
	for _, roles := range everyAliasing {
		t.Run(roles.String(), func(t *testing.T) {
			engine, state, accounts := newAliasedHarness(roles)
			buyer, seller := accounts[roles.buyer], accounts[roles.seller]
			nhbBefore, znhbBefore := state.totals()

			listing, err := engine.CreateListing(seller, big.NewInt(listed), big.NewInt(1), big.NewInt(2), true)
			if err != nil {
				t.Fatalf("create listing: %v", err)
			}
			if roles.buyer == roles.seller {
				// A buyer filling its own listing is refused (ErrSelfFill)
				// before any account is loaded, so that aliasing never
				// reaches the ledger and the refusal changes nothing.
				before := state.snapshot()
				_, err := engine.FillListing(buyer, listing.ID, big.NewInt(listed), big.NewInt(flatFee), [32]byte{1})
				if !errors.Is(err, ErrSelfFill) {
					t.Fatalf("fill of one's own listing = %v, want ErrSelfFill", err)
				}
				if after := state.snapshot(); !reflect.DeepEqual(before, after) {
					t.Fatalf("a refused self-fill changed balances: %v -> %v", before, after)
				}
				return
			}
			if _, err := engine.FillListing(buyer, listing.ID, big.NewInt(listed), big.NewInt(flatFee), [32]byte{1}); err != nil {
				t.Fatalf("fill listing: %v", err)
			}

			nhbAfter, znhbAfter := state.totals()
			if nhbAfter.Cmp(nhbBefore) != 0 {
				t.Fatalf("total NHB changed from %s to %s", nhbBefore, nhbAfter)
			}
			if znhbAfter.Cmp(znhbBefore) != 0 {
				t.Fatalf("total ZNHB changed from %s to %s", znhbBefore, znhbAfter)
			}

			wantNHB := map[int]int64{}
			wantZNHB := map[int]int64{}
			for i := range accounts {
				wantNHB[i], wantZNHB[i] = startBal, startBal
			}
			wantNHB[roles.buyer] -= cost + flatFee
			wantNHB[roles.seller] += cost
			wantNHB[roles.collector] += flatFee
			wantZNHB[roles.seller] -= listed // escrowed at listing
			wantZNHB[roles.buyer] += listed  // paid out of escrow
			for i, addr := range accounts {
				acc := state.accounts[state.key(addr)]
				if acc.BalanceNHB.Cmp(big.NewInt(wantNHB[i])) != 0 || acc.BalanceZNHB.Cmp(big.NewInt(wantZNHB[i])) != 0 {
					t.Fatalf("account %d holds NHB %s / ZNHB %s, want %d / %d", i, acc.BalanceNHB, acc.BalanceZNHB, wantNHB[i], wantZNHB[i])
				}
			}
		})
	}
}

// TestCancelListingReturnsEscrowToSeller cancels an open listing through a
// state that hands out separate copies of an account, as the real one does.
func TestCancelListingReturnsEscrowToSeller(t *testing.T) {
	engine, state, accounts := newAliasedHarness(aliasedRoles{0, 1, 2})
	seller := accounts[1]
	listing, err := engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if err := engine.CancelListing(seller, listing.ID); err != nil {
		t.Fatalf("cancel listing: %v", err)
	}
	acc := state.accounts[state.key(seller)]
	if acc.BalanceZNHB.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("seller ZNHB after cancelling = %s, want the 10000 it started with", acc.BalanceZNHB)
	}
	escrow := state.accounts[state.key(engine.marketEscrowAddress)]
	if escrow.BalanceZNHB.Sign() != 0 {
		t.Fatalf("escrow still holds %s ZNHB after the only listing was cancelled", escrow.BalanceZNHB)
	}
}
