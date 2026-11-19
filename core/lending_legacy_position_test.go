package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// PL-EXT-16: a lending position recorded on a plain account (collateral, supply
// shares, debt) was copied into EVERY pool that touched the account and the plain
// account was written back unchanged, so a second pool, which an ordinary signed
// transaction can create, held the same collateral again and paid it out again
// from the shared collateral vault. From the accounting-fixes height only the
// default pool, the one that existed when those fields were written, takes legacy
// positions; and a position that is taken is cleared from the plain account.

const legacyPositionActivation = 100

func legacyPositionProcessor(t *testing.T, gate uint64) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	sp.SetLendingAccountingFixesActivationHeight(gate)
	return sp
}

func plainLendingFields(t *testing.T, sp *StateProcessor, addr [20]byte) (collateral, shares, debt *big.Int) {
	t.Helper()
	acc, err := nhbstate.NewManager(sp.Trie).GetAccount(addr[:])
	if err != nil {
		t.Fatalf("load plain account: %v", err)
	}
	return acc.CollateralBalance, acc.SupplyShares, acc.DebtPrincipal
}

// touch builds an engine for a pool in a block at the given height, which is what
// the first lending transaction naming the pool does.
func touchPool(t *testing.T, sp *StateProcessor, poolID string, height uint64) {
	t.Helper()
	sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
	defer sp.EndBlock()
	if _, _, err := sp.lendingEngine(poolID); err != nil {
		t.Fatalf("engine for pool %q at height %d: %v", poolID, height, err)
	}
}

func TestLegacyLendingPositionIsTakenByTheDefaultPoolOnlyFromTheAccountingFixesHeight(t *testing.T) {
	cases := []struct {
		name   string
		gate   uint64
		height uint64
		fixed  bool
	}{
		{"DefaultOffCopiesIntoEveryPool", 0, 10_000_000, false},
		{"BelowActivationCopiesIntoEveryPool", legacyPositionActivation, legacyPositionActivation - 1, false},
		{"AtActivationDefaultPoolOnly", legacyPositionActivation, legacyPositionActivation, true},
		{"AboveActivationDefaultPoolOnly", legacyPositionActivation, legacyPositionActivation + 40, true},
	}
	owner := [20]byte{0xC1}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := legacyPositionProcessor(t, tc.gate)
			seedLegacyLendingPosition(t, sp, owner, 250)
			// A stranger's pool is the first to be used.
			touchPool(t, sp, "attacker-pool", tc.height)
			touchPool(t, sp, "default", tc.height)

			inOther := lendingUserExists(t, sp, "attacker-pool", owner)
			inDefault := lendingUserExists(t, sp, "default", owner)
			collateral, shares, debt := plainLendingFields(t, sp, owner)
			if tc.fixed {
				if inOther {
					t.Fatalf("a pool other than the default took the legacy position")
				}
				if !inDefault {
					t.Fatalf("the default pool did not take the legacy position")
				}
				if collateral.Sign() != 0 || shares.Sign() != 0 || debt.Sign() != 0 {
					t.Fatalf("the plain account still holds the position after it was taken: %s %s %s", collateral, shares, debt)
				}
				if done, err := nhbstate.NewManager(sp.Trie).LendingLegacyReconciled("attacker-pool"); err != nil || done {
					t.Fatalf("a pool that takes no legacy positions was marked as swept: done=%v err=%v", done, err)
				}
				return
			}
			if !inOther || !inDefault {
				t.Fatalf("the old behaviour copies the position into every pool, got other=%v default=%v", inOther, inDefault)
			}
			if collateral.Cmp(big.NewInt(5_000)) != 0 {
				t.Fatalf("the old behaviour leaves the plain account unchanged, collateral now %s", collateral)
			}
		})
	}
}

// The consequence, through the engine: the same collateral withdrawn from two
// pools is paid twice by the shared vault before the height, once after it.
func TestLegacyCollateralCannotBeWithdrawnFromTwoPoolsFromTheAccountingFixesHeight(t *testing.T) {
	owner := [20]byte{0xC2}
	for _, tc := range []struct {
		name   string
		gate   uint64
		height uint64
		twice  bool
	}{
		{"BeforeTheHeightTheVaultPaysTwice", legacyPositionActivation, legacyPositionActivation - 1, true},
		{"FromTheHeightItPaysOnce", legacyPositionActivation, legacyPositionActivation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := legacyPositionProcessor(t, tc.gate)
			seedLegacyLendingPosition(t, sp, owner, 0)
			vault := sp.lendingCollateralAddr
			if err := sp.setAccount(vault.Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(100_000)}); err != nil {
				t.Fatalf("fund vault: %v", err)
			}
			sp.BeginBlock(tc.height, time.Unix(int64(tc.height), 0).UTC())
			defer sp.EndBlock()
			who := crypto.MustNewAddress(crypto.NHBPrefix, owner[:])

			first, _, err := sp.lendingEngine("pool-a")
			if err != nil {
				t.Fatal(err)
			}
			errA := first.WithdrawCollateral(who, big.NewInt(5_000))
			second, _, err := sp.lendingEngine("default")
			if err != nil {
				t.Fatal(err)
			}
			errB := second.WithdrawCollateral(who, big.NewInt(5_000))

			paid := 0
			for _, e := range []error{errA, errB} {
				if e == nil {
					paid++
				}
			}
			want := 1
			if tc.twice {
				want = 2
			} else if errA == nil || errB != nil {
				// Whichever pool is used first, only the default pool holds the position.
				t.Fatalf("from the height the position belongs to the default pool: the stranger's pool returned %v, the default pool returned %v", errA, errB)
			}
			if paid != want {
				t.Fatalf("5,000 of legacy collateral was withdrawn %d time(s) from two pools (errors: %v, %v), want %d", paid, errA, errB, want)
			}
			acc, err := nhbstate.NewManager(sp.Trie).GetAccount(owner[:])
			if err != nil {
				t.Fatal(err)
			}
			if got, wantZ := acc.BalanceZNHB, big.NewInt(int64(5_000*want)); got.Cmp(wantZ) != 0 {
				t.Fatalf("the owner holds %s ZNHB after the withdrawals, want %s", got, wantZ)
			}
		})
	}
}

// The default pool still takes a legacy position once, for the owner who has
// always been in it: nothing about a position already in the pool changes.
func TestLegacyLendingPositionIsStillTakenByTheDefaultPoolAfterTheHeight(t *testing.T) {
	sp := legacyPositionProcessor(t, legacyPositionActivation)
	owner := [20]byte{0xC3}
	seedLegacyLendingPosition(t, sp, owner, 250)
	touchPool(t, sp, "default", legacyPositionActivation+1)
	user, ok, err := nhbstate.NewManager(sp.Trie).LendingGetUserAccount("default", owner)
	if err != nil || !ok {
		t.Fatalf("the default pool did not take the position: ok=%v err=%v", ok, err)
	}
	if user.CollateralZNHB.Cmp(big.NewInt(5_000)) != 0 || user.SupplyShares.Cmp(big.NewInt(1_000)) != 0 || user.DebtNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("the default pool holds collateral %s, shares %s, debt %s, want 5000, 1000, 250", user.CollateralZNHB, user.SupplyShares, user.DebtNHB)
	}
	market, ok, err := nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("default market: ok=%v err=%v", ok, err)
	}
	if market.TotalNHBBorrowed.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("the default market's borrowed total is %s, want the 250 of the position", market.TotalNHBBorrowed)
	}
	// A second pass finds nothing left to take.
	touchPool(t, sp, "default", legacyPositionActivation+2)
	market, _, _ = nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
	if market.TotalNHBBorrowed.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("a second pass changed the borrowed total to %s", market.TotalNHBBorrowed)
	}
}
