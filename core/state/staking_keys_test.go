package state

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestStakingKeysRoundtrip(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	mgr := NewManager(tr)

	global := &GlobalIndex{
		UQ128x128:      []byte{0xaa, 0xbb, 0xcc, 0xdd},
		LastUpdateUnix: 1_700_000_000,
		YTDEmissions:   big.NewInt(9876543210),
	}

	if err := mgr.PutGlobalIndex(global); err != nil {
		t.Fatalf("put global index: %v", err)
	}

	global.UQ128x128[0] = 0x00
	global.YTDEmissions.Add(global.YTDEmissions, big.NewInt(5))

	storedGlobal, err := mgr.GetGlobalIndex()
	if err != nil {
		t.Fatalf("get global index: %v", err)
	}

	if !bytes.Equal(storedGlobal.UQ128x128, []byte{0xaa, 0xbb, 0xcc, 0xdd}) {
		t.Fatalf("unexpected stored accumulator: %x", storedGlobal.UQ128x128)
	}
	if storedGlobal.LastUpdateUnix != 1_700_000_000 {
		t.Fatalf("unexpected last update: %d", storedGlobal.LastUpdateUnix)
	}
	if storedGlobal.YTDEmissions.Cmp(big.NewInt(9876543210)) != 0 {
		t.Fatalf("unexpected emissions: %s", storedGlobal.YTDEmissions)
	}
	if storedGlobal.YTDEmissions == global.YTDEmissions {
		t.Fatalf("global index emissions should be copied")
	}

	emptyGlobalDB := storage.NewMemDB()
	defer emptyGlobalDB.Close()

	emptyGlobalTrie, err := trie.NewTrie(emptyGlobalDB, nil)
	if err != nil {
		t.Fatalf("new empty global trie: %v", err)
	}
	emptyGlobal, err := NewManager(emptyGlobalTrie).GetGlobalIndex()
	if err != nil {
		t.Fatalf("get empty global index: %v", err)
	}
	if emptyGlobal.YTDEmissions == nil || emptyGlobal.YTDEmissions.Sign() != 0 {
		t.Fatalf("empty global index should default to zero emissions")
	}

	addr := []byte{0x01, 0x02, 0x03}
	snap := &AccountSnap{
		LastIndexUQ128x128: []byte{0x10, 0x20},
		AccruedZNHB:        big.NewInt(4242),
		LastPayoutUnix:     1_650_000_000,
	}

	if err := mgr.PutStakingSnap(addr, snap); err != nil {
		t.Fatalf("put staking snap: %v", err)
	}

	snap.LastIndexUQ128x128[0] = 0xff
	snap.AccruedZNHB.Add(snap.AccruedZNHB, big.NewInt(10))

	storedSnap, err := mgr.GetStakingSnap(addr)
	if err != nil {
		t.Fatalf("get staking snap: %v", err)
	}

	if !bytes.Equal(storedSnap.LastIndexUQ128x128, []byte{0x10, 0x20}) {
		t.Fatalf("unexpected stored last index: %x", storedSnap.LastIndexUQ128x128)
	}
	if storedSnap.LastPayoutUnix != 1_650_000_000 {
		t.Fatalf("unexpected stored payout ts: %d", storedSnap.LastPayoutUnix)
	}
	if storedSnap.AccruedZNHB.Cmp(big.NewInt(4242)) != 0 {
		t.Fatalf("unexpected stored accrued znHB: %s", storedSnap.AccruedZNHB)
	}
	if storedSnap.AccruedZNHB == snap.AccruedZNHB {
		t.Fatalf("account snapshot accrual should be copied")
	}

	emptySnapDB := storage.NewMemDB()
	defer emptySnapDB.Close()

	emptySnapTrie, err := trie.NewTrie(emptySnapDB, nil)
	if err != nil {
		t.Fatalf("new empty snap trie: %v", err)
	}
	emptySnap, err := NewManager(emptySnapTrie).GetStakingSnap([]byte{0xaa})
	if err != nil {
		t.Fatalf("get empty snap: %v", err)
	}
	if emptySnap.AccruedZNHB == nil || emptySnap.AccruedZNHB.Sign() != 0 {
		t.Fatalf("empty account snap should default to zero accrued znHB")
	}
}
