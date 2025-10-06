package state

import (
	"math/big"
	"testing"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestRefundLedgerRecordAndThread(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(tr)
	ledger := manager.RefundLedger()
	if ledger == nil {
		t.Fatalf("expected refund ledger")
	}

	var origin [32]byte
	copy(origin[:], []byte("origin-hash-000000000000000000000000"))
	amount := big.NewInt(1_000)
	if _, err := ledger.RecordOrigin(origin, amount, 42); err != nil {
		t.Fatalf("record origin: %v", err)
	}

	if err := ledger.ValidateRefund(origin, big.NewInt(600)); err != nil {
		t.Fatalf("validate refund: %v", err)
	}

	var refund [32]byte
	copy(refund[:], []byte("refund-hash-000000000000000000000000"))
	if _, err := ledger.ApplyRefund(origin, refund, big.NewInt(600), 99); err != nil {
		t.Fatalf("apply refund: %v", err)
	}

	thread, ok, err := ledger.Thread(origin)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if !ok {
		t.Fatalf("expected thread to exist")
	}
	if thread == nil || thread.OriginAmount.Cmp(amount) != 0 {
		t.Fatalf("unexpected origin amount: %v", thread)
	}
	if thread.CumulativeRefunded.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("unexpected cumulative refund: %s", thread.CumulativeRefunded)
	}
	if len(thread.Refunds) != 1 {
		t.Fatalf("expected one refund entry")
	}
	if thread.Refunds[0].Amount.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("unexpected refund amount: %s", thread.Refunds[0].Amount)
	}
}

func TestRefundLedgerOverRefund(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := NewManager(tr)
	ledger := manager.RefundLedger()

	var origin [32]byte
	copy(origin[:], []byte("origin-overflow-hash--------000000"))
	if _, err := ledger.RecordOrigin(origin, big.NewInt(500), 1); err != nil {
		t.Fatalf("record origin: %v", err)
	}
	if err := ledger.ValidateRefund(origin, big.NewInt(700)); err == nil {
		t.Fatalf("expected over-refund validation error")
	}
	var refund [32]byte
	copy(refund[:], []byte("refund-overflow-hash--------000000"))
	if _, err := ledger.ApplyRefund(origin, refund, big.NewInt(700), 2); err == nil {
		t.Fatalf("expected over-refund application error")
	}
}
