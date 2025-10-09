package params

import (
	"bytes"
	"encoding/json"
	"fmt"

	"nhbchain/config"
)

// StoreState captures the subset of state manager capabilities required by the
// parameter helpers.
type StoreState interface {
	ParamStoreSet(name string, value []byte) error
	ParamStoreGet(name string) ([]byte, bool, error)
}

// Store provides typed accessors for governance-controlled parameters.
type Store struct {
	state StoreState
}

// NewStore constructs a parameter store wrapper using the supplied state
// backend.
func NewStore(state StoreState) *Store {
	return &Store{state: state}
}

func (s *Store) withState() (StoreState, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("params: state not configured")
	}
	return s.state, nil
}

// SetPauses persists the supplied pause configuration under the canonical
// parameter store key. Values are marshalled as JSON to align with governance
// proposal payloads.
func (s *Store) SetPauses(pauses config.Pauses) error {
	state, err := s.withState()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(pauses)
	if err != nil {
		return fmt.Errorf("params: encode pauses: %w", err)
	}
	return state.ParamStoreSet(ParamsKeyPauses, encoded)
}

// Pauses loads the persisted pause configuration. When unset, a zero-value
// configuration is returned.
func (s *Store) Pauses() (config.Pauses, error) {
	state, err := s.withState()
	if err != nil {
		return config.Pauses{}, err
	}
	raw, ok, err := state.ParamStoreGet(ParamsKeyPauses)
	if err != nil {
		return config.Pauses{}, err
	}
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return config.Pauses{}, nil
	}
	var pauses config.Pauses
	if err := json.Unmarshal(raw, &pauses); err != nil {
		return config.Pauses{}, fmt.Errorf("params: decode pauses: %w", err)
	}
	return pauses, nil
}

// SetStaking persists the staking configuration under the canonical parameter store key.
func (s *Store) SetStaking(staking config.Staking) error {
	state, err := s.withState()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(staking)
	if err != nil {
		return fmt.Errorf("params: encode staking: %w", err)
	}
	return state.ParamStoreSet(ParamsKeyStaking, encoded)
}

// Staking loads the persisted staking configuration if present.
func (s *Store) Staking() (config.Staking, error) {
	state, err := s.withState()
	if err != nil {
		return config.Staking{}, err
	}
	raw, ok, err := state.ParamStoreGet(ParamsKeyStaking)
	if err != nil {
		return config.Staking{}, err
	}
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return config.Staking{}, nil
	}
	var staking config.Staking
	if err := json.Unmarshal(raw, &staking); err != nil {
		return config.Staking{}, fmt.Errorf("params: decode staking: %w", err)
	}
	return staking, nil
}
