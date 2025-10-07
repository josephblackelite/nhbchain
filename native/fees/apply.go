package fees

import (
	"math/big"
	"strings"
)

// DomainPOS identifies point-of-sale payment flows.
const DomainPOS = "pos"

// DomainPolicy captures the configuration applied to a specific fee domain.
type DomainPolicy struct {
	FreeTierAllowance uint64
	MDRBps            uint32
	RouteWallet       [20]byte
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
		clone.Domains[normalized] = cfg
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
	return cfg, ok
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
}

// ApplyResult summarises the computed fee, resulting net amount, and updated
// usage counter after evaluating the provided input against the policy.
type ApplyResult struct {
	Fee             *big.Int
	Net             *big.Int
	Counter         uint64
	RouteWallet     [20]byte
	PolicyVersion   uint64
	FreeTierApplied bool
}

// Apply evaluates the policy for the supplied domain and returns the resulting
// fee metrics. The caller is responsible for persisting the incremented counter
// and routing balances.
func Apply(input ApplyInput) ApplyResult {
	result := ApplyResult{Counter: input.UsageCount + 1, PolicyVersion: input.PolicyVersion, RouteWallet: input.Config.RouteWallet}
	result.Fee = big.NewInt(0)
	if input.Gross != nil {
		result.Net = new(big.Int).Set(input.Gross)
	} else {
		result.Net = big.NewInt(0)
	}
	if result.Net.Sign() <= 0 {
		return result
	}
	if input.Config.FreeTierAllowance > input.UsageCount {
		result.FreeTierApplied = true
		return result
	}
	if input.Config.MDRBps == 0 {
		return result
	}
	fee := new(big.Int).Mul(result.Net, big.NewInt(int64(input.Config.MDRBps)))
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
	return result
}

// Totals aggregates fee accounting metrics per domain and wallet.
type Totals struct {
	Domain string
	Wallet [20]byte
	Gross  *big.Int
	Fee    *big.Int
	Net    *big.Int
}

// Clone returns a copy of the totals structure with duplicated big.Int values.
func (t Totals) Clone() Totals {
	clone := Totals{Domain: t.Domain, Wallet: t.Wallet}
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
