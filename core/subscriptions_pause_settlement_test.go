package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/native/subscriptions"
)

// PL-R5T-81: the per-block settlement honours the subscriptions pause, from a
// configured height.

const subsPauseHeight = 1 // every block of these tests runs at height >= 1

var pauseDay0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func pausedSubscriptionsFixture(t *testing.T, activation uint64) (*sameDayFixture, subscriptions.SubscriptionID) {
	t.Helper()
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	fx.sp.SetSubscriptionsPauseSettlement(activation)
	fx.setBalance(fx.payer, 10_000)
	id := fx.subscribe(pauseDay0, 100, 30*86400)
	return fx, id
}

func (fx *sameDayFixture) pauseSubscriptions(paused bool) {
	fx.sp.SetPauseView(pauseViewStub{modules: map[string]bool{"subscriptions": paused}})
}

// With the setting off a paused module is still billed: the defect.
func TestSubscriptionsPausedSettlementWithTheSettingOffStillCharges(t *testing.T) {
	fx, id := pausedSubscriptionsFixture(t, 0)
	fx.pauseSubscriptions(true)
	fx.settle(pauseDay0)
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("setting off: payer balance %s, want 9900 (the legacy settlement ignores the pause)", got)
	}
	if n := len(fx.charges(id)); n != 1 {
		t.Fatalf("setting off: %d charges recorded, want 1", n)
	}
}

func TestSubscriptionsPausedSettlementWithTheSettingOnChargesNobody(t *testing.T) {
	fx, id := pausedSubscriptionsFixture(t, subsPauseHeight)
	fx.pauseSubscriptions(true)
	fx.settle(pauseDay0)
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("paused: payer balance %s, want 10000", got)
	}
	if got := fx.balance(fx.merchant); got.Sign() != 0 {
		t.Fatalf("paused: the merchant was paid %s", got)
	}
	if n := len(fx.charges(id)); n != 0 {
		t.Fatalf("paused: %d charge attempts recorded, want none", n)
	}
	if due := fx.dueOn(pauseDay0); len(due) != 1 || due[0] != id {
		t.Fatalf("paused: the due entry did not stay where it was: %v", due)
	}
	if _, has, err := fx.manager.SubscriptionsLastProcessedDay(); err != nil || has {
		t.Fatalf("paused: a watermark was written (has=%v err=%v)", has, err)
	}
	// A paused subscription that was already past due is not failed or cancelled by the pause.
	sub, _ := fx.registry.GetSubscription(id)
	if sub.Status != subscriptions.SubscriptionStatusActive {
		t.Fatalf("paused: the subscription status changed to %v", sub.Status)
	}
}

// Days of pause are caught up on unpause with one charge per due subscription.
func TestSubscriptionsAMultiDayPauseIsCaughtUpOnceOnUnpause(t *testing.T) {
	fx, id := pausedSubscriptionsFixture(t, subsPauseHeight)
	fx.pauseSubscriptions(true)
	for d := 0; d < 4; d++ {
		fx.settle(pauseDay0.Add(time.Duration(d) * 24 * time.Hour))
	}
	if n := len(fx.charges(id)); n != 0 {
		t.Fatalf("during the pause %d charges were recorded", n)
	}
	fx.pauseSubscriptions(false)
	fx.settle(pauseDay0.Add(4 * 24 * time.Hour))
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("after unpause: payer balance %s, want exactly one charge (9900)", got)
	}
	if n := len(fx.charges(id)); n != 1 {
		t.Fatalf("after unpause: %d charges recorded, want 1", n)
	}
}

// Unpaused, with the setting on, nothing differs from the legacy settlement.
func TestSubscriptionsUnpausedSettlementIsUnchangedByTheSetting(t *testing.T) {
	for _, activation := range []uint64{0, subsPauseHeight} {
		fx, id := pausedSubscriptionsFixture(t, activation)
		fx.pauseSubscriptions(false)
		fx.settle(pauseDay0)
		if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
			t.Fatalf("activation %d: payer balance %s, want 9900", activation, got)
		}
		if n := len(fx.charges(id)); n != 1 {
			t.Fatalf("activation %d: %d charges recorded, want 1", activation, n)
		}
	}
	// And a state with no pause view at all.
	fx, _ := pausedSubscriptionsFixture(t, subsPauseHeight)
	fx.settle(pauseDay0)
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("no pause view: payer balance %s, want 9900", got)
	}
}

// Below the height a paused module is still billed.
func TestSubscriptionsPauseSettlementIsNotActiveBeforeItsHeight(t *testing.T) {
	fx, _ := pausedSubscriptionsFixture(t, 1_000)
	fx.pauseSubscriptions(true)
	fx.settle(pauseDay0) // height 1
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("below the height: payer balance %s, want 9900", got)
	}
}

func TestSubscriptionsPauseSettlementIsCarriedByCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSubscriptionsPauseSettlement(42)
	dup, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.subsPauseSettleFromHeight != 42 {
		t.Fatalf("Copy dropped the setting: %d", dup.subsPauseSettleFromHeight)
	}
}

func TestNodeSubscriptionsPauseSettlement_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetSubscriptionsPauseSettlement(9)
	node.stateMu.Lock()
	gotH := node.state.subsPauseSettleFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.subsPauseSettleFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuiltH)
	}
}
