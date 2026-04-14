package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
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
	return sp
}

func seedValidator(t *testing.T, sp *StateProcessor, stake int64, engagement uint64) []byte {
	return seedValidatorWithHeartbeat(t, sp, stake, engagement, 0)
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
	threshold := governance.DefaultMinimumValidatorStake()
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

	// Clear the heartbeat so the next epoch selection would otherwise be empty.
	account, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	account.EngagementLastHeartbeat = 0
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
