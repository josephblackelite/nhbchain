package fees

import (
	"log"
	"math/big"
	"strings"
	"sync"
	"time"
)

// DomainPOS identifies point-of-sale payment flows.
const DomainPOS = "pos"

// Default configuration values applied when policies omit explicit settings.
const (
	DefaultFreeTierTxPerMonth = uint64(100)
	DefaultMDRBasisPoints     = uint32(150)
)

// Free tier counter scopes.
const (
	FreeTierScopeAggregate = "__AGGREGATE__"
)

var freeTierDefaultWarned sync.Map

// Asset identifiers supported by the fee engine.
const (
	AssetNHB  = "NHB"
	AssetZNHB = "ZNHB"
)

// AssetPolicy captures the routing configuration for a specific asset within a domain.
type AssetPolicy struct {
	MDRBasisPoints uint32
	OwnerWallet    [20]byte
}

// DomainPolicy captures the configuration applied to a specific fee domain.
type DomainPolicy struct {
	FreeTierTxPerMonth uint64
	MDRBasisPoints     uint32
	OwnerWallet        [20]byte
	Assets             map[string]AssetPolicy
	FreeTierPerAsset   bool
}

// NormalizeAsset canonicalises asset identifiers for consistent lookups.
func NormalizeAsset(asset string) string {
	return strings.ToUpper(strings.TrimSpace(asset))
}

func isZeroWallet(addr [20]byte) bool {
	for _, b := range addr {
		if b != 0 {
			return false
		}
	}
	return true
}

func (p DomainPolicy) normalized(domain string) DomainPolicy {
	normalized := p
	if normalized.FreeTierTxPerMonth == 0 {
		normalized.FreeTierTxPerMonth = DefaultFreeTierTxPerMonth
		if domain = strings.TrimSpace(domain); domain != "" {
			if _, logged := freeTierDefaultWarned.LoadOrStore(strings.ToLower(domain), struct{}{}); !logged {
				log.Printf("fees: domain %s missing FreeTierTxPerMonth, defaulting to %d", domain, DefaultFreeTierTxPerMonth)
			}
		} else {
			if _, logged := freeTierDefaultWarned.LoadOrStore("", struct{}{}); !logged {
				log.Printf("fees: missing FreeTierTxPerMonth, defaulting to %d", DefaultFreeTierTxPerMonth)
			}
		}
	}
	if normalized.MDRBasisPoints == 0 {
		normalized.MDRBasisPoints = DefaultMDRBasisPoints
	}
	var assets map[string]AssetPolicy
	if len(normalized.Assets) > 0 {
		assets = make(map[string]AssetPolicy, len(normalized.Assets))
		for name, cfg := range normalized.Assets {
			asset := NormalizeAsset(name)
			if asset == "" {
				continue
			}
			if cfg.MDRBasisPoints == 0 {
				cfg.MDRBasisPoints = normalized.MDRBasisPoints
			}
			if cfg.MDRBasisPoints == 0 {
				cfg.MDRBasisPoints = DefaultMDRBasisPoints
			}
			if isZeroWallet(cfg.OwnerWallet) && !isZeroWallet(normalized.OwnerWallet) {
				cfg.OwnerWallet = normalized.OwnerWallet
			}
			assets[asset] = cfg
		}
	}
	if len(assets) == 0 {
		assets = map[string]AssetPolicy{
			AssetNHB: {
				MDRBasisPoints: normalized.MDRBasisPoints,
				OwnerWallet:    normalized.OwnerWallet,
			},
		}
	}
	normalized.Assets = assets
	return normalized
}

// FreeTierScope resolves the counter scope applied when tracking free-tier usage.
// Domains aggregate usage across all assets by default. When FreeTierPerAsset is
// set the scope narrows to the supplied asset.
func (p DomainPolicy) FreeTierScope(asset string) string {
	if !p.FreeTierPerAsset {
		return FreeTierScopeAggregate
	}
	normalized := NormalizeAsset(asset)
	if normalized == "" {
		return FreeTierScopeAggregate
	}
	return normalized
}

// NormalizeFreeTierScope canonicalises counter scope identifiers for storage.
func NormalizeFreeTierScope(scope string) string {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return FreeTierScopeAggregate
	}
	upper := strings.ToUpper(trimmed)
	if upper == FreeTierScopeAggregate {
		return FreeTierScopeAggregate
	}
	asset := NormalizeAsset(trimmed)
	if asset == "" {
		return FreeTierScopeAggregate
	}
	return asset
}

// AssetConfig resolves the routing policy for the supplied asset.
func (p DomainPolicy) AssetConfig(asset string) (AssetPolicy, bool) {
	if len(p.Assets) == 0 {
		return AssetPolicy{}, false
	}
	normalized := NormalizeAsset(asset)
	if normalized == "" {
		return AssetPolicy{}, false
	}
	cfg, ok := p.Assets[normalized]
	return cfg, ok
}

// Policy enumerates the configured fee domains and the policy version.
type Policy struct {
	Version uint64
	Domains map[string]DomainPolicy
}

// Clone returns a deep copy of the policy to avoid accidental aliasing of the
// domain map between callers.
func (p Policy) Clone() Policy {
	clone := Policy{Version: p.Version}
	if len(p.Domains) == 0 {
		clone.Domains = map[string]DomainPolicy{}
		return clone
	}
	clone.Domains = make(map[string]DomainPolicy, len(p.Domains))
	for domain, cfg := range p.Domains {
		normalized := NormalizeDomain(domain)
		clone.Domains[normalized] = cfg.normalized(normalized)
	}
	return clone
}

// DomainConfig resolves the policy for the supplied domain if configured.
func (p Policy) DomainConfig(domain string) (DomainPolicy, bool) {
	if len(p.Domains) == 0 {
		return DomainPolicy{}, false
	}
	normalized := NormalizeDomain(domain)
	cfg, ok := p.Domains[normalized]
	if !ok {
		return DomainPolicy{}, false
	}
	return cfg.normalized(normalized), true
}

// NormalizeDomain canonicalises domain identifiers for consistent lookups.
func NormalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

// ApplyInput captures the context required to evaluate the fee obligation for
// a transaction.
type ApplyInput struct {
	Domain        string
	Gross         *big.Int
	UsageCount    uint64
	PolicyVersion uint64
	Config        DomainPolicy
	WindowStart   time.Time
	Asset         string
}

// ApplyResult summarises the computed fee, resulting net amount, and updated
// usage counter after evaluating the provided input against the policy.
type ApplyResult struct {
	Fee               *big.Int
	Net               *big.Int
	Counter           uint64
	OwnerWallet       [20]byte
	PolicyVersion     uint64
	FreeTierApplied   bool
	FreeTierLimit     uint64
	FreeTierRemaining uint64
	FeeBasisPoints    uint32
	WindowStart       time.Time
	Asset             string
}

// Apply evaluates the policy for the supplied domain and returns the resulting
// fee metrics. The caller is responsible for persisting the incremented counter
// and routing balances.
func Apply(input ApplyInput) ApplyResult {
	policy := input.Config.normalized(input.Domain)
	result := ApplyResult{
		Counter:       input.UsageCount + 1,
		PolicyVersion: input.PolicyVersion,
		FreeTierLimit: policy.FreeTierTxPerMonth,
		WindowStart:   input.WindowStart,
		Asset:         NormalizeAsset(input.Asset),
	}
	result.Fee = big.NewInt(0)
	if input.Gross != nil {
		result.Net = new(big.Int).Set(input.Gross)
	} else {
		result.Net = big.NewInt(0)
	}
	if result.Net.Sign() <= 0 {
		return result
	}
	if assetCfg, ok := policy.AssetConfig(result.Asset); ok {
		result.OwnerWallet = assetCfg.OwnerWallet
		result.FeeBasisPoints = assetCfg.MDRBasisPoints
	}
	limit := policy.FreeTierTxPerMonth
	if limit > 0 && input.UsageCount < limit {
		result.FreeTierApplied = true
		if limit > result.Counter {
			result.FreeTierRemaining = limit - result.Counter
		} else {
			result.FreeTierRemaining = 0
		}
		return result
	}
	if result.FeeBasisPoints == 0 {
		return result
	}
	fee := new(big.Int).Mul(result.Net, big.NewInt(int64(result.FeeBasisPoints)))
	fee = fee.Div(fee, big.NewInt(10_000))
	if fee.Sign() <= 0 {
		return result
	}
	if fee.Cmp(result.Net) >= 0 {
		result.Fee = new(big.Int).Set(result.Net)
		result.Net = big.NewInt(0)
		return result
	}
	result.Fee = fee
	result.Net = new(big.Int).Sub(result.Net, fee)
	if limit > result.Counter {
		result.FreeTierRemaining = limit - result.Counter
	}
	return result
}

// Totals aggregates fee accounting metrics per domain, asset, and wallet.
type Totals struct {
	Domain string
	Asset  string
	Wallet [20]byte
	Gross  *big.Int
	Fee    *big.Int
	Net    *big.Int
}

// Clone returns a copy of the totals structure with duplicated big.Int values.
func (t Totals) Clone() Totals {
	clone := Totals{Domain: t.Domain, Asset: t.Asset, Wallet: t.Wallet}
	if t.Gross != nil {
		clone.Gross = new(big.Int).Set(t.Gross)
	}
	if t.Fee != nil {
		clone.Fee = new(big.Int).Set(t.Fee)
	}
	if t.Net != nil {
		clone.Net = new(big.Int).Set(t.Net)
	}
	return clone
}
