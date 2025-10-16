package escrow_test

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/core/state"
	escrowpkg "nhbchain/native/escrow"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func newTestManager(t *testing.T) *state.Manager {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return state.NewManager(tr)
}

func TestManagerEscrowPutGet(t *testing.T) {
	mgr := newTestManager(t)
	var id [32]byte
	copy(id[:], bytes.Repeat([]byte{0xAB}, 32))
	var payer [20]byte
	copy(payer[:], bytes.Repeat([]byte{0x01}, 20))
	var payee [20]byte
	copy(payee[:], bytes.Repeat([]byte{0x02}, 20))
	var mediator [20]byte
	copy(mediator[:], bytes.Repeat([]byte{0x03}, 20))

	amount := big.NewInt(1_000_000)
	created := int64(1_695_000_000)
	escrowDef := &escrowpkg.Escrow{
		ID:            id,
		Payer:         payer,
		Payee:         payee,
		Mediator:      mediator,
		Token:         "nhb",
		Amount:        amount,
		FeeBps:        250,
		Deadline:      1_700_000_000,
		CreatedAt:     created,
		Nonce:         1,
		Status:        escrowpkg.EscrowFunded,
		DisputeReason: " delayed shipment ",
	}

	if err := mgr.EscrowPut(escrowDef); err != nil {
		t.Fatalf("EscrowPut: %v", err)
	}

	stored, ok := mgr.EscrowGet(id)
	if !ok {
		t.Fatalf("EscrowGet: expected escrow to exist")
	}

	if stored == nil {
		t.Fatalf("EscrowGet returned nil escrow")
	}
	if stored.Token != "NHB" {
		t.Fatalf("expected token to normalise to NHB, got %s", stored.Token)
	}
	if stored.Amount == nil || stored.Amount.Cmp(amount) != 0 {
		t.Fatalf("unexpected amount: %v", stored.Amount)
	}
	if stored.Amount == amount {
		t.Fatalf("EscrowGet should clone amount pointer")
	}
	if stored.FeeBps != escrowDef.FeeBps {
		t.Fatalf("unexpected fee bps: %d", stored.FeeBps)
	}
	if stored.CreatedAt != created {
		t.Fatalf("unexpected createdAt: %d", stored.CreatedAt)
	}
	if stored.Status != escrowpkg.EscrowFunded {
		t.Fatalf("unexpected status: %d", stored.Status)
	}
	if stored.Nonce != escrowDef.Nonce {
		t.Fatalf("unexpected nonce: %d", stored.Nonce)
	}
	if stored.Payer != payer || stored.Payee != payee || stored.Mediator != mediator {
		t.Fatalf("addresses mutated during round trip")
	}
	if stored.DisputeReason != "delayed shipment" {
		t.Fatalf("unexpected dispute reason: %q", stored.DisputeReason)
	}
}

func TestManagerEscrowCreditDebit(t *testing.T) {
	mgr := newTestManager(t)
	var id [32]byte
	copy(id[:], bytes.Repeat([]byte{0xCD}, 32))
	var payer [20]byte
	copy(payer[:], bytes.Repeat([]byte{0x04}, 20))
	var payee [20]byte
	copy(payee[:], bytes.Repeat([]byte{0x05}, 20))

	escrowDef := &escrowpkg.Escrow{
		ID:     id,
		Payer:  payer,
		Payee:  payee,
		Token:  "znHB",
		Amount: big.NewInt(5000),
		Nonce:  2,
		Status: escrowpkg.EscrowInit,
	}
	if err := mgr.EscrowPut(escrowDef); err != nil {
		t.Fatalf("EscrowPut: %v", err)
	}

	if err := mgr.EscrowCredit(id, "znhb", big.NewInt(5)); err != nil {
		t.Fatalf("credit #1 failed: %v", err)
	}
	if err := mgr.EscrowCredit(id, "ZNHB", big.NewInt(7)); err != nil {
		t.Fatalf("credit #2 failed: %v", err)
	}
	if err := mgr.EscrowDebit(id, "ZNHB", big.NewInt(4)); err != nil {
		t.Fatalf("debit #1 failed: %v", err)
	}
	if err := mgr.EscrowDebit(id, "ZNHB", big.NewInt(9)); err == nil {
		t.Fatalf("expected debit to fail when exceeding balance")
	}
	if err := mgr.EscrowDebit(id, "ZNHB", big.NewInt(8)); err != nil {
		t.Fatalf("debit #2 failed: %v", err)
	}
	if err := mgr.EscrowDebit(id, "ZNHB", big.NewInt(1)); err == nil {
		t.Fatalf("expected debit on empty balance to fail")
	}
	if err := mgr.EscrowCredit(id, "DOGE", big.NewInt(1)); err == nil {
		t.Fatalf("expected unsupported token credit to fail")
	}
	if err := mgr.EscrowCredit(id, "ZNHB", big.NewInt(-1)); err == nil {
		t.Fatalf("expected negative credit to fail")
	}
	var unknown [32]byte
	if err := mgr.EscrowCredit(unknown, "NHB", big.NewInt(1)); err == nil {
		t.Fatalf("expected credit on unknown escrow to fail")
	}
}
