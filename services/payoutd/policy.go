package payoutd

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrPolicyNotFound indicates that no policy exists for the requested asset.
var ErrPolicyNotFound = errors.New("payoutd: policy not found")

// ErrDailyCapExceeded indicates that applying a payout would exceed the configured window cap.
var ErrDailyCapExceeded = errors.New("payoutd: daily cap exceeded")

// ErrSoftBalanceExceeded reports that the treasury soft inventory would be exhausted by a payout.
var ErrSoftBalanceExceeded = errors.New("payoutd: insufficient soft inventory")

// Policy captures throttling rules for a single stable asset.
type Policy struct {
	Asset         string
	DailyCap      *big.Int
	SoftInventory *big.Int
	Confirmations int
}

// policyFile mirrors the YAML representation of a policy entry.
type policyFile struct {
	Asset         string `yaml:"asset"`
	DailyCap      string `yaml:"daily_cap"`
	SoftInventory string `yaml:"soft_inventory"`
	Confirmations int    `yaml:"confirmations"`
}

// LoadPolicies reads policies from the provided YAML file on disk.
func LoadPolicies(path string) ([]Policy, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open policies: %w", err)
	}
	defer file.Close()
	dec := yaml.NewDecoder(file)
	var entries []policyFile
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode policies: %w", err)
	}
	policies := make([]Policy, 0, len(entries))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		asset := strings.ToUpper(strings.TrimSpace(entry.Asset))
		if asset == "" {
			return nil, fmt.Errorf("policy asset required")
		}
		if _, exists := seen[asset]; exists {
			return nil, fmt.Errorf("duplicate policy for asset %s", asset)
		}
		capAmount, err := parseDecimal(entry.DailyCap)
		if err != nil {
			return nil, fmt.Errorf("asset %s daily_cap: %w", asset, err)
		}
		inventory, err := parseDecimal(entry.SoftInventory)
		if err != nil {
			return nil, fmt.Errorf("asset %s soft_inventory: %w", asset, err)
		}
		confirmations := entry.Confirmations
		if confirmations <= 0 {
			confirmations = 3
		}
		policies = append(policies, Policy{
			Asset:         asset,
			DailyCap:      capAmount,
			SoftInventory: inventory,
			Confirmations: confirmations,
		})
		seen[asset] = struct{}{}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Asset < policies[j].Asset })
	return policies, nil
}

func parseDecimal(raw string) (*big.Int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer amount %q", raw)
	}
	if value.Sign() < 0 {
		return nil, fmt.Errorf("amount must be non-negative")
	}
	return value, nil
}

// PolicyEnforcer coordinates access to the configured payout caps.
type PolicyEnforcer struct {
	mu            sync.Mutex
	policies      map[string]Policy
	totals        map[string]map[string]*big.Int
	inventory     map[string]*big.Int
	confirmations map[string]int
}

// NewPolicyEnforcer constructs an enforcer for the supplied policies.
func NewPolicyEnforcer(policies []Policy) (*PolicyEnforcer, error) {
	if len(policies) == 0 {
		return nil, fmt.Errorf("at least one policy must be configured")
	}
	registry := make(map[string]Policy, len(policies))
	totals := make(map[string]map[string]*big.Int, len(policies))
	inventory := make(map[string]*big.Int, len(policies))
	confirmations := make(map[string]int, len(policies))
	for _, policy := range policies {
		asset := strings.ToUpper(strings.TrimSpace(policy.Asset))
		if asset == "" {
			return nil, fmt.Errorf("policy asset required")
		}
		if _, exists := registry[asset]; exists {
			return nil, fmt.Errorf("duplicate policy for asset %s", asset)
		}
		if policy.DailyCap == nil {
			policy.DailyCap = big.NewInt(0)
		}
		if policy.SoftInventory == nil {
			policy.SoftInventory = big.NewInt(0)
		}
		registry[asset] = Policy{
			Asset:         asset,
			DailyCap:      new(big.Int).Set(policy.DailyCap),
			SoftInventory: new(big.Int).Set(policy.SoftInventory),
			Confirmations: policy.Confirmations,
		}
		totals[asset] = make(map[string]*big.Int)
		inventory[asset] = new(big.Int).Set(policy.SoftInventory)
		confirmations[asset] = policy.Confirmations
		if confirmations[asset] <= 0 {
			confirmations[asset] = 3
		}
	}
	return &PolicyEnforcer{
		policies:      registry,
		totals:        totals,
		inventory:     inventory,
		confirmations: confirmations,
	}, nil
}

// Validate ensures a payout complies with the configured caps.
func (p *PolicyEnforcer) Validate(asset string, amount *big.Int, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateLocked(asset, amount, now)
}

func (p *PolicyEnforcer) validateLocked(asset string, amount *big.Int, now time.Time) error {
	policy, ok := p.policies[strings.ToUpper(strings.TrimSpace(asset))]
	if !ok {
		return ErrPolicyNotFound
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("payout amount must be positive")
	}
	inventory := p.inventory[policy.Asset]
	if inventory != nil && inventory.Cmp(amount) < 0 {
		return ErrSoftBalanceExceeded
	}
	dayKey := dayBucket(now)
	spent := p.totals[policy.Asset][dayKey]
	if spent == nil {
		spent = big.NewInt(0)
	}
	remaining := new(big.Int).Sub(policy.DailyCap, spent)
	if remaining.Sign() < 0 {
		remaining = big.NewInt(0)
	}
	if remaining.Cmp(amount) < 0 {
		return ErrDailyCapExceeded
	}
	return nil
}

// Record notes a successful payout against the configured caps.
func (p *PolicyEnforcer) Record(asset string, amount *big.Int, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	policy, ok := p.policies[strings.ToUpper(strings.TrimSpace(asset))]
	if !ok {
		return
	}
	if amount == nil {
		return
	}
	dayKey := dayBucket(now)
	if _, ok := p.totals[policy.Asset][dayKey]; !ok {
		p.totals[policy.Asset][dayKey] = big.NewInt(0)
	}
	p.totals[policy.Asset][dayKey].Add(p.totals[policy.Asset][dayKey], amount)
	if inv := p.inventory[policy.Asset]; inv != nil {
		inv.Sub(inv, amount)
		if inv.Sign() < 0 {
			inv.SetInt64(0)
		}
	}
}

// SetInventory overrides the tracked soft inventory for an asset.
func (p *PolicyEnforcer) SetInventory(asset string, balance *big.Int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(asset))
	if _, ok := p.policies[key]; !ok {
		return
	}
	if balance == nil {
		p.inventory[key] = big.NewInt(0)
		return
	}
	p.inventory[key] = new(big.Int).Set(balance)
}

// RemainingCap reports the remaining allowance for the asset in the current window.
func (p *PolicyEnforcer) RemainingCap(asset string, now time.Time) *big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(asset))
	policy, ok := p.policies[key]
	if !ok {
		return big.NewInt(0)
	}
	dayKey := dayBucket(now)
	spent := p.totals[key][dayKey]
	if spent == nil {
		spent = big.NewInt(0)
	} else {
		spent = new(big.Int).Set(spent)
	}
	remaining := new(big.Int).Sub(policy.DailyCap, spent)
	if remaining.Sign() < 0 {
		remaining = big.NewInt(0)
	}
	return remaining
}

// DailyCap returns the configured total cap for the asset.
func (p *PolicyEnforcer) DailyCap(asset string) *big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(asset))
	policy, ok := p.policies[key]
	if !ok || policy.DailyCap == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(policy.DailyCap)
}

// RemainingInventory returns the current tracked soft inventory for an asset.
func (p *PolicyEnforcer) RemainingInventory(asset string) *big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(asset))
	balance := p.inventory[key]
	if balance == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(balance)
}

// Confirmations returns the configured confirmation count for the asset.
func (p *PolicyEnforcer) Confirmations(asset string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(asset))
	if conf, ok := p.confirmations[key]; ok && conf > 0 {
		return conf
	}
	return 3
}

// Snapshot returns the remaining cap per asset for observability endpoints.
func (p *PolicyEnforcer) Snapshot(now time.Time) map[string]*big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*big.Int, len(p.policies))
	dayKey := dayBucket(now)
	for asset, policy := range p.policies {
		spent := p.totals[asset][dayKey]
		if spent == nil {
			spent = big.NewInt(0)
		}
		remaining := new(big.Int).Sub(policy.DailyCap, spent)
		if remaining.Sign() < 0 {
			remaining = big.NewInt(0)
		}
		out[asset] = remaining
	}
	return out
}

func dayBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
