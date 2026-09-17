package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

const testEpochTimestamp int64 = 1_700_000_100

func newEpochStateProcessor(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}
	cfg := sp.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 1
	cfg.SnapshotHistory = 16
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}
	// Tests in this file seed validators with small raw stake numbers
	// (thousands, not the real 18-decimal ZNHB wei scale) to keep test
	// arithmetic simple -- they are testing epoch selection/rotation logic
	// in isolation, not real-world stake magnitudes. Explicitly set a small
	// governed minimum here so these tests stay correct and independent of
	// governance.DefaultMinimumValidatorStake()'s real production value
	// (10,000 ZNHB in Wei), which these small raw stakes would never clear.
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1000)); err != nil {
		t.Fatalf("set test minimum stake: %v", err)
	}
	return sp
}

func seedValidator(t *testing.T, sp *StateProcessor, stake int64, engagement uint64) []byte {
	// validatorReadyForActivation unconditionally rejects a zero heartbeat
	// (see epochs.go), so a heartbeat of 0 here would make every validator
	// ineligible for epoch weight computation regardless of stake. Callers
	// that specifically need to exercise "no heartbeat yet" behavior should
	// use seedValidatorWithHeartbeat(..., 0) directly and go through a
	// liveness-fallback path that doesn't require a fresh heartbeat, not
	// this general-purpose helper.
	return seedValidatorWithHeartbeat(t, sp, stake, engagement, uint64(testEpochTimestamp))
}

func seedValidatorWithHeartbeat(t *testing.T, sp *StateProcessor, stake int64, engagement uint64, heartbeat uint64) []byte {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(stake),
		EngagementScore:         engagement,
		EngagementLastHeartbeat: heartbeat,
		// This suite tests stake-threshold/heartbeat/rotation logic, not the
		// item-1 explicit-registration gate itself (see
		// core/validator_registration_test.go for that coverage) -- every
		// validator seeded here is registered so existing expectations about
		// eligibility/selection stay meaningful.
		ValidatorRegistered: true,
	}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("set account: %v", err)
	}
	return addr
}

func TestEpochSnapshotDeterminism(t *testing.T) {
	sp := newEpochStateProcessor(t)

	a := seedValidator(t, sp, 20000, 10)
	b := seedValidator(t, sp, 30000, 5)
	c := seedValidator(t, sp, 25000, 12)

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}

	snapshot, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected epoch snapshot")
	}
	if len(snapshot.Weights) != 3 {
		t.Fatalf("expected 3 weight entries, got %d", len(snapshot.Weights))
	}

	// Composite weights should be sorted in descending order.
	if snapshot.Weights[0].Composite.Cmp(snapshot.Weights[1].Composite) < 0 ||
		snapshot.Weights[1].Composite.Cmp(snapshot.Weights[2].Composite) < 0 {
		t.Fatalf("weights not sorted descending: %v", snapshot.Weights)
	}

	// Ensure repeated retrieval is deterministic.
	byEpoch, ok := sp.EpochSnapshot(snapshot.Epoch)
	if !ok {
		t.Fatalf("missing snapshot by epoch")
	}
	for i := range snapshot.Weights {
		if snapshot.Weights[i].Composite.Cmp(byEpoch.Weights[i].Composite) != 0 {
			t.Fatalf("composite mismatch at index %d", i)
		}
	}

	if len(sp.EpochHistory()) != 1 {
		t.Fatalf("expected 1 snapshot in history, got %d", len(sp.EpochHistory()))
	}

	if snapshot.Weights[0].Composite.Cmp(snapshot.Weights[1].Composite) == 0 &&
		bytesEqual(snapshot.Weights[0].Address, snapshot.Weights[1].Address) {
		t.Fatalf("tie-breaking failed to produce deterministic order")
	}

	// Ensure addresses tracked.
	expected := [][]byte{b, c, a}
	for i, addr := range expected {
		if !bytesEqual(snapshot.Weights[i].Address, addr) {
			t.Fatalf("unexpected ordering at %d", i)
		}
	}
}

func TestEpochTieBreaks(t *testing.T) {
	sp := newEpochStateProcessor(t)

	cfg := sp.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 0
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}

	a := seedValidator(t, sp, 20000, 0)
	b := seedValidator(t, sp, 20000, 0)

	if bytesCompare(a, b) > 0 {
		a, b = b, a
	}

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}

	snapshot, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected snapshot")
	}
	if len(snapshot.Weights) != 2 {
		t.Fatalf("expected 2 weights, got %d", len(snapshot.Weights))
	}
	if !bytesEqual(snapshot.Weights[0].Address, a) || !bytesEqual(snapshot.Weights[1].Address, b) {
		t.Fatalf("tie-break ordering incorrect")
	}
}

func TestEpochRotationRespectsMinimumStake(t *testing.T) {
	sp := newEpochStateProcessor(t)
	cfg := sp.EpochConfig()
	cfg.RotationEnabled = true
	cfg.MaxValidators = 2
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	heartbeat := uint64(testEpochTimestamp)
	eligible1 := seedValidatorWithHeartbeat(t, sp, 20000, 10, heartbeat)
	eligible2 := seedValidatorWithHeartbeat(t, sp, 30000, 5, heartbeat)
	_ = seedValidatorWithHeartbeat(t, sp, 5000, 100, heartbeat) // below minimum stake

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}

	snapshot, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected snapshot")
	}
	if len(snapshot.Selected) != 2 {
		t.Fatalf("expected 2 selected validators, got %d", len(snapshot.Selected))
	}

	set := map[string]struct{}{}
	for _, addr := range snapshot.Selected {
		set[string(addr)] = struct{}{}
	}
	if _, ok := set[string(eligible1)]; !ok {
		t.Fatalf("validator 1 missing from rotation")
	}
	if _, ok := set[string(eligible2)]; !ok {
		t.Fatalf("validator 2 missing from rotation")
	}
	if _, ok := set[string(eligible1)]; !ok || len(sp.ValidatorSet) != 2 {
		t.Fatalf("expected validator set to contain exactly selected validators")
	}
	if _, ok := sp.ValidatorSet[string(eligible1)]; !ok {
		t.Fatalf("validator 1 not in active set")
	}
	if _, ok := sp.ValidatorSet[string(eligible2)]; !ok {
		t.Fatalf("validator 2 not in active set")
	}
	// This test's helper explicitly sets a small governed minimum (see
	// newEpochStateProcessor) rather than relying on
	// governance.DefaultMinimumValidatorStake()'s real production value
	// (10,000 ZNHB in Wei) -- read back the actual governed threshold that
	// was enforced, not the unrelated production default constant.
	manager := nhbstate.NewManager(sp.Trie)
	threshold, err := manager.MinimumValidatorStake()
	if err != nil {
		t.Fatalf("read minimum stake: %v", err)
	}
	for addr := range sp.ValidatorSet {
		stake := sp.ValidatorSet[addr]
		if stake.Cmp(threshold) < 0 {
			t.Fatalf("validator with insufficient stake persisted: %s", addr)
		}
	}
}

func TestEpochSelectionUpdatesWithGovernedMinimumStake(t *testing.T) {
	sp := newEpochStateProcessor(t)

	heartbeat := uint64(testEpochTimestamp)
	high := seedValidatorWithHeartbeat(t, sp, 50000, 10, heartbeat)
	mid := seedValidatorWithHeartbeat(t, sp, 28000, 5, heartbeat)
	low := seedValidatorWithHeartbeat(t, sp, 15000, 7, heartbeat)

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}
	snapshot, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected initial snapshot")
	}
	if len(snapshot.Selected) != 3 {
		t.Fatalf("expected all validators selected initially, got %d", len(snapshot.Selected))
	}

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(35000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	if err := sp.ProcessBlockLifecycle(2, testEpochTimestamp+1); err != nil {
		t.Fatalf("process block after parameter change: %v", err)
	}
	updated, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected updated snapshot")
	}
	if len(updated.Selected) != 1 {
		t.Fatalf("expected exactly one validator selected, got %d", len(updated.Selected))
	}
	if !bytesEqual(updated.Selected[0], high) {
		t.Fatalf("expected highest stake validator selected")
	}
	if _, ok := sp.ValidatorSet[string(mid)]; ok {
		t.Fatalf("mid stake validator should not remain active after threshold increase")
	}
	if _, ok := sp.ValidatorSet[string(low)]; ok {
		t.Fatalf("low stake validator should not remain active after threshold increase")
	}
	if len(sp.ValidatorSet) != 1 {
		t.Fatalf("expected validator set to contain only the qualifying validator")
	}
}

func TestEpochActivationRequiresRecentHeartbeat(t *testing.T) {
	sp := newEpochStateProcessor(t)
	cfg := sp.EpochConfig()
	cfg.RotationEnabled = true
	cfg.MaxValidators = 10
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	ready := seedValidatorWithHeartbeat(t, sp, 40000, 10, uint64(testEpochTimestamp))
	_ = seedValidatorWithHeartbeat(t, sp, 45000, 20, 0)
	_ = seedValidatorWithHeartbeat(t, sp, 50000, 30, uint64(testEpochTimestamp-int64((20*time.Minute).Seconds())))

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}

	snapshot, ok := sp.LatestEpochSnapshot()
	if !ok {
		t.Fatalf("expected snapshot")
	}
	if len(snapshot.Selected) != 1 {
		t.Fatalf("expected exactly one heartbeat-ready validator, got %d", len(snapshot.Selected))
	}
	if !bytesEqual(snapshot.Selected[0], ready) {
		t.Fatalf("unexpected selected validator")
	}
}

func TestNonRotatingActivationWaitsUntilNextEpoch(t *testing.T) {
	sp := newEpochStateProcessor(t)
	addr := seedValidatorWithHeartbeat(t, sp, 40000, 10, uint64(testEpochTimestamp))
	if _, ok := sp.ValidatorSet[string(addr)]; ok {
		t.Fatalf("candidate should not enter active validator set before epoch finalization")
	}
	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}
	if _, ok := sp.ValidatorSet[string(addr)]; !ok {
		t.Fatalf("candidate should enter active validator set at epoch boundary")
	}
}

func TestEpochRotationRetainsPreviousValidatorsWhenHeartbeatSelectionIsEmpty(t *testing.T) {
	sp := newEpochStateProcessor(t)
	cfg := sp.EpochConfig()
	cfg.RotationEnabled = true
	cfg.MaxValidators = 10
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	addr := seedValidatorWithHeartbeat(t, sp, 40000, 10, uint64(testEpochTimestamp))
	sp.ValidatorSet[string(addr)] = big.NewInt(40000)

	// Make the heartbeat STALE (outside the readiness grace period) rather
	// than zeroing it out entirely -- a stale-but-nonzero heartbeat models a
	// real validator that has run before and is just having a liveness
	// blip, which the fallback is meant to tolerate. A zero heartbeat means
	// "has never once proven it runs a node," which is the phantom-
	// validator case the fallback must NOT recover (see
	// TestFallbackValidatorSetExcludesNeverHeartbeatedAddress).
	account, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	account.EngagementLastHeartbeat = uint64(testEpochTimestamp - 3600)
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("update account: %v", err)
	}

	if err := sp.ProcessBlockLifecycle(1, testEpochTimestamp); err != nil {
		t.Fatalf("process block: %v", err)
	}
	if _, ok := sp.ValidatorSet[string(addr)]; !ok {
		t.Fatalf("expected existing validator to remain active when epoch selection is empty")
	}
}

// TestFallbackValidatorSetExcludesNeverHeartbeatedAddress is the regression
// test for the production incident this fix addresses: an address that
// accumulated enough .Stake to be "eligible" (e.g. via an accidental
// undirected self-stake) but has NEVER sent a single heartbeat must not be
// resurrected into the active validator set by the empty-selection fallback,
// even though it would otherwise have enough stake and even though the
// fallback deliberately skips the heartbeat-recency check for validators
// that HAVE heartbeated before.
func TestFallbackValidatorSetExcludesNeverHeartbeatedAddress(t *testing.T) {
	sp := newEpochStateProcessor(t)

	real := seedValidatorWithHeartbeat(t, sp, 40000, 10, uint64(testEpochTimestamp-3600))
	sp.ValidatorSet[string(real)] = big.NewInt(40000)

	phantom := seedValidatorWithHeartbeat(t, sp, 40000, 0, 0)
	sp.EligibleValidators[string(phantom)] = big.NewInt(40000)

	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	fallback, err := sp.fallbackValidatorSet(minStake)
	if err != nil {
		t.Fatalf("fallback validator set: %v", err)
	}
	if _, ok := fallback[string(real)]; !ok {
		t.Fatalf("expected previously-active validator with heartbeat history to be recoverable")
	}
	if _, ok := fallback[string(phantom)]; ok {
		t.Fatalf("never-heartbeated address must not be swept into the fallback validator set")
	}
}

func TestEnsureValidatorSetLivenessRecoversFromEligibleValidators(t *testing.T) {
	sp := newEpochStateProcessor(t)
	addr := seedValidator(t, sp, 40000, 10)
	sp.ValidatorSet = map[string]*big.Int{}
	sp.EligibleValidators[string(addr)] = big.NewInt(40000)

	if err := sp.ensureValidatorSetLiveness(time.Unix(testEpochTimestamp, 0).UTC()); err != nil {
		t.Fatalf("ensure validator set liveness: %v", err)
	}
	if _, ok := sp.ValidatorSet[string(addr)]; !ok {
		t.Fatalf("expected eligible validator to repopulate empty active set")
	}
}

func bytesEqual(a, b []byte) bool {
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

func bytesCompare(a, b []byte) int {
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	for i := 0; i < min; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
