package core

import (
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/params"
)

// TestStakingPauseOnChain_ReadOnly proves StakingPauseOnChain (which
// replaced ensureStakingPauseCleared's direct trie write, cmd/nhb/main.go
// and cmd/consensusd/main.go) only ever reads the persisted value, never
// mutates it -- the whole point of the fix: a validator's local config must
// never unilaterally overwrite this consensus-shared flag.
func TestStakingPauseOnChain_ReadOnly(t *testing.T) {
	node := newTestNode(t)

	paused, err := node.StakingPauseOnChain()
	if err != nil {
		t.Fatalf("read staking pause (default): %v", err)
	}
	if paused {
		t.Fatalf("expected staking to default to unpaused")
	}

	if err := node.WithState(func(m *nhbstate.Manager) error {
		store := params.NewStore(m)
		current, err := store.Pauses()
		if err != nil {
			return err
		}
		current.Staking = true
		return store.SetPauses(current)
	}); err != nil {
		t.Fatalf("seed paused state: %v", err)
	}

	paused, err = node.StakingPauseOnChain()
	if err != nil {
		t.Fatalf("read staking pause (after seeding paused): %v", err)
	}
	if !paused {
		t.Fatalf("expected StakingPauseOnChain to report the persisted paused=true value")
	}

	// Calling it again must not have changed anything -- a read-only check,
	// unlike the old ensureStakingPauseCleared which would have forced this
	// back to false right here.
	paused, err = node.StakingPauseOnChain()
	if err != nil {
		t.Fatalf("read staking pause (second read): %v", err)
	}
	if !paused {
		t.Fatalf("expected the paused value to remain true across repeated reads -- StakingPauseOnChain must never mutate state")
	}
}
