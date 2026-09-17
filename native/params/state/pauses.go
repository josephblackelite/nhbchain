package state

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const pausesKey = "system/pauses"

// Reader exposes the minimal parameter store capabilities required to inspect pause toggles.
type Reader interface {
	ParamStoreGet(name string) ([]byte, bool, error)
}

// StakingPaused reports whether the staking module pause toggle is enabled.
func StakingPaused(reader Reader) (bool, error) {
	if reader == nil {
		return false, fmt.Errorf("params: reader not configured")
	}
	raw, ok, err := reader.ParamStoreGet(pausesKey)
	if err != nil {
		return false, fmt.Errorf("params: load pauses: %w", err)
	}
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var payload struct {
		Staking bool `json:"staking"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, fmt.Errorf("params: decode pauses: %w", err)
	}
	return payload.Staking, nil
}
