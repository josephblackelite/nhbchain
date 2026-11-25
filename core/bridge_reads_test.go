package core

import (
	"math"
	"math/big"
	"strings"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// Read-only views of single bridge locks and release records (ledger PL-BC-C): what was locked and what became of a burn
// id. Every test runs the real apply path first.

func twoLocks(t *testing.T) *bcFixture {
	t.Helper()
	f := stage1Fixture(t, nil) // alice has locked 8,000 ZNHB: lock id 1
	if err := f.send("alice", types.TxTypeBridgeLock, wei(25), bridgeLockPayload{DestChain: "eip155:84532", Dest: "0xAbCdEf0123456789aBcDeF0123456789AbCdEf01"}); err != nil {
		t.Fatalf("second lock: %v", err)
	}
	return f
}

func TestBridgeLockViewReportsEveryField(t *testing.T) {
	f := twoLocks(t)
	first, err := f.sp.BridgeLock(1)
	if err != nil || first == nil {
		t.Fatalf("lock 1: %v %v", first, err)
	}
	if first.ID != 1 || first.AmountWei.Cmp(wei(8_000)) != 0 || first.DestChain != bridgeTestChain || first.Dest != bridgeTestDest || first.Height == 0 {
		t.Fatalf("lock 1 reads %+v", first)
	}
	if !strings.HasPrefix(first.Sender, "nhb1") {
		t.Fatalf("the sender is shown as %q, want an nhb1 account", first.Sender)
	}
	second, err := f.sp.BridgeLock(2)
	if err != nil || second == nil {
		t.Fatalf("lock 2: %v %v", second, err)
	}
	// The chain stores the destination in lower case (it is compared as bytes), so a read shows the stored form.
	if second.ID != 2 || second.AmountWei.Cmp(wei(25)) != 0 || second.DestChain != "eip155:84532" || second.Dest != "0xabcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("lock 2 reads %+v", second)
	}
	if second.Sender != first.Sender {
		t.Fatalf("both locks came from alice but read as %s and %s", first.Sender, second.Sender)
	}
	// The returned amount is a copy: changing it cannot change what the next read says.
	first.AmountWei.SetInt64(1)
	again, _ := f.sp.BridgeLock(1)
	if again.AmountWei.Cmp(wei(8_000)) != 0 {
		t.Fatalf("a caller changed the amount of lock 1 through the view: now %s", again.AmountWei)
	}
}

func TestBridgeLockViewForAnIdThatDoesNotExistIsNil(t *testing.T) {
	f := twoLocks(t)
	for _, id := range []uint64{0, 3, 1 << 40, math.MaxUint64} {
		v, err := f.sp.BridgeLock(id)
		if err != nil || v != nil {
			t.Fatalf("lock %d does not exist but reads %+v, %v", id, v, err)
		}
	}
}

func TestBridgeLocksAfterPagesInIdOrderAndNeverPastTheLastLock(t *testing.T) {
	f := twoLocks(t)
	for i := 0; i < 5; i++ {
		if err := f.send("alice", types.TxTypeBridgeLock, wei(1), lockPayload()); err != nil {
			t.Fatalf("lock %d: %v", i+3, err)
		}
	}
	// seven locks in all
	page, last, err := f.sp.BridgeLocksAfter(0, 3)
	if err != nil || len(page) != 3 || page[0].ID != 1 || page[1].ID != 2 || page[2].ID != 3 || last != 7 {
		t.Fatalf("first page: %+v last=%d err=%v", page, last, err)
	}
	page, _, _ = f.sp.BridgeLocksAfter(3, 3)
	if len(page) != 3 || page[0].ID != 4 || page[2].ID != 6 {
		t.Fatalf("second page after id 3: %+v", page)
	}
	page, _, _ = f.sp.BridgeLocksAfter(6, 3)
	if len(page) != 1 || page[0].ID != 7 {
		t.Fatalf("last page after id 6: %+v", page)
	}
	for _, after := range []uint64{7, 8, 1 << 50, math.MaxUint64 - 1, math.MaxUint64} {
		page, last, err = f.sp.BridgeLocksAfter(after, 3)
		if err != nil || len(page) != 0 || last != 7 {
			t.Fatalf("after id %d there must be nothing, got %+v last=%d err=%v", after, page, last, err)
		}
	}
}

func TestBridgeLocksAfterLimitIsBoundedAndDefaulted(t *testing.T) {
	f := newBridgeFixture(t)
	f.fund("alice", wei(100_000), wei(0))
	total := BridgeListLocksMax + 5
	for i := 0; i < total; i++ {
		if err := f.send("alice", types.TxTypeBridgeLock, wei(1), lockPayload()); err != nil {
			t.Fatalf("lock %d: %v", i+1, err)
		}
	}
	page, last, err := f.sp.BridgeLocksAfter(0, 0)
	if err != nil || len(page) != BridgeListLocksDefault || last != uint64(total) {
		t.Fatalf("a limit of 0 must give the default %d, got %d last=%d (%v)", BridgeListLocksDefault, len(page), last, err)
	}
	page, _, err = f.sp.BridgeLocksAfter(0, -5)
	if err != nil || len(page) != BridgeListLocksDefault {
		t.Fatalf("a negative limit must give the default, got %d (%v)", len(page), err)
	}
	page, _, err = f.sp.BridgeLocksAfter(0, 1_000_000)
	if err != nil || len(page) != BridgeListLocksMax || page[len(page)-1].ID != uint64(BridgeListLocksMax) {
		t.Fatalf("a huge limit is capped at %d, got %d (%v)", BridgeListLocksMax, len(page), err)
	}
	page, _, err = f.sp.BridgeLocksAfter(uint64(BridgeListLocksMax), 1_000_000)
	if err != nil || len(page) != 5 {
		t.Fatalf("the five locks past the cap are the next page, got %d (%v)", len(page), err)
	}
}

func TestBridgeReleaseViewFollowsABurnIdFromNothingToPaid(t *testing.T) {
	f := stage1Fixture(t, nil)
	f.atHeight(100)
	id := []byte("burn-view-1")
	v, err := f.sp.BridgeRelease(id)
	if err != nil || v.Status != BridgeReleaseNone || v.AmountWei != nil {
		t.Fatalf("a burn id nobody used reads %+v, %v", v, err)
	}
	if err := f.release("burn-view-1", "bob", wei(40)); err != nil {
		t.Fatalf("release: %v", err)
	}
	v, err = f.sp.BridgeRelease([]byte("burn-view-1"))
	if err != nil || v.Status != BridgeReleasePaid || v.AmountWei.Cmp(wei(40)) != 0 || v.Height != 100 || !strings.HasPrefix(v.Recipient, "nhb1") {
		t.Fatalf("a paid burn id reads %+v, %v", v, err)
	}
	if other, _ := f.sp.BridgeRelease([]byte("burn-view-2")); other.Status != BridgeReleaseNone {
		t.Fatalf("a different burn id reads %+v", other)
	}
}

func TestBridgeReleaseViewShowsAWaitingAndACancelledRelease(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(100)
		p.DelayBlocks = 50
	})
	f.atHeight(200)
	if err := f.release("wait-1", "bob", wei(500)); err != nil {
		t.Fatalf("a large release: %v", err)
	}
	v, err := f.sp.BridgeRelease([]byte("wait-1"))
	if err != nil || v.Status != BridgeReleasePending || v.AmountWei.Cmp(wei(500)) != 0 || v.Height != 200 || v.ExecutableAt != 250 {
		t.Fatalf("a waiting release reads %+v, %v", v, err)
	}
	if err := f.op("guardian", BridgeOpCancel, "wait-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	v, err = f.sp.BridgeRelease([]byte("wait-1"))
	if err != nil || v.Status != BridgeReleaseCancelled || v.ExecutableAt != 0 {
		t.Fatalf("a cancelled release reads %+v, %v", v, err)
	}
}

func TestBridgeReleaseViewRefusesAnUnusableBurnId(t *testing.T) {
	f := stage1Fixture(t, nil)
	if _, err := f.sp.BridgeRelease(nil); err == nil {
		t.Fatalf("an empty burn id must be refused")
	}
	if _, err := f.sp.BridgeRelease(make([]byte, 65)); err == nil {
		t.Fatalf("a 65 byte burn id must be refused")
	}
	if _, err := f.sp.BridgeRelease(make([]byte, 64)); err != nil {
		t.Fatalf("a 64 byte burn id is allowed: %v", err)
	}
}

// A listing never hides damage: ids are assigned one after the other and never deleted, so a hole is an error and a
// record with a status the chain never writes is an error, not a quiet "paid".
func TestBridgeReadsRefuseDamagedState(t *testing.T) {
	f := twoLocks(t)
	manager := nhbstate.NewManager(f.sp.Trie)
	if err := manager.KVDelete(bridgeLockKey(1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.sp.BridgeLocksAfter(0, 10); err == nil || !strings.Contains(err.Error(), "lock 1 is missing") {
		t.Fatalf("a listing over a missing lock record must fail, got %v", err)
	}
	if page, _, err := f.sp.BridgeLocksAfter(1, 10); err != nil || len(page) != 1 || page[0].ID != 2 {
		t.Fatalf("a listing that starts after the hole still works: %+v %v", page, err)
	}
	if err := manager.KVPut(bridgeReleaseKey([]byte("odd-1")), bridgeReleaseRecord{Recipient: make([]byte, 20), Amount: wei(1), Height: 5, Status: 9}); err != nil {
		t.Fatal(err)
	}
	if v, err := f.sp.BridgeRelease([]byte("odd-1")); err == nil {
		t.Fatalf("a release record with status 9 must fail, got %+v", v)
	}
}

func TestNodeBridgeReadsAnswerOnARealNode(t *testing.T) {
	node := newTestNode(t)
	if v, err := node.BridgeLock(1); err != nil || v != nil {
		t.Fatalf("a node with no bridge reads lock 1 as %+v, %v", v, err)
	}
	page, last, err := node.BridgeLocksAfter(0, 10)
	if err != nil || len(page) != 0 || last != 0 {
		t.Fatalf("a node with no bridge lists %+v last=%d err=%v", page, last, err)
	}
	r, err := node.BridgeRelease([]byte("x"))
	if err != nil || r.Status != BridgeReleaseNone || r.AmountWei != (*big.Int)(nil) {
		t.Fatalf("a node with no bridge reads a release as %+v, %v", r, err)
	}
}
