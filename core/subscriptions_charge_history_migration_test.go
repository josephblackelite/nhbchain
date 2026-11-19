package core

import (
	"encoding/binary"
	"math/big"
	"testing"
	"time"

	"nhbchain/native/subscriptions"
)

// PL-R5T-82 through the settlement: a subscription holding charge history under the old
// key keeps its numbering from the configured height, and is left as it was before it.

func legacyListKey(id subscriptions.SubscriptionID) []byte {
	key := []byte("subscriptions/charges/")
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], uint64(id))
	return append(key, seq[:]...)
}

func seedLegacyHistory(t *testing.T, fx *sameDayFixture, id subscriptions.SubscriptionID, attempts int) {
	t.Helper()
	var old []subscriptions.Charge
	for i := 1; i <= attempts; i++ {
		old = append(old, subscriptions.Charge{
			SubscriptionID: id, PlanID: 1, Payer: fx.payer, Merchant: fx.merchant, Asset: subscriptions.AssetNHB,
			AmountWei: big.NewInt(100), FeeWei: big.NewInt(0), Status: subscriptions.ChargeStatusPaid,
			AttemptNumber: uint32(i), ChargedAt: uint64(pauseDay0.Unix()) - uint64(attempts-i+1)*86400,
		})
	}
	if err := fx.manager.KVPut(legacyListKey(id), old); err != nil {
		t.Fatalf("seed the old list: %v", err)
	}
}

func migrationFixture(t *testing.T, activation uint64, attempts int) (*sameDayFixture, subscriptions.SubscriptionID) {
	t.Helper()
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	fx.sp.SetSubscriptionsChargeHistoryMigration(activation)
	fx.setBalance(fx.payer, 10_000)
	id := fx.subscribe(pauseDay0, 100, 30*86400)
	seedLegacyHistory(t, fx, id, attempts)
	return fx, id
}

func attemptNumbers(charges []subscriptions.Charge) []uint32 {
	out := []uint32{}
	for _, c := range charges {
		out = append(out, c.AttemptNumber)
	}
	return out
}

func equalAttempts(a, b []uint32) bool {
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

func TestSubscriptionsChargeHistoryMigrationOffRestartsTheNumbering(t *testing.T) {
	fx, id := migrationFixture(t, 0, 3)
	fx.settle(pauseDay0)
	if got := attemptNumbers(fx.charges(id)); !equalAttempts(got, []uint32{1}) {
		t.Fatalf("setting off: history %v, want only the new attempt numbered 1", got)
	}
}

func TestSubscriptionsChargeHistoryMigrationOnContinuesTheNumbering(t *testing.T) {
	fx, id := migrationFixture(t, subsPauseHeight, 3)
	fx.settle(pauseDay0)
	if got := attemptNumbers(fx.charges(id)); !equalAttempts(got, []uint32{1, 2, 3, 4}) {
		t.Fatalf("setting on: history %v, want [1 2 3 4]", got)
	}
	if got := fx.balance(fx.payer); got.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("the charge itself changed: payer balance %s", got)
	}
}

func TestSubscriptionsChargeHistoryMigrationIsNotActiveBeforeItsHeight(t *testing.T) {
	fx, id := migrationFixture(t, 1_000, 3)
	fx.settle(pauseDay0)
	if got := attemptNumbers(fx.charges(id)); !equalAttempts(got, []uint32{1}) {
		t.Fatalf("below the height: history %v, want [1]", got)
	}
}

// With no history under the old key the setting leaves the trie root identical.
func TestSubscriptionsChargeHistoryMigrationLeavesAFreshChainIdentical(t *testing.T) {
	roots := map[uint64]string{}
	for _, activation := range []uint64{0, subsPauseHeight} {
		fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
		fx.sp.SetSubscriptionsChargeHistoryMigration(activation)
		fx.setBalance(fx.payer, 10_000)
		fx.subscribe(pauseDay0, 100, 30*86400)
		fx.settle(pauseDay0)
		fx.settle(pauseDay0.Add(time.Hour))
		roots[activation] = fx.sp.Trie.Hash().Hex()
	}
	if roots[0] != roots[subsPauseHeight] {
		t.Fatalf("state root differs with the setting on and nothing under the old key: %s vs %s", roots[0], roots[subsPauseHeight])
	}
}

func TestSubscriptionsChargeHistoryMigrationIsCarriedByCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSubscriptionsChargeHistoryMigration(42)
	dup, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.subsChargeMigrationFromHeight != 42 {
		t.Fatalf("Copy dropped the setting: %d", dup.subsChargeMigrationFromHeight)
	}
}

func TestNodeSubscriptionsChargeHistoryMigration_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetSubscriptionsChargeHistoryMigration(9)
	node.stateMu.Lock()
	gotH := node.state.subsChargeMigrationFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.subsChargeMigrationFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuiltH)
	}
}
