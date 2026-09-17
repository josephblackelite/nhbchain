package state

import (
	"math/big"
	"testing"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestAdjustTokenSupply(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(tr)

	total, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("initial supply: %v", err)
	}
	if total.Sign() != 0 {
		t.Fatalf("expected zero supply, got %s", total)
	}

	updated, err := manager.AdjustTokenSupply("nhb", big.NewInt(1000))
	if err != nil {
		t.Fatalf("adjust supply: %v", err)
	}
	if updated.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("unexpected supply after mint: %s", updated)
	}

	updated, err = manager.AdjustTokenSupply("NHB", big.NewInt(-250))
	if err != nil {
		t.Fatalf("burn supply: %v", err)
	}
	if updated.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("unexpected supply after burn: %s", updated)
	}

	if _, err = manager.AdjustTokenSupply("NHB", big.NewInt(-1000)); err == nil {
		t.Fatalf("expected underflow protection")
	}
}
