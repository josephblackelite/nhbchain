package quotas

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	nativecommon "nhbchain/native/common"
)

type memoryState struct {
	data map[string][]byte
}

func newMemoryState() *memoryState {
	return &memoryState{data: make(map[string][]byte)}
}

func (m *memoryState) KVGet(key []byte, out interface{}) (bool, error) {
	raw, ok := m.data[string(key)]
	if !ok || len(raw) == 0 {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(raw, out); err != nil {
		return false, err
	}
	return true, nil
}

func (m *memoryState) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.data[string(key)] = encoded
	return nil
}

func (m *memoryState) KVAppend(key []byte, value []byte) error {
	var existing [][]byte
	if raw, ok := m.data[string(key)]; ok && len(raw) > 0 {
		if err := rlp.DecodeBytes(raw, &existing); err != nil {
			return err
		}
	}
	for _, entry := range existing {
		if string(entry) == string(value) {
			encoded, err := rlp.EncodeToBytes(existing)
			if err != nil {
				return err
			}
			m.data[string(key)] = encoded
			return nil
		}
	}
	existing = append(existing, append([]byte(nil), value...))
	encoded, err := rlp.EncodeToBytes(existing)
	if err != nil {
		return err
	}
	m.data[string(key)] = encoded
	return nil
}

func (m *memoryState) KVGetList(key []byte, out interface{}) error {
	raw, ok := m.data[string(key)]
	if !ok || len(raw) == 0 {
		// ensure out becomes empty slice
		switch dest := out.(type) {
		case *[]byte:
			*dest = (*dest)[:0]
		case *[][]byte:
			*dest = (*dest)[:0]
		default:
			// attempt to decode empty list
			encoded, _ := rlp.EncodeToBytes([][]byte{})
			return rlp.DecodeBytes(encoded, out)
		}
		return nil
	}
	return rlp.DecodeBytes(raw, out)
}

func (m *memoryState) KVDelete(key []byte) error {
	delete(m.data, string(key))
	return nil
}

func TestQuotaStoreCountersAndPrune(t *testing.T) {
	state := newMemoryState()
	store := NewStore(state)

	addr := make([]byte, 20)
	addr[0] = 0xAA
	quota := nativecommon.Quota{MaxRequestsPerMin: 2, EpochSeconds: 60}

	if _, err := nativecommon.Apply(store, "escrow", 0, addr, quota, 1, 0); err != nil {
		t.Fatalf("apply quota: %v", err)
	}
	next, err := nativecommon.Apply(store, "escrow", 0, addr, quota, 1, 0)
	if err != nil {
		t.Fatalf("apply quota second: %v", err)
	}
	if next.ReqCount != 2 {
		t.Fatalf("expected request count 2, got %d", next.ReqCount)
	}

	if _, err := nativecommon.Apply(store, "escrow", 0, addr, quota, 1, 0); err == nil || !errors.Is(err, nativecommon.ErrQuotaRequestsExceeded) {
		t.Fatalf("expected ErrQuotaRequestsExceeded, got %v", err)
	}

	rollover, err := nativecommon.Apply(store, "escrow", 1, addr, quota, 1, 0)
	if err != nil {
		t.Fatalf("apply quota after epoch: %v", err)
	}
	if rollover.EpochID != 1 || rollover.ReqCount != 1 {
		t.Fatalf("unexpected counters after rollover: %+v", rollover)
	}

	if err := store.PruneEpoch("escrow", 0); err != nil {
		t.Fatalf("prune epoch: %v", err)
	}
	if _, ok, err := store.Load("escrow", 0, addr); err != nil {
		t.Fatalf("load after prune: %v", err)
	} else if ok {
		t.Fatalf("expected epoch 0 counters pruned")
	}
}
