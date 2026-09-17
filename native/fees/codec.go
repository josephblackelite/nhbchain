package fees

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON tracks whether the free-tier allowance was explicitly provided
// so that zero-valued configurations can disable the allowance without being
// mistaken for legacy defaults.
func (p *DomainPolicy) UnmarshalJSON(data []byte) error {
	type alias DomainPolicy
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = DomainPolicy(decoded)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range raw {
		if equalFreeTierKey(key) {
			p.freeTierExplicit = true
			break
		}
	}
	return nil
}

// UnmarshalTOML performs a best-effort conversion from snake_case TOML keys
// into the camelCase JSON structure used throughout the fee engine. The
// explicit free-tier flag is tracked so that zero values survive normalization.
func (p *DomainPolicy) UnmarshalTOML(data interface{}) error {
	table, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("fees: domain policy must decode from a table")
	}
	normalized := normalizeDomainPolicyTable(table)

	type alias DomainPolicy
	var decoded alias
	blob, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		return err
	}
	*p = DomainPolicy(decoded)
	if _, ok := normalized["freeTierTxPerMonth"]; ok {
		p.freeTierExplicit = true
	}
	return nil
}

func normalizeDomainPolicyTable(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		switch {
		case equalFreeTierKey(key):
			out["freeTierTxPerMonth"] = value
		case strings.EqualFold(key, "mdr_basis_points"):
			out["mdrBasisPoints"] = value
		case strings.EqualFold(key, "owner_wallet"):
			out["ownerWallet"] = value
		case strings.EqualFold(key, "free_tier_per_asset"):
			out["freeTierPerAsset"] = value
		case strings.EqualFold(key, "assets"):
			out["assets"] = normalizeAssetPolicies(value)
		default:
			out[key] = value
		}
	}
	return out
}

func normalizeAssetPolicies(value interface{}) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	if len(list) == 0 {
		return value
	}
	converted := make([]interface{}, len(list))
	for i, item := range list {
		if table, ok := item.(map[string]interface{}); ok {
			converted[i] = normalizeAssetTable(table)
			continue
		}
		converted[i] = item
	}
	return converted
}

func normalizeAssetTable(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		switch {
		case strings.EqualFold(key, "mdr_basis_points"):
			out["mdrBasisPoints"] = value
		case strings.EqualFold(key, "owner_wallet"):
			out["ownerWallet"] = value
		default:
			out[key] = value
		}
	}
	return out
}

func equalFreeTierKey(key string) bool {
	return strings.EqualFold(key, "freeTierTxPerMonth") || strings.EqualFold(key, "free_tier_tx_per_month")
}
