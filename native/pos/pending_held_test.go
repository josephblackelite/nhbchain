package pos

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

func (f *sweepFixture) requireHeld(payer [20]byte, want int64) {
	f.t.Helper()
	got, err := f.engine.PendingHeld(payer)
	if err != nil {
		f.t.Fatalf("pending held: %v", err)
	}
	if got.Cmp(big.NewInt(want)) != 0 {
		f.t.Fatalf("payer %x holds %s in pending authorizations, want %d", payer[:2], got, want)
	}
}

// authorizeFor places a hold for a payer funded with 10000 on first use.
func (f *sweepFixture) authorizeFor(payer [20]byte, amount int64, expiryAfter time.Duration) [32]byte {
	f.t.Helper()
	if _, ok := f.state.accounts[string(payer[:])]; !ok {
		f.state.accounts[string(payer[:])] = &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	}
	auth, err := f.engine.Authorize(payer, f.merchant, big.NewInt(amount), uint64(f.base.Add(expiryAfter).Unix()), nil)
	if err != nil {
		f.t.Fatalf("authorize: %v", err)
	}
	return auth.ID
}

// The pending holds of a payer are what Authorize added to its locked balance
// and capture, void and expiry take out again, so the figure has to follow each
// of them exactly.
func TestPendingHeldFollowsAuthorizeCaptureVoidAndExpiry(t *testing.T) {
	f := newSweepFixture(t)
	a, b, nobody := sweepPayer(1), sweepPayer(2), sweepPayer(3)

	f.requireHeld(a, 0)
	first := f.authorizeFor(a, 300, time.Hour)
	second := f.authorizeFor(a, 200, 2*time.Hour)
	f.authorizeFor(b, 100, time.Hour)
	f.requireHeld(a, 500)
	f.requireHeld(b, 100)
	f.requireHeld(nobody, 0)
	f.requireAccount(a, 9_500, 500)

	// A partial capture settles the whole hold: the unused part goes back to
	// the payer's balance, so none of it is held any more.
	if _, err := f.engine.Capture(first, big.NewInt(120), f.merchant); err != nil {
		t.Fatalf("capture: %v", err)
	}
	f.requireHeld(a, 200)
	f.requireAccount(a, 9_680, 200)

	if _, err := f.engine.Void(second, "changed my mind", a); err != nil {
		t.Fatalf("void: %v", err)
	}
	f.requireHeld(a, 0)
	f.requireAccount(a, 10_000-120, 0)
	f.requireHeld(b, 100)

	// Expiry releases the last one.
	if _, err := f.engine.SweepExpired(f.sweepAt()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f.requireHeld(b, 0)
}

// A capture the payer makes against itself moves nothing out of the account,
// and the hold is gone from the pending set all the same.
func TestPendingHeldPayerCapturingItsOwnHold(t *testing.T) {
	f := newSweepFixture(t)
	payer := sweepPayer(4)
	f.merchant = payer
	id := f.authorizeFor(payer, 250, time.Hour)
	f.requireHeld(payer, 250)
	if _, err := f.engine.Capture(id, big.NewInt(250), payer); err != nil {
		t.Fatalf("capture: %v", err)
	}
	f.requireHeld(payer, 0)
}

// A hold that no longer has a pending record behind its index entry holds
// nothing: the sweep removes such an entry, and the figure ignores it the same
// way instead of failing.
func TestPendingHeldIgnoresStaleIndexEntries(t *testing.T) {
	f := newSweepFixture(t)
	payer := sweepPayer(5)
	live := f.authorizeFor(payer, 400, time.Hour)
	settled := f.authorizeFor(payer, 90, time.Hour)

	// Settle one hold but leave its entry in the index, and add an entry that
	// never had a record.
	if _, err := f.engine.Capture(settled, big.NewInt(90), f.merchant); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var ghost [32]byte
	ghost[0], ghost[31] = 0xee, 0x01
	if err := f.state.KVPut(authorizationPendingIndex, [][]byte{live[:], settled[:], ghost[:], {0x01, 0x02}}); err != nil {
		t.Fatalf("write index: %v", err)
	}
	f.requireHeld(payer, 400)
}

func (f *sweepFixture) requireHeldByPayer(want map[[20]byte]int64) {
	f.t.Helper()
	got, err := f.engine.PendingHeldByPayer()
	if err != nil {
		f.t.Fatalf("pending held by payer: %v", err)
	}
	if len(got) != len(want) {
		f.t.Fatalf("PendingHeldByPayer names %d payers, want %d: %v", len(got), len(want), got)
	}
	for payer, amount := range want {
		if held := got[payer]; held == nil || held.Cmp(big.NewInt(amount)) != 0 {
			f.t.Fatalf("payer %x holds %v, want %d", payer[:2], held, amount)
		}
		// The two reads agree for every payer.
		f.requireHeld(payer, amount)
	}
}

// PendingHeldByPayer is PendingHeld for every payer in one pass: the same figures
// through authorize, capture, void and expiry, a payer with nothing pending absent,
// and the same entries skipped (a settled record, a missing one, a malformed id).
func TestPendingHeldByPayerAgreesWithPendingHeld(t *testing.T) {
	f := newSweepFixture(t)
	a, b, nobody := sweepPayer(1), sweepPayer(2), sweepPayer(3)

	got, err := f.engine.PendingHeldByPayer()
	if err != nil || len(got) != 0 {
		t.Fatalf("with nothing pending: %v, %v", got, err)
	}

	first := f.authorizeFor(a, 300, time.Hour)
	second := f.authorizeFor(a, 200, 2*time.Hour)
	f.authorizeFor(b, 100, time.Hour)
	f.requireHeldByPayer(map[[20]byte]int64{a: 500, b: 100})
	f.requireHeld(nobody, 0)

	if _, err := f.engine.Capture(first, big.NewInt(120), f.merchant); err != nil {
		t.Fatalf("capture: %v", err)
	}
	f.requireHeldByPayer(map[[20]byte]int64{a: 200, b: 100})

	if _, err := f.engine.Void(second, "changed my mind", a); err != nil {
		t.Fatalf("void: %v", err)
	}
	f.requireHeldByPayer(map[[20]byte]int64{b: 100})

	if _, err := f.engine.SweepExpired(f.sweepAt()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f.requireHeldByPayer(map[[20]byte]int64{})
}

func TestPendingHeldByPayerIgnoresStaleIndexEntries(t *testing.T) {
	f := newSweepFixture(t)
	payer := sweepPayer(5)
	live := f.authorizeFor(payer, 400, time.Hour)
	settled := f.authorizeFor(payer, 90, time.Hour)
	if _, err := f.engine.Capture(settled, big.NewInt(90), f.merchant); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var ghost [32]byte
	ghost[0], ghost[31] = 0xee, 0x01
	if err := f.state.KVPut(authorizationPendingIndex, [][]byte{live[:], settled[:], ghost[:], {0x01, 0x02}}); err != nil {
		t.Fatalf("write index: %v", err)
	}
	f.requireHeldByPayer(map[[20]byte]int64{payer: 400})
}

func TestPendingHeldByPayerNeedsALifecycle(t *testing.T) {
	var nobody *Lifecycle
	if _, err := nobody.PendingHeldByPayer(); err == nil {
		t.Fatalf("a nil lifecycle answered")
	}
}
