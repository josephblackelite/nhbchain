package core

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/events"
)

// PL-R5T-26: ZNHB the offender has already moved into a pending unbond is out of
// the slash's reach (a 7-day unbond against an 8,640-block evidence window), and the
// offense is still marked penalised with a slash of zero. These tests unbond through
// the real undelegate path and process a genuine double-sign report the way a block
// does, with the rule off and on.

// unbondOwn moves amount of the offender's own stake into a pending unbond, as the
// offender would before the report lands.
func (e *slashEnv) unbondOwn(amount int64) {
	e.t.Helper()
	e.sp.BeginBlock(60, slashNow)
	if _, err := e.sp.StakeUndelegate(e.offender[:], big.NewInt(amount)); err != nil {
		e.t.Fatalf("unbond %d: %v", amount, err)
	}
}

func (e *slashEnv) account(addr [20]byte) (locked, stake, unbonding, balance *big.Int) {
	e.t.Helper()
	acct, err := e.manager.GetAccount(addr[:])
	if err != nil {
		e.t.Fatalf("load %x: %v", addr[:2], err)
	}
	unbonding = big.NewInt(0)
	for _, u := range acct.PendingUnbonds {
		unbonding.Add(unbonding, u.Amount)
	}
	return acct.LockedZNHB, acct.Stake, unbonding, acct.BalanceZNHB
}

func (e *slashEnv) penaltyEventAmount() string {
	for _, evt := range e.sp.Events() {
		if evt.Type == events.TypePotsoPenaltyApplied {
			return evt.Attributes["slashAmt"]
		}
	}
	return "(no penalty event)"
}

func atHeight(h uint64) ValidatorRules { return ValidatorRules{SlashPendingUnbondsActivationHeight: h} }

// Off, the slash forfeits nothing from an unbond and the offense is spent: today's
// behaviour, pinned.
func TestSlashPendingUnbonds_OffForfeitsNothingFromAnUnbond(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} {
		env := newSlashEnv(t, atHeight(from), 2_000, 0)
		env.unbondOwn(2_000)
		env.process(200)
		_, stake, unbonding, _ := env.account(env.offender)
		if stake.Sign() != 0 {
			t.Fatalf("setup: stake %s, want 0 after unbonding everything", stake)
		}
		if unbonding.Cmp(big.NewInt(2_000)) != 0 {
			t.Fatalf("height %d: the unbond holds %s, want the untouched 2000", from, unbonding)
		}
		if got := env.penaltyEventAmount(); got != "0" {
			t.Fatalf("height %d: penalty event slashAmt = %s, want 0", from, got)
		}
		offense := (&evidence.Record{Hash: env.hash, Evidence: env.ev}).Offense()
		applied, err := env.manager.PotsoPenaltyApplied(offense.Key, env.offender)
		if err != nil || !applied {
			t.Fatalf("the offense must be marked penalised (%v, %v)", applied, err)
		}
	}
}

// On, the unbond is forfeited to the treasury and the event says so.
func TestSlashPendingUnbonds_OnForfeitsTheUnbond(t *testing.T) {
	env := newSlashEnv(t, atHeight(100), 2_000, 0)
	env.unbondOwn(2_000)
	_, _, _, treasuryBefore := env.account(env.sp.evidenceSlashTreasury())
	env.process(200)
	_, _, unbonding, _ := env.account(env.offender)
	if unbonding.Sign() != 0 {
		t.Fatalf("the unbond still holds %s", unbonding)
	}
	_, _, _, treasuryAfter := env.account(env.sp.evidenceSlashTreasury())
	if gained := new(big.Int).Sub(treasuryAfter, treasuryBefore); gained.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("the treasury gained %s, want 2000", gained)
	}
	if got := env.penaltyEventAmount(); got != "2000" {
		t.Fatalf("penalty event slashAmt = %s, want 2000", got)
	}
}

// Part of the offender's own stake is still locked and part is in an unbond: both are
// forfeited, the locked part first.
func TestSlashPendingUnbonds_LockedAndUnbonded(t *testing.T) {
	env := newSlashEnv(t, atHeight(100), 3_000, 0)
	env.unbondOwn(1_000)
	env.process(200)
	locked, stake, unbonding, _ := env.account(env.offender)
	if locked.Sign() != 0 || stake.Sign() != 0 || unbonding.Sign() != 0 {
		t.Fatalf("locked %s stake %s unbonding %s, want all 0", locked, stake, unbonding)
	}
	if got := env.penaltyEventAmount(); got != "3000" {
		t.Fatalf("penalty event slashAmt = %s, want 3000", got)
	}
}

// Stake that other wallets delegated in is theirs: the slash does not reach it (it is
// the other half of PL-R5T-26 and stays an owner decision), and the base the slash is
// sized on does not count it twice.
func TestSlashPendingUnbonds_DelegatedInStakeIsNotReached(t *testing.T) {
	env := newSlashEnv(t, atHeight(100), 2_000, 3_000)
	env.unbondOwn(1_000)
	env.process(200)
	locked, stake, unbonding, _ := env.account(env.offender)
	if locked.Sign() != 0 || unbonding.Sign() != 0 {
		t.Fatalf("locked %s unbonding %s, want both forfeited", locked, unbonding)
	}
	if stake.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("stake %s, want the 3000 delegated in, untouched", stake)
	}
	if got := env.penaltyEventAmount(); got != "2000" {
		t.Fatalf("penalty event slashAmt = %s, want 2000 (1000 locked + 1000 unbonded)", got)
	}
}

// The rule starts at its height.
func TestSlashPendingUnbonds_ActivationBoundary(t *testing.T) {
	before := newSlashEnv(t, atHeight(200), 2_000, 0)
	before.unbondOwn(2_000)
	before.process(199)
	if _, _, unbonding, _ := before.account(before.offender); unbonding.Sign() == 0 {
		t.Fatalf("the unbond was reached one block before the height")
	}
	at := newSlashEnv(t, atHeight(200), 2_000, 0)
	at.unbondOwn(2_000)
	at.process(200)
	if _, _, unbonding, _ := at.account(at.offender); unbonding.Sign() != 0 {
		t.Fatalf("the unbond was not reached at the height")
	}
}

// Nothing is created or destroyed: offender plus treasury hold the same total ZNHB
// before and after.
func TestSlashPendingUnbonds_ConservesZNHB(t *testing.T) {
	env := newSlashEnv(t, atHeight(100), 2_000, 1_000)
	env.unbondOwn(1_500)
	total := func() *big.Int {
		sum := big.NewInt(0)
		for _, addr := range [][20]byte{env.offender, env.sp.evidenceSlashTreasury()} {
			locked, _, unbonding, balance := env.account(addr)
			sum.Add(sum, locked).Add(sum, unbonding).Add(sum, balance)
		}
		return sum
	}
	before := total()
	env.process(200)
	if after := total(); after.Cmp(before) != 0 {
		t.Fatalf("offender plus treasury held %s before and %s after", before, after)
	}
}

// Processed again, the same report changes nothing (the offense is spent), and two
// nodes that process it get the same amounts.
func TestSlashPendingUnbonds_RepeatableAndIdempotent(t *testing.T) {
	a := newSlashEnv(t, atHeight(100), 2_000, 0)
	b := newSlashEnv(t, atHeight(100), 2_000, 0)
	a.unbondOwn(2_000)
	b.unbondOwn(2_000)
	a.process(200)
	b.process(200)
	if a.penaltyEventAmount() != b.penaltyEventAmount() {
		t.Fatalf("two nodes forfeited %s and %s", a.penaltyEventAmount(), b.penaltyEventAmount())
	}
	before := a.root()
	a.process(201)
	if !bytes.Equal(before, a.root()) {
		t.Fatalf("a second pass over a penalised report changed state")
	}
}

// The unbonds of an account that holds none and is not slashed are not touched by the
// rule: slashing a validator that never unbonded behaves exactly as with the rule off.
func TestSlashPendingUnbonds_NoUnbondsMeansNoDifference(t *testing.T) {
	off := newSlashEnv(t, atHeight(0), 2_000, 0)
	on := newSlashEnv(t, atHeight(100), 2_000, 0)
	off.process(200)
	on.process(200)
	if off.penaltyEventAmount() != on.penaltyEventAmount() {
		t.Fatalf("rule on forfeited %s, rule off %s", on.penaltyEventAmount(), off.penaltyEventAmount())
	}
	lo, so, uo, bo := off.account(off.offender)
	ln, sn, un, bn := on.account(on.offender)
	if lo.Cmp(ln) != 0 || so.Cmp(sn) != 0 || uo.Cmp(un) != 0 || bo.Cmp(bn) != 0 {
		t.Fatalf("the accounts differ after the slash")
	}
}
