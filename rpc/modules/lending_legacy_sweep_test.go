package modules

import (
	"testing"

	nhbstate "nhbchain/core/state"
)

// A read that reconciles legacy lending positions lists every account. Once a
// lending transaction has completed that sweep for a pool the read skips it:
// every account that held a position was migrated by it. A pool that has not
// been swept still gets the full pass.
func TestLendingReadSkipsACompletedLegacySweep(t *testing.T) {
	node := newLendingTestNode(t)
	module := NewLendingModule(node)

	var addr [20]byte
	for i := range addr {
		addr[i] = 0xC7
	}
	seedLegacyLendingAccount(t, node, addr)

	migrated := func(poolID string) bool {
		var found bool
		err := node.WithState(func(manager *nhbstate.Manager) error {
			if err := module.reconcileLegacyPoolStateInto(manager, poolID); err != nil {
				return err
			}
			_, ok, err := manager.LendingGetUserAccount(poolID, addr)
			found = ok
			return err
		})
		if err != nil {
			t.Fatalf("reconcile pool %q: %v", poolID, err)
		}
		return found
	}

	if err := node.WithState(func(manager *nhbstate.Manager) error {
		return manager.LendingMarkLegacyReconciled("swept")
	}); err != nil {
		t.Fatalf("mark pool swept: %v", err)
	}
	if migrated("swept") {
		t.Fatalf("a read swept every account again for a pool whose sweep had completed")
	}
	if !migrated("fresh") {
		t.Fatalf("a pool that had not been swept did not get its pass")
	}
}
