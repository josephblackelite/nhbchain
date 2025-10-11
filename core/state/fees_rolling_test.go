package state

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestRollingFeesAddDayMergesBuckets(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(tr)
	tracker := NewRollingFees(manager)

	day := time.Date(2024, time.March, 10, 14, 30, 0, 0, time.UTC)
	if err := tracker.AddDay(day, big.NewInt(10), big.NewInt(20)); err != nil {
		t.Fatalf("add first bucket: %v", err)
	}
	if err := tracker.AddDay(day, big.NewInt(5), big.NewInt(15)); err != nil {
		t.Fatalf("add second bucket: %v", err)
	}

	dayID := dayStartUTC(day).Format(rollingFeesDateFormat)
	key := rollingFeeBucketKey(dayID)
	var stored storedRollingFees
	ok, err := manager.KVGet(key, &stored)
	if err != nil {
		t.Fatalf("load bucket: %v", err)
	}
	if !ok {
		t.Fatalf("bucket missing")
	}

	if stored.NetNHB.Cmp(big.NewInt(15)) != 0 {
		t.Fatalf("unexpected NHB total: %s", stored.NetNHB)
	}
	if stored.NetZNHB.Cmp(big.NewInt(35)) != 0 {
		t.Fatalf("unexpected ZNHB total: %s", stored.NetZNHB)
	}

	var index []string
	if err := manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		t.Fatalf("load index: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("unexpected index length: %d", len(index))
	}
	if index[0] != dayID {
		t.Fatalf("unexpected index entry: %s", index[0])
	}

	totalNHB, err := tracker.Get7dNetFeesNHB(day)
	if err != nil {
		t.Fatalf("sum nhb: %v", err)
	}
	if totalNHB.Cmp(big.NewInt(15)) != 0 {
		t.Fatalf("unexpected 7d NHB total: %s", totalNHB)
	}

	totalZNHB, err := tracker.Get7dNetFeesZNHB(day)
	if err != nil {
		t.Fatalf("sum znhb: %v", err)
	}
	if totalZNHB.Cmp(big.NewInt(35)) != 0 {
		t.Fatalf("unexpected 7d ZNHB total: %s", totalZNHB)
	}
}

func TestRollingFeesSevenDayWindow(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(tr)
	tracker := NewRollingFees(manager)

	base := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		day := base.AddDate(0, 0, i)
		nhb := big.NewInt(int64(i + 1))
		znhb := big.NewInt(int64((i + 1) * 10))
		if err := tracker.AddDay(day, nhb, znhb); err != nil {
			t.Fatalf("add day %d: %v", i, err)
		}
		if i == 4 {
			partialNHB, err := tracker.Get7dNetFeesNHB(day)
			if err != nil {
				t.Fatalf("partial nhb: %v", err)
			}
			if partialNHB.Cmp(big.NewInt(15)) != 0 {
				t.Fatalf("unexpected partial NHB total: %s", partialNHB)
			}

			partialZNHB, err := tracker.Get7dNetFeesZNHB(day)
			if err != nil {
				t.Fatalf("partial znhb: %v", err)
			}
			if partialZNHB.Cmp(big.NewInt(150)) != 0 {
				t.Fatalf("unexpected partial ZNHB total: %s", partialZNHB)
			}
		}
	}

	var index []string
	if err := manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		t.Fatalf("load index: %v", err)
	}
	if len(index) != 7 {
		t.Fatalf("unexpected index length: %d", len(index))
	}
	for i := 0; i < 7; i++ {
		expected := dayStartUTC(base.AddDate(0, 0, i+1)).Format(rollingFeesDateFormat)
		if index[i] != expected {
			t.Fatalf("unexpected index entry at %d: %s != %s", i, index[i], expected)
		}
	}

	totalNHB, err := tracker.Get7dNetFeesNHB(base.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("sum nhb: %v", err)
	}
	if totalNHB.Cmp(big.NewInt(35)) != 0 {
		t.Fatalf("unexpected rolling NHB total: %s", totalNHB)
	}

	totalZNHB, err := tracker.Get7dNetFeesZNHB(base.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("sum znhb: %v", err)
	}
	if totalZNHB.Cmp(big.NewInt(350)) != 0 {
		t.Fatalf("unexpected rolling ZNHB total: %s", totalZNHB)
	}

	// Ensure trimming kept only the most recent seven entries.
}
