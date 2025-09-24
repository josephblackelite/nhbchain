package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

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
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:      big.NewInt(0),
		BalanceZNHB:     big.NewInt(0),
		Stake:           big.NewInt(stake),
		EngagementScore: engagement,
	}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("set account: %v", err)
	}
	return addr
}

func TestEpochSnapshotDeterminism(t *testing.T) {
	sp := newEpochStateProcessor(t)

	a := seedValidator(t, sp, 2000, 10)
	b := seedValidator(t, sp, 3000, 5)
	c := seedValidator(t, sp, 2500, 12)

	if err := sp.ProcessBlockLifecycle(1, time.Now().Unix()); err != nil {
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

	a := seedValidator(t, sp, 2000, 0)
	b := seedValidator(t, sp, 2000, 0)

	if bytesCompare(a, b) > 0 {
		a, b = b, a
	}

	if err := sp.ProcessBlockLifecycle(1, time.Now().Unix()); err != nil {
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

	eligible1 := seedValidator(t, sp, 2000, 10)
	eligible2 := seedValidator(t, sp, 3000, 5)
	_ = seedValidator(t, sp, 500, 100) // below minimum stake

	if err := sp.ProcessBlockLifecycle(1, time.Now().Unix()); err != nil {
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
	for addr := range sp.ValidatorSet {
		stake := sp.ValidatorSet[addr]
		if stake.Cmp(big.NewInt(MINIMUM_STAKE)) < 0 {
			t.Fatalf("validator with insufficient stake persisted: %s", addr)
		}
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
