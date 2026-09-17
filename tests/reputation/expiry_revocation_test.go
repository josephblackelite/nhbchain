package reputation_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/native/reputation"
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

func TestReputationExpiryAndRevocation(t *testing.T) {
	store := newMemoryStore()
	ledger := reputation.NewLedger(store)

	var subject [20]byte
	copy(subject[:], []byte("subject-addr-1234567890"))
	var verifier [20]byte
	copy(verifier[:], []byte("verifier-addr-123456"))
	var rogue [20]byte
	copy(rogue[:], []byte("rogue-verifier-0000"))

	issued := time.Unix(1_700_000_000, 0).Unix()
	expires := issued + 60

	verification := &reputation.SkillVerification{
		Subject:   subject,
		Skill:     "Solidity",
		Verifier:  verifier,
		IssuedAt:  issued,
		ExpiresAt: expires,
	}
	ledger.SetNowFunc(func() int64 { return issued })
	if err := ledger.Put(verification); err != nil {
		t.Fatalf("put verification: %v", err)
	}

	attID, err := reputation.AttestationID(verification)
	if err != nil {
		t.Fatalf("attestation id: %v", err)
	}

	stored, ok, err := ledger.Get(subject, "Solidity", verifier)
	if err != nil {
		t.Fatalf("get verification: %v", err)
	}
	if !ok || stored == nil {
		t.Fatalf("expected verification to be returned before expiry")
	}

	if _, err := ledger.Revoke(attID, rogue, "malicious"); !errors.Is(err, reputation.ErrRevocationUnauthorized) {
		t.Fatalf("expected unauthorized revocation error, got %v", err)
	}

	ledger.SetNowFunc(func() int64 { return expires + 1 })
	_, ok, err = ledger.Get(subject, "Solidity", verifier)
	if err != nil {
		t.Fatalf("get verification post-expiry: %v", err)
	}
	if ok {
		t.Fatalf("expected verification to be filtered after expiry")
	}

	ledger.SetNowFunc(func() int64 { return issued + 10 })
	revocation, err := ledger.Revoke(attID, verifier, "superseded by new exam")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revocation == nil {
		t.Fatalf("expected revocation details")
	}
	if revocation.Reason != "superseded by new exam" {
		t.Fatalf("unexpected revocation reason %q", revocation.Reason)
	}

	_, ok, err = ledger.Get(subject, "Solidity", verifier)
	if err != nil {
		t.Fatalf("get verification after revocation: %v", err)
	}
	if ok {
		t.Fatalf("expected revoked verification to be filtered")
	}

	if _, err := ledger.Revoke(attID, verifier, "duplicate"); !errors.Is(err, reputation.ErrAttestationRevoked) {
		t.Fatalf("expected revoked error on second revocation, got %v", err)
	}
}
