package bank

import (
	"math/big"
	"testing"

	"nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func newTestManager(t *testing.T) *state.Manager {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() {
		db.Close()
	})
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return state.NewManager(tr)
}

func TestNoopSlasherDisabled(t *testing.T) {
	s := NewNoopSlasher(false)
	if err := s.Slash([20]byte{}, big.NewInt(10)); err == nil {
		t.Fatalf("expected error when disabled")
	}
}

func TestNoopSlasherZeroAmount(t *testing.T) {
	s := NewNoopSlasher(false)
	if err := s.Slash([20]byte{}, big.NewInt(0)); err != nil {
		t.Fatalf("expected no error for zero amount: %v", err)
	}
}

func TestNoopSlasherNegativeAmount(t *testing.T) {
	s := NewNoopSlasher(true)
	if err := s.Slash([20]byte{}, big.NewInt(-1)); err == nil {
		t.Fatalf("expected negative amount error")
	}
}

func TestNoopSlasherEnabledIsNoop(t *testing.T) {
	s := NewNoopSlasher(true)
	if err := s.Slash([20]byte{}, big.NewInt(10)); err != nil {
		t.Fatalf("expected enabled noop slasher to succeed, got %v", err)
	}
}

// TestValidatorSlasherCreditsTreasury is a direct regression test for the
// user-mandated fix: ZNHB has a hard fixed supply (no protocol path to
// mint more after genesis), so a slashed validator's forfeited stake must
// never simply be discarded -- it has to land somewhere real. This asserts
// the offender's LockedZNHB/Stake go down by exactly the penalty and the
// treasury's BalanceZNHB goes up by exactly the same amount, with total
// ZNHB held across both accounts unchanged (proving this is a pure
// transfer, not a burn or a mint).
func TestValidatorSlasherCreditsTreasury(t *testing.T) {
	mgr := newTestManager(t)
	var offender, treasury [20]byte
	offender[19] = 1
	treasury[19] = 2

	if err := mgr.PutAccount(offender[:], &types.Account{
		LockedZNHB: big.NewInt(1_000),
		Stake:      big.NewInt(1_000),
	}); err != nil {
		t.Fatalf("seed offender: %v", err)
	}
	if err := mgr.PutAccount(treasury[:], &types.Account{
		BalanceZNHB: big.NewInt(500),
	}); err != nil {
		t.Fatalf("seed treasury: %v", err)
	}

	s := NewValidatorSlasher(mgr, treasury)
	if err := s.Slash(offender, big.NewInt(300)); err != nil {
		t.Fatalf("slash: %v", err)
	}

	offenderAfter, err := mgr.GetAccount(offender[:])
	if err != nil {
		t.Fatalf("get offender: %v", err)
	}
	if offenderAfter.LockedZNHB.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected offender LockedZNHB=700, got %s", offenderAfter.LockedZNHB)
	}
	if offenderAfter.Stake.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected offender Stake=700, got %s", offenderAfter.Stake)
	}

	treasuryAfter, err := mgr.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAfter.BalanceZNHB.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("expected treasury BalanceZNHB=800 (500 + 300 penalty), got %s", treasuryAfter.BalanceZNHB)
	}

	totalBefore := big.NewInt(1_000 + 500)
	totalAfter := new(big.Int).Add(offenderAfter.LockedZNHB, treasuryAfter.BalanceZNHB)
	// LockedZNHB + BalanceZNHB isn't literally the same accounting bucket,
	// but the point stands: exactly 300 moved from one to the other, no
	// more, no less -- assert that delta directly instead of a same-bucket
	// sum, which would be comparing apples to oranges.
	_ = totalBefore
	_ = totalAfter
	movedFromOffender := new(big.Int).Sub(big.NewInt(1_000), offenderAfter.LockedZNHB)
	movedToTreasury := new(big.Int).Sub(treasuryAfter.BalanceZNHB, big.NewInt(500))
	if movedFromOffender.Cmp(movedToTreasury) != 0 {
		t.Fatalf("penalty debited (%s) does not match penalty credited (%s) -- value was created or destroyed", movedFromOffender, movedToTreasury)
	}
}

// TestValidatorSlasherCapsAtLockedBalance confirms the existing
// cap-at-available-balance behavior (penalty larger than what's actually
// locked) still only credits the treasury with what was actually taken,
// not the requested (uncapped) penalty amount.
func TestValidatorSlasherCapsAtLockedBalance(t *testing.T) {
	mgr := newTestManager(t)
	var offender, treasury [20]byte
	offender[19] = 3
	treasury[19] = 4

	if err := mgr.PutAccount(offender[:], &types.Account{
		LockedZNHB: big.NewInt(100),
		Stake:      big.NewInt(100),
	}); err != nil {
		t.Fatalf("seed offender: %v", err)
	}

	s := NewValidatorSlasher(mgr, treasury)
	if err := s.Slash(offender, big.NewInt(1_000)); err != nil {
		t.Fatalf("slash: %v", err)
	}

	offenderAfter, err := mgr.GetAccount(offender[:])
	if err != nil {
		t.Fatalf("get offender: %v", err)
	}
	if offenderAfter.LockedZNHB.Sign() != 0 {
		t.Fatalf("expected offender LockedZNHB=0, got %s", offenderAfter.LockedZNHB)
	}

	treasuryAfter, err := mgr.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAfter.BalanceZNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected treasury credited exactly the capped 100, got %s", treasuryAfter.BalanceZNHB)
	}
}

// TestValidatorSlasherAccumulatesAcrossMultipleSlashes confirms repeated
// slashing events (e.g. two different offenders, or the same offender
// slashed twice) correctly accumulate in the treasury rather than
// overwriting each other.
func TestValidatorSlasherAccumulatesAcrossMultipleSlashes(t *testing.T) {
	mgr := newTestManager(t)
	var offenderA, offenderB, treasury [20]byte
	offenderA[19] = 5
	offenderB[19] = 6
	treasury[19] = 7

	if err := mgr.PutAccount(offenderA[:], &types.Account{LockedZNHB: big.NewInt(500)}); err != nil {
		t.Fatalf("seed offenderA: %v", err)
	}
	if err := mgr.PutAccount(offenderB[:], &types.Account{LockedZNHB: big.NewInt(500)}); err != nil {
		t.Fatalf("seed offenderB: %v", err)
	}

	s := NewValidatorSlasher(mgr, treasury)
	if err := s.Slash(offenderA, big.NewInt(150)); err != nil {
		t.Fatalf("slash A: %v", err)
	}
	if err := s.Slash(offenderB, big.NewInt(250)); err != nil {
		t.Fatalf("slash B: %v", err)
	}

	treasuryAfter, err := mgr.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAfter.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected treasury BalanceZNHB=400 (150+250 accumulated), got %s", treasuryAfter.BalanceZNHB)
	}
}
