package pos

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

type memoryRegistryState struct {
	kv map[string][]byte
}

func newMemoryRegistryState() *memoryRegistryState {
	return &memoryRegistryState{kv: make(map[string][]byte)}
}

func (m *memoryRegistryState) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := m.kv[string(key)]
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

func (m *memoryRegistryState) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.kv[string(key)] = encoded
	return nil
}

func (m *memoryRegistryState) KVDelete(key []byte) error {
	delete(m.kv, string(key))
	return nil
}

func TestRegistryMerchantNonceTracking(t *testing.T) {
	state := newMemoryRegistryState()
	registry := NewRegistry(state)

	record, err := registry.UpsertMerchant("admin", "merchant-a", 1, 100, "chain-A")
	if err != nil {
		t.Fatalf("upsert merchant: %v", err)
	}
	if record.Nonce != 1 || record.ExpiresAt != 100 || strings.TrimSpace(record.ChainID) != "chain-A" {
		t.Fatalf("unexpected merchant domain: %+v", record)
	}

	if _, err := registry.UpsertMerchant("admin", "merchant-a", 1, 200, "chain-A"); err == nil {
		t.Fatalf("expected stale nonce rejection")
	}

	paused, err := registry.PauseMerchant("admin", "merchant-a", 2, 150, "chain-A")
	if err != nil {
		t.Fatalf("pause merchant: %v", err)
	}
	if !paused.Paused || paused.Nonce != 2 || paused.ExpiresAt != 150 {
		t.Fatalf("unexpected pause result: %+v", paused)
	}

	resumed, err := registry.ResumeMerchant("admin", "merchant-a", 3, 175, "chain-A")
	if err != nil {
		t.Fatalf("resume merchant: %v", err)
	}
	if resumed.Paused {
		t.Fatalf("expected merchant to be active after resume")
	}
	if resumed.Nonce != 3 || resumed.ExpiresAt != 175 {
		t.Fatalf("unexpected resume domain: %+v", resumed)
	}

	if _, err := registry.PauseMerchant("admin", "merchant-a", 3, 180, "chain-A"); err == nil {
		t.Fatalf("expected pause with reused nonce to fail")
	}

	other, err := registry.UpsertMerchant("ops", "merchant-b", 1, 90, "chain-B")
	if err != nil {
		t.Fatalf("secondary authority upsert: %v", err)
	}
	if other.Nonce != 1 || strings.TrimSpace(other.ChainID) != "chain-B" {
		t.Fatalf("unexpected secondary merchant domain: %+v", other)
	}
}

func TestRegistryDeviceNonceTracking(t *testing.T) {
	state := newMemoryRegistryState()
	registry := NewRegistry(state)

	if _, err := registry.UpsertMerchant("admin", "merchant-a", 1, 100, "chain-A"); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	device, err := registry.RegisterDevice("admin", "device-1", "merchant-a", 2, 110, "chain-A")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.Nonce != 2 || device.ExpiresAt != 110 || strings.TrimSpace(device.ChainID) != "chain-A" {
		t.Fatalf("unexpected device domain: %+v", device)
	}

	revoked, err := registry.RevokeDevice("admin", "device-1", 3, 120, "chain-A")
	if err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if !revoked.Revoked || revoked.Nonce != 3 {
		t.Fatalf("unexpected revoke result: %+v", revoked)
	}

	restored, err := registry.RestoreDevice("admin", "device-1", 4, 130, "chain-A")
	if err != nil {
		t.Fatalf("restore device: %v", err)
	}
	if restored.Revoked {
		t.Fatalf("expected device to be restored")
	}
	if restored.Nonce != 4 || restored.ExpiresAt != 130 {
		t.Fatalf("unexpected restore domain: %+v", restored)
	}

	if _, err := registry.RevokeDevice("admin", "device-1", 4, 140, "chain-A"); err == nil {
		t.Fatalf("expected revoke with stale nonce to fail")
	}

	otherAuthority, err := registry.RevokeDevice("ops", "device-1", 1, 150, "chain-B")
	if err != nil {
		t.Fatalf("other authority revoke: %v", err)
	}
	if !otherAuthority.Revoked || otherAuthority.Nonce != 1 || strings.TrimSpace(otherAuthority.ChainID) != "chain-B" {
		t.Fatalf("unexpected other authority revoke: %+v", otherAuthority)
	}
}
