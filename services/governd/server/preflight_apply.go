package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"nhbchain/config"
	govcfg "nhbchain/native/gov"
)

// preflightPolicyApply validates that applying the provided parameter payload
// over the supplied baseline configuration preserves all global invariants. The
// payload should mirror the map returned by governance parameter validation.
func preflightPolicyApply(cur config.Global, payload map[string]json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	delta, hasDelta, err := parsePolicyDelta(payload)
	if err != nil {
		return err
	}
	if !hasDelta {
		return nil
	}
	return govcfg.PreflightPolicyApply(cur, delta)
}

func parsePolicyDelta(payload map[string]json.RawMessage) (govcfg.PolicyDelta, bool, error) {
	var delta govcfg.PolicyDelta
	var hasDelta bool
	for key, raw := range payload {
		switch key {
		case "gov.tally.QuorumBps":
			value, err := parseUint64(raw)
			if err != nil {
				return govcfg.PolicyDelta{}, false, fmt.Errorf("gov.tally.QuorumBps: %w", err)
			}
			if value > math.MaxUint32 {
				return govcfg.PolicyDelta{}, false, fmt.Errorf("gov.tally.QuorumBps: exceeds uint32 bounds")
			}
			v := uint32(value)
			if delta.Governance == nil {
				delta.Governance = &govcfg.GovernanceDelta{}
			}
			delta.Governance.QuorumBPS = &v
			hasDelta = true
		case "gov.tally.ThresholdBps":
			value, err := parseUint64(raw)
			if err != nil {
				return govcfg.PolicyDelta{}, false, fmt.Errorf("gov.tally.ThresholdBps: %w", err)
			}
			if value > math.MaxUint32 {
				return govcfg.PolicyDelta{}, false, fmt.Errorf("gov.tally.ThresholdBps: exceeds uint32 bounds")
			}
			v := uint32(value)
			if delta.Governance == nil {
				delta.Governance = &govcfg.GovernanceDelta{}
			}
			delta.Governance.PassThresholdBPS = &v
			hasDelta = true
		}
	}
	return delta, hasDelta, nil
}

func parseUint64(raw json.RawMessage) (uint64, error) {
	var value interface{}
	dec := json.NewDecoder(bytesFrom(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return 0, err
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid integer")
		}
		return uint64(parsed), nil
	case float64:
		if typed < 0 {
			return 0, fmt.Errorf("invalid integer")
		}
		return uint64(typed), nil
	default:
		return 0, fmt.Errorf("invalid integer")
	}
}

func bytesFrom(raw json.RawMessage) *bytes.Reader {
	return bytes.NewReader(raw)
}
