package state

import (
	"errors"
	"fmt"
	"math"

	"nhbchain/storage/trie"
)

// StateVersion identifies the expected on-disk schema layout for the core
// application state. Increment this constant whenever breaking changes are made
// to the stored structure. Version 2 introduces persistent staking fields and
// associated metadata.
const StateVersion uint32 = 2

var (
	stateVersionKey = []byte("state/version")
	// ErrStateVersionMismatch indicates the stored schema version does not
	// match the version supported by the current binary.
	ErrStateVersionMismatch = errors.New("state: schema version mismatch")
)

// SetStateVersion records the provided schema version in state. Callers should
// invoke this after performing any required migrations.
func (m *Manager) SetStateVersion(version uint32) error {
	if m == nil {
		return fmt.Errorf("state: manager unavailable")
	}
	return m.KVPut(stateVersionKey, uint64(version))
}

// StateVersion returns the stored schema version and a boolean indicating
// whether the value was present.
func (m *Manager) StateVersion() (uint32, bool, error) {
	if m == nil {
		return 0, false, fmt.Errorf("state: manager unavailable")
	}
	var stored uint64
	ok, err := m.KVGet(stateVersionKey, &stored)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	if stored > uint64(math.MaxUint32) {
		return 0, false, fmt.Errorf("state: schema version overflow: %d", stored)
	}
	return uint32(stored), true, nil
}

// EnsureStateVersion verifies that the on-disk state version matches the
// version supported by this binary. When allowMigrate is true, mismatches are
// tolerated so operators can perform manual migrations.
func EnsureStateVersion(tr *trie.Trie, allowMigrate bool) error {
	if tr == nil {
		return fmt.Errorf("state: trie must not be nil")
	}
	manager := NewManager(tr)
	version, ok, err := manager.StateVersion()
	if err != nil {
		return err
	}
	if !ok {
		version = 0
	}
	if version == StateVersion {
		return nil
	}
	if allowMigrate {
		return nil
	}
	return fmt.Errorf("%w: on-disk=%d expected=%d", ErrStateVersionMismatch, version, StateVersion)
}
