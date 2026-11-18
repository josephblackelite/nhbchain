package bank

import (
	"math/big"
	"testing"

	"nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-R5T-26: the slasher forfeits at most the offender's own LockedZNHB. ZNHB the
// offender has already moved into a pending unbond (7 days by default, against an
// evidence window of 8,640 blocks) is never touched. These tests are about the
// slasher that reaches the offender's own unclaimed unbonds, once the locked balance
// is used up. The default slasher is unchanged (the first test pins that).

func unbond(id uint64, validator []byte, amount int64) types.StakeUnbond {
	return types.StakeUnbond{ID: id, Validator: append([]byte(nil), validator...), Amount: big.NewInt(amount), ReleaseTime: 1_900_000_000}
}

func seedOffender(t *testing.T, mgr *state.Manager, offender [20]byte, locked, stake int64, unbonds ...types.StakeUnbond) {
	t.Helper()
	acct, err := mgr.GetAccount(offender[:])
	if err != nil {
		t.Fatalf("load offender: %v", err)
	}
	acct.LockedZNHB = big.NewInt(locked)
	acct.Stake = big.NewInt(stake)
	acct.PendingUnbonds = unbonds
	if err := mgr.PutAccount(offender[:], acct); err != nil {
		t.Fatalf("seed offender: %v", err)
	}
}

func unbondAmounts(t *testing.T, mgr *state.Manager, addr [20]byte) []int64 {
	t.Helper()
	acct, err := mgr.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := []int64{}
	for _, u := range acct.PendingUnbonds {
		out = append(out, u.Amount.Int64())
	}
	return out
}

func eq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDefaultSlasherNeverTouchesPendingUnbonds(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 0, 0, unbond(1, offender[:], 700))
	applied, err := NewValidatorSlasher(mgr, treasury).SlashApplied(offender, big.NewInt(1_000))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Sign() != 0 {
		t.Fatalf("the default slasher forfeited %s from an unbond", applied)
	}
	if got := unbondAmounts(t, mgr, offender); !eq(got, []int64{700}) {
		t.Fatalf("unbond changed: %v", got)
	}
}

// Everything the offender holds is gone when it unbonded it all before the report.
func TestReachingSlasherTakesAnUnbondWhenNothingIsLocked(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 0, 0, unbond(1, offender[:], 2_000))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(2_000))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("forfeited %s, want 2000", applied)
	}
	if got := unbondAmounts(t, mgr, offender); len(got) != 0 {
		t.Fatalf("the emptied unbond is still listed: %v", got)
	}
	tr, _ := mgr.GetAccount(treasury[:])
	if tr.BalanceZNHB.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("treasury holds %s, want 2000", tr.BalanceZNHB)
	}
}

// The locked balance goes first, then the unbonds in the order they were made, the
// last one partly.
func TestReachingSlasherTakesLockedFirstThenUnbondsInOrder(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 500, 500, unbond(1, offender[:], 1_000), unbond(2, offender[:], 1_000))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(2_000))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("forfeited %s, want 2000 (500 locked + 1000 + 500 of the second unbond)", applied)
	}
	if got := unbondAmounts(t, mgr, offender); !eq(got, []int64{500}) {
		t.Fatalf("remaining unbonds %v, want only the second at 500", got)
	}
	acct, _ := mgr.GetAccount(offender[:])
	if acct.LockedZNHB.Sign() != 0 || acct.Stake.Sign() != 0 {
		t.Fatalf("locked %s stake %s, want both 0", acct.LockedZNHB, acct.Stake)
	}
}

// Only the offender's own unbonds (the ones made out of its stake in itself) are
// reached; an unbond from stake it delegated to another validator is not its
// validator stake.
func TestReachingSlasherLeavesUnbondsFromOtherValidators(t *testing.T) {
	mgr := newTestManager(t)
	offender, other, treasury := [20]byte{0x01}, [20]byte{0x02}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 0, 0, unbond(1, other[:], 900), unbond(2, offender[:], 300))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(5_000))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("forfeited %s, want only the offender's own 300", applied)
	}
	if got := unbondAmounts(t, mgr, offender); !eq(got, []int64{900}) {
		t.Fatalf("remaining unbonds %v, want the other validator's 900", got)
	}
}

// The amount asked for is the ceiling.
func TestReachingSlasherNeverTakesMoreThanAsked(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 0, 0, unbond(1, offender[:], 1_000))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(400))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("forfeited %s, want 400", applied)
	}
	if got := unbondAmounts(t, mgr, offender); !eq(got, []int64{600}) {
		t.Fatalf("remaining %v, want 600", got)
	}
}

// What a locked-only offender loses is what it lost before: the reach is an addition
// for the unbonds, not a different rule for the locked balance.
func TestReachingSlasherMatchesTheDefaultWhenThereAreNoUnbonds(t *testing.T) {
	for _, tc := range []struct{ locked, stake, ask int64 }{
		{1_000, 1_000, 1_000}, {400, 1_000, 1_000}, {1_000, 300, 1_000}, {1_000, 1_000, 250}, {0, 0, 100},
	} {
		a, b := newTestManager(t), newTestManager(t)
		offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
		seedOffender(t, a, offender, tc.locked, tc.stake)
		seedOffender(t, b, offender, tc.locked, tc.stake)
		want, err := NewValidatorSlasher(a, treasury).SlashApplied(offender, big.NewInt(tc.ask))
		if err != nil {
			t.Fatal(err)
		}
		got, err := NewValidatorSlasher(b, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(tc.ask))
		if err != nil {
			t.Fatal(err)
		}
		if got.Cmp(want) != 0 {
			t.Errorf("locked %d stake %d ask %d: reaching slasher took %s, default took %s", tc.locked, tc.stake, tc.ask, got, want)
		}
		ra, _ := a.GetAccount(offender[:])
		rb, _ := b.GetAccount(offender[:])
		if ra.LockedZNHB.Cmp(rb.LockedZNHB) != 0 || ra.Stake.Cmp(rb.Stake) != 0 {
			t.Errorf("locked %d stake %d ask %d: accounts differ after the slash", tc.locked, tc.stake, tc.ask)
		}
	}
}

// ZNHB is not created or destroyed: what leaves the offender lands on the treasury.
func TestReachingSlasherConservesTheTotal(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 300, 300, unbond(1, offender[:], 700), unbond(2, offender[:], 50))
	total := func() int64 {
		var sum int64
		for _, addr := range [][20]byte{offender, treasury} {
			a, _ := mgr.GetAccount(addr[:])
			sum += a.LockedZNHB.Int64() + a.BalanceZNHB.Int64()
			for _, u := range a.PendingUnbonds {
				sum += u.Amount.Int64()
			}
		}
		return sum
	}
	before := total()
	if _, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(1_000)); err != nil {
		t.Fatal(err)
	}
	if after := total(); after != before {
		t.Fatalf("total ZNHB changed from %d to %d", before, after)
	}
}

// Entries with no amount or a zero amount are skipped, not trusted.
func TestReachingSlasherIgnoresMalformedUnbondEntries(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	bad := types.StakeUnbond{ID: 1, Validator: offender[:], Amount: nil}
	zero := types.StakeUnbond{ID: 3, Validator: offender[:], Amount: big.NewInt(0)}
	seedOffender(t, mgr, offender, 0, 0, bad, zero, unbond(4, offender[:], 40))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(100))
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if applied.Cmp(big.NewInt(40)) != 0 {
		t.Fatalf("forfeited %s, want 40", applied)
	}
}

// ReachingPendingUnbonds returns a copy: the slasher it was made from is unchanged.
func TestReachingPendingUnbondsDoesNotChangeTheOriginal(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	seedOffender(t, mgr, offender, 0, 0, unbond(1, offender[:], 100))
	base := NewValidatorSlasher(mgr, treasury)
	_ = base.ReachingPendingUnbonds()
	applied, err := base.SlashApplied(offender, big.NewInt(100))
	if err != nil {
		t.Fatal(err)
	}
	if applied.Sign() != 0 {
		t.Fatalf("the original slasher reached an unbond: %s", applied)
	}
}

// Stake that came from other wallets is not reduced by the part taken from an unbond:
// the unbond left the stake when it was made. Only the locked part lowers it.
func TestReachingSlasherLowersTheStakeOnlyByTheLockedPart(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	// 1,000 of its own still locked, 4,000 delegated in, 1,000 of its own in an unbond.
	seedOffender(t, mgr, offender, 1_000, 5_000, unbond(1, offender[:], 1_000))
	applied, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(2_000))
	if err != nil {
		t.Fatal(err)
	}
	if applied.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("forfeited %s, want 2000", applied)
	}
	acct, _ := mgr.GetAccount(offender[:])
	if acct.Stake.Cmp(big.NewInt(4_000)) != 0 {
		t.Fatalf("stake is %s, want the 4000 delegated in", acct.Stake)
	}
}

// An entry that holds nothing is not touched: it stays where it was.
func TestReachingSlasherLeavesEmptyEntriesAlone(t *testing.T) {
	mgr := newTestManager(t)
	offender, treasury := [20]byte{0x01}, [20]byte{0xEE}
	zero := types.StakeUnbond{ID: 1, Validator: offender[:], Amount: big.NewInt(0)}
	seedOffender(t, mgr, offender, 0, 0, zero, unbond(2, offender[:], 40))
	if _, err := NewValidatorSlasher(mgr, treasury).ReachingPendingUnbonds().SlashApplied(offender, big.NewInt(100)); err != nil {
		t.Fatal(err)
	}
	if got := unbondAmounts(t, mgr, offender); !eq(got, []int64{0}) {
		t.Fatalf("remaining entries %v, want the untouched empty one", got)
	}
}
