package pos

import (
	"math/big"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/pos"
	"nhbchain/observability"
)

// sweepSkipScenario holds an unreleasable hold at the head of the pending
// queue and two healthy holds behind it, all expired at the sweep time.
type sweepSkipScenario struct {
	sp        *core.StateProcessor
	lifecycle *pos.Lifecycle
	sweepTime time.Time
	stuck     [20]byte
	stuckID   [32]byte
	healthy   [2][20]byte
	healthyID [2][32]byte
}

func newSweepSkipScenario(t *testing.T) *sweepSkipScenario {
	t.Helper()
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	seed := func(addr [20]byte, balance int64) {
		t.Helper()
		if err := manager.PutAccount(addr[:], &types.Account{
			BalanceNHB:        big.NewInt(0),
			BalanceZNHB:       big.NewInt(balance),
			LockedZNHB:        big.NewInt(0),
			Stake:             big.NewInt(0),
			StakeShares:       big.NewInt(0),
			CollateralBalance: big.NewInt(0),
		}); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	s := &sweepSkipScenario{sp: sp}
	s.stuck[0], s.stuck[19] = 0xa1, 0x01
	s.healthy[0][0], s.healthy[0][19] = 0xa2, 0x02
	s.healthy[1][0], s.healthy[1][19] = 0xa3, 0x03
	var merchant [20]byte
	merchant[0], merchant[19] = 0xb1, 0x09
	for _, addr := range [][20]byte{s.stuck, s.healthy[0], s.healthy[1], merchant} {
		seed(addr, 25_000)
	}

	s.lifecycle = pos.NewLifecycle(manager)
	s.lifecycle.SetEmitter(eventForwarder{sp: sp})
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	s.lifecycle.SetNowFunc(func() time.Time { return baseTime })
	expiry := uint64(baseTime.Add(time.Hour).Unix())

	authorize := func(payer [20]byte, amount int64) [32]byte {
		t.Helper()
		auth, err := s.lifecycle.Authorize(payer, merchant, big.NewInt(amount), expiry, nil)
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		return auth.ID
	}
	s.stuckID = authorize(s.stuck, 5_000)
	s.healthyID[0] = authorize(s.healthy[0], 4_000)
	s.healthyID[1] = authorize(s.healthy[1], 3_000)

	// The stuck payer's locked balance is now below its hold.
	acc, err := manager.GetAccount(s.stuck[:])
	if err != nil {
		t.Fatalf("load stuck payer: %v", err)
	}
	acc.LockedZNHB = big.NewInt(1_000)
	if err := manager.PutAccount(s.stuck[:], acc); err != nil {
		t.Fatalf("lower stuck payer's locked balance: %v", err)
	}
	s.sweepTime = baseTime.Add(2 * time.Hour)
	return s
}

// requireOutcome checks the state after a sweep that carried on past the
// unreleasable hold: both healthy holds are released, the unreleasable one and
// its payer are as they were.
func (s *sweepSkipScenario) requireOutcome(t *testing.T) {
	t.Helper()
	s.requireOutcomeOn(t, s.sp)
}

func (s *sweepSkipScenario) requireOutcomeOn(t *testing.T, sp *core.StateProcessor) {
	t.Helper()
	lifecycle := pos.NewLifecycle(nhbstate.NewManager(sp.Trie))
	for i, id := range s.healthyID {
		auth, err := lifecycle.Get(id)
		if err != nil {
			t.Fatalf("get healthy authorization %d: %v", i, err)
		}
		if auth.Status != pos.AuthorizationStatusExpired {
			t.Fatalf("healthy authorization %d status %v, want expired", i, auth.Status)
		}
		acc, err := sp.GetAccount(s.healthy[i][:])
		if err != nil {
			t.Fatalf("get healthy payer %d: %v", i, err)
		}
		if acc.LockedZNHB.Sign() != 0 || acc.BalanceZNHB.Cmp(big.NewInt(25_000)) != 0 {
			t.Fatalf("healthy payer %d balance %s locked %s, want 25000 / 0", i, acc.BalanceZNHB, acc.LockedZNHB)
		}
	}
	s.requireStuckHoldUntouchedOn(t, sp, lifecycle)
}

// requireStalled checks the state after a sweep that ended at the unreleasable
// hold: nothing was released, so both healthy holds are still pending and
// locked next to the unreleasable one.
func (s *sweepSkipScenario) requireStalled(t *testing.T) {
	t.Helper()
	s.requireStalledOn(t, s.sp)
}

func (s *sweepSkipScenario) requireStalledOn(t *testing.T, sp *core.StateProcessor) {
	t.Helper()
	lifecycle := pos.NewLifecycle(nhbstate.NewManager(sp.Trie))
	locked := [2]int64{4_000, 3_000}
	for i, id := range s.healthyID {
		auth, err := lifecycle.Get(id)
		if err != nil {
			t.Fatalf("get healthy authorization %d: %v", i, err)
		}
		if auth.Status != pos.AuthorizationStatusPending {
			t.Fatalf("healthy authorization %d status %v, want pending", i, auth.Status)
		}
		acc, err := sp.GetAccount(s.healthy[i][:])
		if err != nil {
			t.Fatalf("get healthy payer %d: %v", i, err)
		}
		if acc.LockedZNHB.Cmp(big.NewInt(locked[i])) != 0 || acc.BalanceZNHB.Cmp(big.NewInt(25_000-locked[i])) != 0 {
			t.Fatalf("healthy payer %d balance %s locked %s, want %d / %d", i, acc.BalanceZNHB, acc.LockedZNHB, 25_000-locked[i], locked[i])
		}
	}
	s.requireStuckHoldUntouchedOn(t, sp, lifecycle)
}

func (s *sweepSkipScenario) requireStuckHoldUntouchedOn(t *testing.T, sp *core.StateProcessor, lifecycle *pos.Lifecycle) {
	t.Helper()
	stuck, err := lifecycle.Get(s.stuckID)
	if err != nil {
		t.Fatalf("get stuck authorization: %v", err)
	}
	if stuck.Status != pos.AuthorizationStatusPending {
		t.Fatalf("stuck authorization status %v, want pending", stuck.Status)
	}
	acc, err := sp.GetAccount(s.stuck[:])
	if err != nil {
		t.Fatalf("get stuck payer: %v", err)
	}
	if acc.LockedZNHB.Cmp(big.NewInt(1_000)) != 0 || acc.BalanceZNHB.Cmp(big.NewInt(20_000)) != 0 {
		t.Fatalf("stuck payer balance %s locked %s, want 20000 / 1000", acc.BalanceZNHB, acc.LockedZNHB)
	}
}

// finalize runs one block's end-of-block work at the given height and time.
func finalize(sp *core.StateProcessor, height uint64, at time.Time) {
	sp.BeginBlock(height, at)
	sp.FinalizeBlock()
	sp.EndBlock()
}

func stateRoot(sp *core.StateProcessor) string { return sp.Trie.Hash().Hex() }

// sweepActivationHeight is the activation height the tests below use; the
// scenario's sweep runs in a block at this height unless a test says otherwise.
const sweepActivationHeight = 7

// With no activation height set (the default) the block's sweep ends at the
// unreleasable hold at every height: the holds queued behind it stay pending
// and locked, the block leaves the state exactly as it found it, and nothing is
// counted or announced as released.
func TestFinalizeBlockStopsAtUnreleasableHoldWhenActivationIsNotSet(t *testing.T) {
	s := newSweepSkipScenario(t)
	metrics := observability.POSLifecycle()
	before := testutil.ToFloat64(metrics.AuthExpiredCounter())
	eventsBefore := len(s.sp.Events())
	root := stateRoot(s.sp)

	for _, height := range []uint64{1, sweepActivationHeight, 8, 1 << 40} {
		finalize(s.sp, height, s.sweepTime.Add(time.Duration(height%60)*time.Second))
		s.requireStalled(t)
		if got := stateRoot(s.sp); got != root {
			t.Fatalf("block %d changed the state root: %s -> %s", height, root, got)
		}
	}
	if diff := testutil.ToFloat64(metrics.AuthExpiredCounter()) - before; diff != 0 {
		t.Fatalf("expired metric moved by %f, want 0", diff)
	}
	if got := len(s.sp.Events()); got != eventsBefore {
		t.Fatalf("emitted %d events, want none", got-eventsBefore)
	}
}

// The direct sweep call, with nothing set, reports the locked-balance error
// the earlier code reported, and counts nothing.
func TestSweepExpiredPOSAuthorizationsStopsAtUnreleasableHoldWhenActivationIsNotSet(t *testing.T) {
	s := newSweepSkipScenario(t)
	metrics := observability.POSLifecycle()
	before := testutil.ToFloat64(metrics.AuthExpiredCounter())

	s.sp.BeginBlock(sweepActivationHeight, s.sweepTime)
	count, err := s.sp.SweepExpiredPOSAuthorizations(s.sweepTime)
	s.sp.EndBlock()
	if err == nil || err.Error() != "pos: locked balance inconsistent" {
		t.Fatalf("sweep error = %v, want the locked-balance error", err)
	}
	if count != 0 {
		t.Fatalf("sweep reported %d released holds, want 0", count)
	}
	if diff := testutil.ToFloat64(metrics.AuthExpiredCounter()) - before; diff != 0 {
		t.Fatalf("expired metric moved by %f, want 0", diff)
	}
	s.requireStalled(t)
}

// FinalizeBlock, which runs the expiry sweep every block, releases the holds
// queued behind an unreleasable one instead of stopping at it, from the block
// at the activation height.
func TestFinalizeBlockSweepsPastUnreleasableHold(t *testing.T) {
	s := newSweepSkipScenario(t)
	s.sp.SetPOSSweepSkipActivationHeight(sweepActivationHeight)
	metrics := observability.POSLifecycle()
	before := testutil.ToFloat64(metrics.AuthExpiredCounter())
	eventsBefore := len(s.sp.Events())

	finalize(s.sp, sweepActivationHeight, s.sweepTime)

	s.requireOutcome(t)
	if diff := testutil.ToFloat64(metrics.AuthExpiredCounter()) - before; diff != 2 {
		t.Fatalf("expired metric moved by %f, want 2 (the two released holds)", diff)
	}
	autoVoided := 0
	for _, evt := range s.sp.Events()[eventsBefore:] {
		if evt.Type == events.TypePosAuthAutoVoided {
			autoVoided++
		}
	}
	if autoVoided != 2 {
		t.Fatalf("emitted %d auto-void events, want 2", autoVoided)
	}

	// Later blocks keep retrying the skipped hold without changing anything.
	finalize(s.sp, sweepActivationHeight+1, s.sweepTime.Add(time.Minute))
	s.requireOutcome(t)
}

// The last block before the activation height still ends at the unreleasable
// hold and leaves the state alone; the block at the activation height is the
// first to release the holds behind it.
func TestFinalizeBlockSweepSkipStartsAtTheActivationHeight(t *testing.T) {
	s := newSweepSkipScenario(t)
	s.sp.SetPOSSweepSkipActivationHeight(sweepActivationHeight)
	root := stateRoot(s.sp)

	finalize(s.sp, sweepActivationHeight-1, s.sweepTime)
	s.requireStalled(t)
	if got := stateRoot(s.sp); got != root {
		t.Fatalf("the block before the activation height changed the state root: %s -> %s", root, got)
	}

	finalize(s.sp, sweepActivationHeight, s.sweepTime.Add(time.Second))
	s.requireOutcome(t)
	if got := stateRoot(s.sp); got == root {
		t.Fatalf("the block at the activation height left the state root unchanged")
	}
}

// Activation at the first height is expressible and releases from the first
// block; the sweep outside a block never counts as past the activation height.
func TestSweepSkipActivationFirstHeightAndOutsideABlock(t *testing.T) {
	s := newSweepSkipScenario(t)
	s.sp.SetPOSSweepSkipActivationHeight(1)

	count, err := s.sp.SweepExpiredPOSAuthorizations(s.sweepTime)
	if err == nil || err.Error() != "pos: locked balance inconsistent" || count != 0 {
		t.Fatalf("sweep outside a block = (%d, %v), want (0, the locked-balance error)", count, err)
	}
	s.requireStalled(t)

	finalize(s.sp, 1, s.sweepTime)
	s.requireOutcome(t)
}

// A copy of the state processor, which is what blocks are built, validated and
// committed on, keeps the activation height: a copy of one that has it releases
// the holds in its block, a copy of one that does not still ends at the
// unreleasable hold, and neither touches the processor it was copied from.
func TestStateProcessorCopyKeepsSweepSkipActivationHeight(t *testing.T) {
	s := newSweepSkipScenario(t)
	s.sp.SetPOSSweepSkipActivationHeight(sweepActivationHeight)
	root := stateRoot(s.sp)

	skipping, err := s.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	finalize(skipping, sweepActivationHeight, s.sweepTime)
	s.requireOutcomeOn(t, skipping)

	s.sp.SetPOSSweepSkipActivationHeight(0)
	stopping, err := s.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	finalize(stopping, sweepActivationHeight, s.sweepTime)
	s.requireStalledOn(t, stopping)

	s.requireStalled(t)
	if got := stateRoot(s.sp); got != root {
		t.Fatalf("running blocks on copies changed the original: %s -> %s", root, got)
	}
}

// The direct sweep call reports the holds it did release and no error, in a
// block at or past the activation height.
func TestSweepExpiredPOSAuthorizationsCountsReleasedHoldsPastUnreleasableOne(t *testing.T) {
	s := newSweepSkipScenario(t)
	s.sp.SetPOSSweepSkipActivationHeight(sweepActivationHeight)
	metrics := observability.POSLifecycle()
	before := testutil.ToFloat64(metrics.AuthExpiredCounter())

	s.sp.BeginBlock(sweepActivationHeight, s.sweepTime)
	defer s.sp.EndBlock()
	count, err := s.sp.SweepExpiredPOSAuthorizations(s.sweepTime)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if count != 2 {
		t.Fatalf("sweep released %d holds, want 2", count)
	}
	if diff := testutil.ToFloat64(metrics.AuthExpiredCounter()) - before; diff != 2 {
		t.Fatalf("expired metric moved by %f, want 2", diff)
	}
	s.requireOutcome(t)

	count, err = s.sp.SweepExpiredPOSAuthorizations(s.sweepTime)
	if err != nil || count != 0 {
		t.Fatalf("second sweep = (%d, %v), want (0, nil)", count, err)
	}
}

// Two processors that run the same blocks over the same state with the same
// setting end in the same state root. The root with the setting at the block's
// height differs from the root without it; a setting above the block's height
// gives the root of no setting at all.
func TestFinalizeBlockSweepPastUnreleasableHoldIsDeterministic(t *testing.T) {
	rootFor := func(activation uint64) (string, string) {
		roots := make([]string, 0, 2)
		for i := 0; i < 2; i++ {
			s := newSweepSkipScenario(t)
			s.sp.SetPOSSweepSkipActivationHeight(activation)
			finalize(s.sp, sweepActivationHeight, s.sweepTime)
			roots = append(roots, stateRoot(s.sp))
		}
		return roots[0], roots[1]
	}
	for _, activation := range []uint64{0, sweepActivationHeight, sweepActivationHeight + 1} {
		first, second := rootFor(activation)
		if first != second {
			t.Fatalf("activation %d: state roots differ: %s vs %s", activation, first, second)
		}
	}
	unset, _ := rootFor(0)
	active, _ := rootFor(sweepActivationHeight)
	later, _ := rootFor(sweepActivationHeight + 1)
	if unset == active {
		t.Fatalf("the sweep skipping at the block's height left the state root as without it: %s", unset)
	}
	if unset != later {
		t.Fatalf("an activation height above the block changed the state root: %s vs %s", unset, later)
	}
}
