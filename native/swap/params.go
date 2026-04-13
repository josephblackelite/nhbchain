package swap

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// SlippageTolerance captures the allowed deviation between the computed mint amount
// and the submitted voucher amount. Values are expressed in basis points.
type SlippageTolerance struct {
	MaxBps uint64
}

// NewSlippageTolerance constructs a slippage tolerance guardrail, validating the
// configured ceiling falls within a reasonable domain.
func NewSlippageTolerance(bps uint64) (SlippageTolerance, error) {
	if bps > 10000 {
		return SlippageTolerance{}, fmt.Errorf("swap: slippage tolerance must not exceed 10000 bps")
	}
	return SlippageTolerance{MaxBps: bps}, nil
}

// Enabled reports whether the tolerance will enforce deviations.
func (s SlippageTolerance) Enabled() bool {
	return s.MaxBps > 0
}

// OracleGuardrails describes oracle freshness and deviation tolerances.
type OracleGuardrails struct {
	MaxAge          time.Duration
	MaxDeviationBps uint64
}

// NewOracleGuardrails normalises the supplied parameters and verifies bounds.
func NewOracleGuardrails(maxAge time.Duration, deviation uint64) (OracleGuardrails, error) {
	if maxAge < 0 {
		return OracleGuardrails{}, fmt.Errorf("swap: oracle max age must be positive")
	}
	if deviation > 10000 {
		return OracleGuardrails{}, fmt.Errorf("swap: oracle deviation must not exceed 10000 bps")
	}
	return OracleGuardrails{MaxAge: maxAge, MaxDeviationBps: deviation}, nil
}

// CashOutAssetCapConfig models a per-asset daily cap parsed from configuration.
type CashOutAssetCapConfig struct {
	Asset       string `toml:"Asset"`
	DailyCapWei string `toml:"DailyCapWei"`
}

// CashOutTierConfig models a KYC tier cap parsed from configuration.
type CashOutTierConfig struct {
	Tier          string `toml:"Tier"`
	DailyCapWei   string `toml:"DailyCapWei"`
	MonthlyCapWei string `toml:"MonthlyCapWei"`
}

// CashOutConfig aggregates the cash-out risk controls sourced from configuration.
type CashOutConfig struct {
	AssetCaps []CashOutAssetCapConfig `toml:"AssetCaps"`
	Tiers     []CashOutTierConfig     `toml:"Tiers"`
}

// Normalise trims whitespace, removes duplicates, and applies canonical casing.
func (cfg CashOutConfig) Normalise() CashOutConfig {
	normalized := CashOutConfig{}
	if len(cfg.AssetCaps) > 0 {
		seen := make(map[string]struct{}, len(cfg.AssetCaps))
		for _, entry := range cfg.AssetCaps {
			asset := strings.ToUpper(strings.TrimSpace(entry.Asset))
			if asset == "" {
				continue
			}
			if _, exists := seen[asset]; exists {
				continue
			}
			seen[asset] = struct{}{}
			normalized.AssetCaps = append(normalized.AssetCaps, CashOutAssetCapConfig{
				Asset:       asset,
				DailyCapWei: strings.TrimSpace(entry.DailyCapWei),
			})
		}
		sort.Slice(normalized.AssetCaps, func(i, j int) bool {
			return normalized.AssetCaps[i].Asset < normalized.AssetCaps[j].Asset
		})
	}
	if len(cfg.Tiers) > 0 {
		seen := make(map[string]struct{}, len(cfg.Tiers))
		for _, entry := range cfg.Tiers {
			tier := strings.TrimSpace(entry.Tier)
			if tier == "" {
				continue
			}
			key := strings.ToLower(tier)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			normalized.Tiers = append(normalized.Tiers, CashOutTierConfig{
				Tier:          tier,
				DailyCapWei:   strings.TrimSpace(entry.DailyCapWei),
				MonthlyCapWei: strings.TrimSpace(entry.MonthlyCapWei),
			})
		}
		sort.Slice(normalized.Tiers, func(i, j int) bool {
			return strings.ToLower(normalized.Tiers[i].Tier) < strings.ToLower(normalized.Tiers[j].Tier)
		})
	}
	return normalized
}

// CashOutTierLimits represents the parsed daily/monthly limits for a KYC tier.
type CashOutTierLimits struct {
	Tier          string
	DailyCapWei   *big.Int
	MonthlyCapWei *big.Int
}

// CashOutParameters contains runtime representations of the cash-out guardrails.
type CashOutParameters struct {
	AssetDailyCaps map[StableAsset]*big.Int
	TierCaps       map[string]CashOutTierLimits
}

// Parameters converts the textual configuration into runtime big integers and bounds.
func (cfg CashOutConfig) Parameters() (CashOutParameters, error) {
	normalized := cfg.Normalise()
	params := CashOutParameters{
		AssetDailyCaps: make(map[StableAsset]*big.Int, len(normalized.AssetCaps)),
		TierCaps:       make(map[string]CashOutTierLimits, len(normalized.Tiers)),
	}
	for _, entry := range normalized.AssetCaps {
		asset, err := normaliseAsset(StableAsset(entry.Asset))
		if err != nil {
			return params, fmt.Errorf("risk: invalid cash-out asset cap for %s: %w", entry.Asset, err)
		}
		amount, err := parseWeiAmount(entry.DailyCapWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid cash-out daily cap for %s: %w", asset, err)
		}
		params.AssetDailyCaps[asset] = amount
	}
	for _, entry := range normalized.Tiers {
		tierName := strings.TrimSpace(entry.Tier)
		if tierName == "" {
			return params, fmt.Errorf("risk: tier identifier required")
		}
		dailyCap, err := parseWeiAmount(entry.DailyCapWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid tier daily cap for %s: %w", tierName, err)
		}
		monthlyCap, err := parseWeiAmount(entry.MonthlyCapWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid tier monthly cap for %s: %w", tierName, err)
		}
		key := strings.ToLower(tierName)
		params.TierCaps[key] = CashOutTierLimits{
			Tier:          tierName,
			DailyCapWei:   dailyCap,
			MonthlyCapWei: monthlyCap,
		}
	}
	return params, nil
}

// Clone returns a deep copy of the parameters.
func (p CashOutParameters) Clone() CashOutParameters {
	clone := CashOutParameters{
		AssetDailyCaps: make(map[StableAsset]*big.Int, len(p.AssetDailyCaps)),
		TierCaps:       make(map[string]CashOutTierLimits, len(p.TierCaps)),
	}
	for asset, cap := range p.AssetDailyCaps {
		if cap != nil {
			clone.AssetDailyCaps[asset] = new(big.Int).Set(cap)
		} else {
			clone.AssetDailyCaps[asset] = nil
		}
	}
	for key, limits := range p.TierCaps {
		copyLimits := CashOutTierLimits{Tier: limits.Tier}
		if limits.DailyCapWei != nil {
			copyLimits.DailyCapWei = new(big.Int).Set(limits.DailyCapWei)
		}
		if limits.MonthlyCapWei != nil {
			copyLimits.MonthlyCapWei = new(big.Int).Set(limits.MonthlyCapWei)
		}
		clone.TierCaps[key] = copyLimits
	}
	return clone
}

// AssetDailyCap returns a defensive copy of the configured per-asset daily cap.
func (p CashOutParameters) AssetDailyCap(asset StableAsset) (*big.Int, bool) {
	cap, ok := p.AssetDailyCaps[asset]
	if !ok || cap == nil {
		return nil, false
	}
	return new(big.Int).Set(cap), true
}

// Tier returns the guardrails configured for the supplied tier identifier.
func (p CashOutParameters) Tier(tier string) (CashOutTierLimits, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(tier))
	if trimmed == "" {
		return CashOutTierLimits{}, false
	}
	limits, ok := p.TierCaps[trimmed]
	if !ok {
		return CashOutTierLimits{}, false
	}
	clone := CashOutTierLimits{Tier: limits.Tier}
	if limits.DailyCapWei != nil {
		clone.DailyCapWei = new(big.Int).Set(limits.DailyCapWei)
	}
	if limits.MonthlyCapWei != nil {
		clone.MonthlyCapWei = new(big.Int).Set(limits.MonthlyCapWei)
	}
	return clone, true
}

// SlippageTolerance returns a runtime representation of the slippage guardrail.
func (c Config) SlippageTolerance() (SlippageTolerance, error) {
	return NewSlippageTolerance(c.SlippageBps)
}

// OracleGuardrails returns the oracle guardrails derived from configuration.
func (c Config) OracleGuardrails() (OracleGuardrails, error) {
	return NewOracleGuardrails(c.MaxQuoteAge(), c.PriceProofMaxDeviationBps)
}
