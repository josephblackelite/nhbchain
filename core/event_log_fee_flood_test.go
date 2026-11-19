package core

import (
	"math/big"
	"reflect"
	"testing"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-R5T-42: fee events share the recent ring, not the pinned one, so a flood of them
// cannot push out the penalty and escrow events; the owner wallet's fee total comes
// from the durable per-domain totals, which a ring wrap or a restart cannot lower.

func TestEventLogFeeFloodLeavesPenaltyAndEscrowEventsInPlace(t *testing.T) {
	log := newEventLog(5, 3)
	log.append([]types.Event{
		typedEvent(events.TypePotsoPenaltyApplied, 1),
		typedEvent("escrow.created", 2),
	})
	flood := make([]types.Event, 0, 500)
	for n := 3; n < 503; n++ {
		flood = append(flood, typedEvent(events.TypeFeeApplied, n))
	}
	log.append(flood)

	kept := map[string]bool{}
	for _, evt := range log.snapshot() {
		kept[evt.Type+"/"+evt.Attributes["n"]] = true
	}
	if !kept[events.TypePotsoPenaltyApplied+"/1"] {
		t.Fatalf("the penalty event was pushed out by fee events")
	}
	if !kept["escrow.created/2"] {
		t.Fatalf("the escrow event was pushed out by fee events")
	}
}

func TestEventLogFeeEventsAreBoundedByTheRecentRing(t *testing.T) {
	log := newEventLog(5, 3)
	var batch []types.Event
	for n := 0; n < 100; n++ {
		batch = append(batch, typedEvent(events.TypeFeeApplied, n))
	}
	log.append(batch)
	var got []string
	for _, evt := range log.snapshot() {
		got = append(got, evt.Attributes["n"])
	}
	if !reflect.DeepEqual(got, []string{"95", "96", "97", "98", "99"}) {
		t.Fatalf("fee events held: %v, want only the last five", got)
	}
}

func TestOwnerWalletFeeAccrualComesFromTheDurableTotals(t *testing.T) {
	node := newTestNode(t)
	var owner, other [20]byte
	owner[19], other[19] = 0xA1, 0xA2
	record := func(domain, asset string, wallet [20]byte, fee int64) {
		t.Helper()
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		manager := nhbstate.NewManager(node.state.Trie)
		if err := manager.FeesAccumulateTotals(domain, asset, wallet, big.NewInt(fee*20), big.NewInt(fee), big.NewInt(fee*19)); err != nil {
			t.Fatalf("accumulate: %v", err)
		}
	}
	record("pos", "NHB", owner, 500)
	record("pos", "NHB", owner, 250)
	record("pos", "ZNHB", owner, 40)
	record("otc", "NHB", owner, 10)
	record("pos", "NHB", other, 9_999)

	got, err := node.OwnerWalletFeeAccrual(owner, []string{"pos", "otc", "p2p", "POS"})
	if err != nil {
		t.Fatalf("accrual: %v", err)
	}
	want := map[string]string{"NHB": "760", "ZNHB": "40"}
	if len(got) != len(want) {
		t.Fatalf("accrual by asset %v, want %v", got, want)
	}
	for asset, amount := range want {
		if got[asset] == nil || got[asset].String() != amount {
			t.Fatalf("accrual %s = %v, want %s", asset, got[asset], amount)
		}
	}

	// A flood of fee events through the log changes nothing: the figure is not read from it.
	node.stateMu.Lock()
	for n := 0; n < 3*maxRetainedEvents; n++ {
		evt := typedEvent(events.TypeFeeApplied, n)
		node.state.AppendEvent(&evt)
	}
	node.retainCommittedEventsLocked()
	node.stateMu.Unlock()
	again, err := node.OwnerWalletFeeAccrual(owner, []string{"pos", "otc", "p2p", "POS"})
	if err != nil || again["NHB"].String() != "760" || again["ZNHB"].String() != "40" {
		t.Fatalf("accrual after a flood of fee events: %v %v", again, err)
	}
}
