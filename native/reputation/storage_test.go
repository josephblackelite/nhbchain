package reputation

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
)

type memoryStore struct {
	data map[string][]byte
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string][]byte)}
}

func (m *memoryStore) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.data[string(key)] = encoded
	return nil
}

func (m *memoryStore) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := m.data[string(key)]
	if !ok {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(encoded, out); err != nil {
		return false, err
	}
	return true, nil
}

func TestLedgerPutAndGet(t *testing.T) {
	store := newMemoryStore()
	ledger := NewLedger(store)

	var subject [20]byte
	copy(subject[:], []byte("subject-address-123"))
	var verifier [20]byte
	copy(verifier[:], []byte("verifier-address-456"))

	issued := time.Now().Unix()
	expires := issued + 3600
	verification := &SkillVerification{
		Subject:   subject,
		Skill:     "  Solidity  ",
		Verifier:  verifier,
		IssuedAt:  issued,
		ExpiresAt: expires,
	}
	if err := ledger.Put(verification); err != nil {
		t.Fatalf("put verification: %v", err)
	}

	stored, ok, err := ledger.Get(subject, "solidity", verifier)
	if err != nil {
		t.Fatalf("get verification: %v", err)
	}
	if !ok {
		t.Fatalf("expected verification to exist")
	}
	if stored.Skill != "Solidity" {
		t.Fatalf("expected skill 'Solidity', got %q", stored.Skill)
	}
	if stored.IssuedAt != issued {
		t.Fatalf("expected issuedAt %d, got %d", issued, stored.IssuedAt)
	}
	if stored.ExpiresAt != expires {
		t.Fatalf("expected expiresAt %d, got %d", expires, stored.ExpiresAt)
	}
}

func TestLedgerPutInvalidSkill(t *testing.T) {
	store := newMemoryStore()
	ledger := NewLedger(store)

	err := ledger.Put(&SkillVerification{})
	if err == nil {
		t.Fatalf("expected error for invalid verification")
	}
}
