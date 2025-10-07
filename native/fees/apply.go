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

var freeTierDefaultWarned sync.Map

// DomainPolicy captures the configuration applied to a specific fee domain.
type DomainPolicy struct {
	FreeTierTxPerMonth uint64
	MDRBasisPoints     uint32
	OwnerWallet        [20]byte
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
	return normalized
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
	WindowStart   time.Time
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
}

// Apply evaluates the policy for the supplied domain and returns the resulting
// fee metrics. The caller is responsible for persisting the incremented counter
// and routing balances.
func Apply(input ApplyInput) ApplyResult {
	policy := input.Config.normalized(input.Domain)
	result := ApplyResult{
		Counter:        input.UsageCount + 1,
		PolicyVersion:  input.PolicyVersion,
		OwnerWallet:    policy.OwnerWallet,
		FreeTierLimit:  policy.FreeTierTxPerMonth,
		FeeBasisPoints: policy.MDRBasisPoints,
		WindowStart:    input.WindowStart,
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
	if policy.MDRBasisPoints == 0 {
		return result
	}
	fee := new(big.Int).Mul(result.Net, big.NewInt(int64(policy.MDRBasisPoints)))
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
