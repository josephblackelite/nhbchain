package pos

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

// autoVoidRecorder keeps the auto-void events the sweep emits.
type autoVoidRecorder struct {
	voided [][32]byte
}

func (r *autoVoidRecorder) Emit(evt events.Event) {
	if v, ok := evt.(events.PosAuthAutoVoided); ok {
		r.voided = append(r.voided, v.AuthorizationID)
	}
}

// failingPutState refuses to store one account, to drive the sweep into an
// error that is not a locked-balance mismatch.
type failingPutState struct {
	*memoryLifecycleState
	failFor [20]byte
}

func (s *failingPutState) PutAccount(addr []byte, account *types.Account) error {
	if string(addr) == string(s.failFor[:]) {
		return errors.New("test: account store unavailable")
	}
	return s.memoryLifecycleState.PutAccount(addr, account)
}

type sweepFixture struct {
	t        *testing.T
	state    *memoryLifecycleState
	engine   *Lifecycle
	events   *autoVoidRecorder
	base     time.Time
	merchant [20]byte
}

// newSweepFixture returns a fixture whose lifecycle skips an unreleasable hold
// and carries on, which is what most of the tests below are about.
// newDefaultSweepFixture leaves the lifecycle as NewLifecycle made it.
func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	f := newDefaultSweepFixture(t)
	f.engine.SetSweepSkipsUnreleasable(true)
	return f
}

func newDefaultSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	state := newMemoryLifecycleState()
	return newSweepFixtureOn(t, state, state)
}

func newSweepFixtureOn(t *testing.T, state lifecycleState, backing *memoryLifecycleState) *sweepFixture {
	t.Helper()
	f := &sweepFixture{
		t:      t,
		state:  backing,
		engine: NewLifecycle(state),
		events: &autoVoidRecorder{},
		base:   time.Unix(1_700_000_000, 0).UTC(),
	}
	f.merchant[0] = 0x4d
	f.engine.SetEmitter(f.events)
	f.engine.SetNowFunc(func() time.Time { return f.base })
	return f
}

func sweepPayer(tag byte) [20]byte {
	var payer [20]byte
	payer[0] = 0xa0
	payer[1] = tag
	return payer
}

// hold funds a fresh payer with 1000 and places a hold of amount that expires
// an hour after the fixture's base time.
func (f *sweepFixture) hold(tag byte, amount int64) ([20]byte, [32]byte) {
	f.t.Helper()
	payer := sweepPayer(tag)
	f.state.accounts[string(payer[:])] = &types.Account{BalanceZNHB: big.NewInt(1_000), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	auth, err := f.engine.Authorize(payer, f.merchant, big.NewInt(amount), uint64(f.base.Add(time.Hour).Unix()), nil)
	if err != nil {
		f.t.Fatalf("authorize: %v", err)
	}
	return payer, auth.ID
}

// setLocked rewrites the payer's locked balance directly, leaving it below
// (or back above) what the payer's hold needs.
func (f *sweepFixture) setLocked(payer [20]byte, locked int64) {
	f.t.Helper()
	acc, _ := f.state.GetAccount(payer[:])
	acc.LockedZNHB = big.NewInt(locked)
	if err := f.state.PutAccount(payer[:], acc); err != nil {
		f.t.Fatalf("put account: %v", err)
	}
}

func (f *sweepFixture) sweepAt() time.Time { return f.base.Add(2 * time.Hour) }

func (f *sweepFixture) status(id [32]byte) AuthorizationStatus {
	f.t.Helper()
	auth, err := f.engine.Get(id)
	if err != nil {
		f.t.Fatalf("get authorization: %v", err)
	}
	return auth.Status
}

func (f *sweepFixture) requireAccount(payer [20]byte, balance, locked int64) {
	f.t.Helper()
	acc, _ := f.state.GetAccount(payer[:])
	if acc.BalanceZNHB.Cmp(big.NewInt(balance)) != 0 || acc.LockedZNHB.Cmp(big.NewInt(locked)) != 0 {
		f.t.Fatalf("account %x: balance %s locked %s, want %d / %d", payer[:2], acc.BalanceZNHB, acc.LockedZNHB, balance, locked)
	}
}

func (f *sweepFixture) requirePending(want ...[32]byte) {
	f.t.Helper()
	ids, err := f.engine.pendingAuthorizationIDs()
	if err != nil {
		f.t.Fatalf("pending index: %v", err)
	}
	if len(ids) != len(want) {
		f.t.Fatalf("pending index has %d entries, want %d", len(ids), len(want))
	}
	for i := range want {
		if string(ids[i]) != string(want[i][:]) {
			f.t.Fatalf("pending index entry %d is %x, want %x", i, ids[i], want[i])
		}
	}
}

func requireVoided(t *testing.T, got []*Authorization, want ...[32]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sweep voided %d authorizations, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("voided[%d] is %x, want %x", i, got[i].ID, want[i])
		}
		if got[i].Status != AuthorizationStatusExpired {
			t.Fatalf("voided[%d] status %v, want expired", i, got[i].Status)
		}
	}
}

// An expired hold the payer's locked balance no longer covers is left pending,
// and the holds queued behind it are still released.
func TestSweepExpiredSkipsUnreleasableHoldAndReleasesTheRest(t *testing.T) {
	f := newSweepFixture(t)
	stuckPayer, stuckID := f.hold(1, 600)
	healthyA, idA := f.hold(2, 300)
	healthyB, idB := f.hold(3, 200)
	f.setLocked(stuckPayer, 100)

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if !errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("sweep error = %v, want one wrapping ErrSweepSkipped", err)
	}
	if want := "pos: expiry sweep left authorizations pending: 1"; err.Error() != want {
		t.Fatalf("sweep error text = %q, want %q", err.Error(), want)
	}
	requireVoided(t, voided, idA, idB)

	if got := f.status(idA); got != AuthorizationStatusExpired {
		t.Fatalf("first healthy hold status %v, want expired", got)
	}
	if got := f.status(idB); got != AuthorizationStatusExpired {
		t.Fatalf("second healthy hold status %v, want expired", got)
	}
	f.requireAccount(healthyA, 1_000, 0)
	f.requireAccount(healthyB, 1_000, 0)

	// The unreleasable hold and its payer are exactly as they were.
	if got := f.status(stuckID); got != AuthorizationStatusPending {
		t.Fatalf("stuck hold status %v, want pending", got)
	}
	f.requireAccount(stuckPayer, 400, 100)
	f.requirePending(stuckID)

	if len(f.events.voided) != 2 || f.events.voided[0] != idA || f.events.voided[1] != idB {
		t.Fatalf("auto-void events = %x, want the two released holds only", f.events.voided)
	}
}

// Unreleasable holds anywhere in the queue are skipped and counted; every
// other expired hold is released.
func TestSweepExpiredInterleavedSkipsAreCounted(t *testing.T) {
	f := newSweepFixture(t)
	_, id1 := f.hold(1, 100)
	stuck2, id2 := f.hold(2, 500)
	_, id3 := f.hold(3, 100)
	stuck4, id4 := f.hold(4, 500)
	_, id5 := f.hold(5, 100)
	f.setLocked(stuck2, 0)
	f.setLocked(stuck4, 499)

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if !errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("sweep error = %v, want one wrapping ErrSweepSkipped", err)
	}
	if want := "pos: expiry sweep left authorizations pending: 2"; err.Error() != want {
		t.Fatalf("sweep error text = %q, want %q", err.Error(), want)
	}
	requireVoided(t, voided, id1, id3, id5)
	f.requirePending(id2, id4)
	f.requireAccount(stuck2, 500, 0)
	f.requireAccount(stuck4, 500, 499)
}

// A hold that is not due yet is neither released nor counted as skipped, and a
// sweep with nothing unreleasable reports no error.
func TestSweepExpiredWithoutUnreleasableHoldsReportsNoError(t *testing.T) {
	f := newSweepFixture(t)
	_, id1 := f.hold(1, 100)
	_, id2 := f.hold(2, 100)

	voided, err := f.engine.SweepExpired(f.base.Add(30 * time.Minute))
	if err != nil || len(voided) != 0 {
		t.Fatalf("early sweep = (%d voided, %v), want nothing and no error", len(voided), err)
	}
	f.requirePending(id1, id2)

	voided, err = f.engine.SweepExpired(f.sweepAt())
	if err != nil {
		t.Fatalf("sweep error: %v", err)
	}
	requireVoided(t, voided, id1, id2)
	f.requirePending()
}

// Sweeping again changes nothing: the skipped hold is reported each time and
// the rest of the state is untouched.
func TestSweepExpiredRepeatedSweepsAreStable(t *testing.T) {
	f := newSweepFixture(t)
	stuckPayer, stuckID := f.hold(1, 600)
	healthy, healthyID := f.hold(2, 300)
	f.setLocked(stuckPayer, 100)

	if _, err := f.engine.SweepExpired(f.sweepAt()); !errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("first sweep error = %v, want ErrSweepSkipped", err)
	}
	for i := 0; i < 3; i++ {
		voided, err := f.engine.SweepExpired(f.sweepAt().Add(time.Duration(i+1) * time.Minute))
		if !errors.Is(err, ErrSweepSkipped) {
			t.Fatalf("repeat sweep %d error = %v, want ErrSweepSkipped", i, err)
		}
		if len(voided) != 0 {
			t.Fatalf("repeat sweep %d voided %d, want 0", i, len(voided))
		}
		f.requirePending(stuckID)
		f.requireAccount(stuckPayer, 400, 100)
		f.requireAccount(healthy, 1_000, 0)
		if got := f.status(healthyID); got != AuthorizationStatusExpired {
			t.Fatalf("healthy hold status %v after repeat sweep, want expired", got)
		}
	}
}

// Once the payer has enough locked again the next sweep releases the hold.
func TestSweepExpiredReleasesSkippedHoldOnceLockedBalanceIsRestored(t *testing.T) {
	f := newSweepFixture(t)
	payer, id := f.hold(1, 600)
	f.setLocked(payer, 100)

	if _, err := f.engine.SweepExpired(f.sweepAt()); !errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("sweep error = %v, want ErrSweepSkipped", err)
	}
	f.setLocked(payer, 600)

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if err != nil {
		t.Fatalf("sweep after restoring the locked balance: %v", err)
	}
	requireVoided(t, voided, id)
	f.requireAccount(payer, 1_000, 0)
	f.requirePending()
}

// Void and Capture still refuse a hold the payer's locked balance does not
// cover, with the same error text, and change nothing.
func TestUnreleasableHoldStillRejectsVoidAndCapture(t *testing.T) {
	f := newSweepFixture(t)
	payer, id := f.hold(1, 600)
	f.setLocked(payer, 100)

	const want = "pos: locked balance inconsistent"
	if _, err := f.engine.Void(id, "", payer); err == nil || err.Error() != want || !errors.Is(err, errLockedBalanceInconsistent) {
		t.Fatalf("void error = %v, want %q", err, want)
	}
	if _, err := f.engine.Capture(id, big.NewInt(600), f.merchant); err == nil || err.Error() != want || !errors.Is(err, errLockedBalanceInconsistent) {
		t.Fatalf("capture error = %v, want %q", err, want)
	}
	if got := f.status(id); got != AuthorizationStatusPending {
		t.Fatalf("hold status %v, want pending", got)
	}
	f.requireAccount(payer, 400, 100)
	f.requirePending(id)
}

// Only the locked-balance mismatch is skipped. Any other failure still ends
// the sweep, returns no result and leaves the later holds alone.
func TestSweepExpiredStillAbortsOnOtherErrors(t *testing.T) {
	backing := newMemoryLifecycleState()
	failing := &failingPutState{memoryLifecycleState: backing}
	f := newSweepFixtureOn(t, failing, backing)
	f.engine.SetSweepSkipsUnreleasable(true)
	failPayer, failID := f.hold(1, 100)
	later, laterID := f.hold(2, 100)
	failing.failFor = failPayer

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if err == nil {
		t.Fatal("sweep succeeded despite an account store failure")
	}
	if errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("sweep error %v must not be reported as a skip", err)
	}
	if voided != nil {
		t.Fatalf("sweep returned %d voided records with a hard error", len(voided))
	}
	if got := f.status(failID); got != AuthorizationStatusPending {
		t.Fatalf("failed hold status %v, want pending", got)
	}
	if got := f.status(laterID); got != AuthorizationStatusPending {
		t.Fatalf("later hold status %v, want pending", got)
	}
	f.requireAccount(later, 900, 100)
	f.requirePending(failID, laterID)
}

// A lifecycle that was never told to skip stops at the first expired hold the
// payer's locked balance does not cover. The holds ahead of it have been
// released by then; it and every hold queued behind it are exactly as they were,
// and the sweep reports the plain locked-balance error and no result. Repeated
// sweeps end at the same place.
func TestSweepExpiredEndsAtUnreleasableHoldByDefault(t *testing.T) {
	f := newDefaultSweepFixture(t)
	ahead, aheadID := f.hold(1, 300)
	stuckPayer, stuckID := f.hold(2, 600)
	behind, behindID := f.hold(3, 200)
	f.setLocked(stuckPayer, 100)

	for i := 0; i < 3; i++ {
		voided, err := f.engine.SweepExpired(f.sweepAt().Add(time.Duration(i) * time.Minute))
		if err == nil || err.Error() != "pos: locked balance inconsistent" || !errors.Is(err, errLockedBalanceInconsistent) {
			t.Fatalf("sweep %d error = %v, want the locked-balance error", i, err)
		}
		if errors.Is(err, ErrSweepSkipped) {
			t.Fatalf("sweep %d error %v must not be reported as a skip by default", i, err)
		}
		if voided != nil {
			t.Fatalf("sweep %d returned %d voided records next to the error", i, len(voided))
		}
		if got := f.status(aheadID); got != AuthorizationStatusExpired {
			t.Fatalf("sweep %d: hold ahead of the unreleasable one has status %v, want expired", i, got)
		}
		f.requireAccount(ahead, 1_000, 0)
		if got := f.status(stuckID); got != AuthorizationStatusPending {
			t.Fatalf("sweep %d: unreleasable hold status %v, want pending", i, got)
		}
		f.requireAccount(stuckPayer, 400, 100)
		if got := f.status(behindID); got != AuthorizationStatusPending {
			t.Fatalf("sweep %d: hold behind the unreleasable one has status %v, want pending", i, got)
		}
		f.requireAccount(behind, 800, 200)
		f.requirePending(stuckID, behindID)
		if len(f.events.voided) != 1 || f.events.voided[0] != aheadID {
			t.Fatalf("sweep %d: auto-void events = %x, want only the hold ahead", i, f.events.voided)
		}
	}
}

// Skipping can be switched off again, and then the lifecycle behaves exactly as
// one that was never told anything.
func TestSweepExpiredSkippingCanBeSwitchedOff(t *testing.T) {
	f := newSweepFixture(t)
	f.engine.SetSweepSkipsUnreleasable(false)
	stuckPayer, stuckID := f.hold(1, 600)
	_, healthyID := f.hold(2, 300)
	f.setLocked(stuckPayer, 100)

	voided, err := f.engine.SweepExpired(f.sweepAt())
	if err == nil || errors.Is(err, ErrSweepSkipped) || !errors.Is(err, errLockedBalanceInconsistent) {
		t.Fatalf("sweep error = %v, want the plain locked-balance error", err)
	}
	if voided != nil {
		t.Fatalf("sweep returned %d voided records next to the error", len(voided))
	}
	f.requirePending(stuckID, healthyID)
}
