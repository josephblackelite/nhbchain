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

// ErrUserDailyCapExceeded indicates an account-level daily amount cap was exceeded.
var ErrUserDailyCapExceeded = errors.New("payoutd: account daily cap exceeded")

// ErrUserHourlyCapExceeded indicates an account-level hourly amount cap was exceeded.
var ErrUserHourlyCapExceeded = errors.New("payoutd: account hourly cap exceeded")

// ErrDestinationDailyCapExceeded indicates a destination-level daily amount cap was exceeded.
var ErrDestinationDailyCapExceeded = errors.New("payoutd: destination daily cap exceeded")

// ErrVelocityExceeded indicates the payout frequency exceeded the configured policy.
var ErrVelocityExceeded = errors.New("payoutd: payout velocity exceeded")

// ErrDestinationBlocked indicates the payout destination is blocked by policy.
var ErrDestinationBlocked = errors.New("payoutd: destination blocked")

// ErrDestinationNotAllowed indicates the payout destination does not satisfy the allowlist.
var ErrDestinationNotAllowed = errors.New("payoutd: destination not allowed")

// ErrAccountBlocked indicates the payout account is blocked by policy.
var ErrAccountBlocked = errors.New("payoutd: account blocked")

// ErrRegionBlocked indicates the payout region is blocked by policy.
var ErrRegionBlocked = errors.New("payoutd: region blocked")

// ErrPartnerBlocked indicates the payout partner is blocked by policy.
var ErrPartnerBlocked = errors.New("payoutd: partner blocked")

// ErrManualReviewRequired indicates a payout requires manual approval before execution.
var ErrManualReviewRequired = errors.New("payoutd: manual review required")

// Policy captures throttling and screening rules for a single stable asset.
type Policy struct {
	Asset                    string
	DailyCap                 *big.Int
	SoftInventory            *big.Int
	Confirmations            int
	PerUserDailyCap          *big.Int
	PerUserHourlyCap         *big.Int
	PerDestinationDailyCap   *big.Int
	MaxPayoutsPerHour        int
	MaxPayoutsPerDay         int
	RequireReviewAbove       *big.Int
	RequireReviewForRegions  []string
	RequireReviewForPartners []string
	BlockedDestinations      []string
	AllowedDestinationPrefix []string
	BlockedAccounts          []string
	BlockedRegions           []string
	BlockedPartners          []string
}

// PolicyCheck captures the identity and destination metadata required for policy evaluation.
type PolicyCheck struct {
	Asset             string
	Amount            *big.Int
	Account           string
	Destination       string
	PartnerID         string
	Region            string
	RequestedBy       string
	ApprovedBy        string
	ApprovalReference string
}

// policyFile mirrors the YAML representation of a policy entry.
type policyFile struct {
	Asset                    string   `yaml:"asset"`
	DailyCap                 string   `yaml:"daily_cap"`
	SoftInventory            string   `yaml:"soft_inventory"`
	Confirmations            int      `yaml:"confirmations"`
	PerUserDailyCap          string   `yaml:"per_user_daily_cap"`
	PerUserHourlyCap         string   `yaml:"per_user_hourly_cap"`
	PerDestinationDailyCap   string   `yaml:"per_destination_daily_cap"`
	MaxPayoutsPerHour        int      `yaml:"max_payouts_per_hour"`
	MaxPayoutsPerDay         int      `yaml:"max_payouts_per_day"`
	RequireReviewAbove       string   `yaml:"require_review_above"`
	RequireReviewForRegions  []string `yaml:"require_review_for_regions"`
	RequireReviewForPartners []string `yaml:"require_review_for_partners"`
	BlockedDestinations      []string `yaml:"blocked_destinations"`
	AllowedDestinationPrefix []string `yaml:"allowed_destination_prefixes"`
	BlockedAccounts          []string `yaml:"blocked_accounts"`
	BlockedRegions           []string `yaml:"blocked_regions"`
	BlockedPartners          []string `yaml:"blocked_partners"`
}

type compiledPolicy struct {
	Policy

	blockedDestinations      map[string]struct{}
	blockedAccounts          map[string]struct{}
	blockedRegions           map[string]struct{}
	blockedPartners          map[string]struct{}
	requireReviewForRegions  map[string]struct{}
	requireReviewForPartners map[string]struct{}
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
		policy := Policy{
			Asset:                    asset,
			Confirmations:            entry.Confirmations,
			MaxPayoutsPerHour:        entry.MaxPayoutsPerHour,
			MaxPayoutsPerDay:         entry.MaxPayoutsPerDay,
			RequireReviewForRegions:  normalizeStringList(entry.RequireReviewForRegions),
			RequireReviewForPartners: normalizeStringList(entry.RequireReviewForPartners),
			BlockedDestinations:      normalizeStringList(entry.BlockedDestinations),
			AllowedDestinationPrefix: normalizeStringList(entry.AllowedDestinationPrefix),
			BlockedAccounts:          normalizeStringList(entry.BlockedAccounts),
			BlockedRegions:           normalizeStringList(entry.BlockedRegions),
			BlockedPartners:          normalizeStringList(entry.BlockedPartners),
		}
		if policy.Confirmations <= 0 {
			policy.Confirmations = 3
		}
		policy.DailyCap, err = parseDecimal(entry.DailyCap)
		if err != nil {
			return nil, fmt.Errorf("asset %s daily_cap: %w", asset, err)
		}
		policy.SoftInventory, err = parseDecimal(entry.SoftInventory)
		if err != nil {
			return nil, fmt.Errorf("asset %s soft_inventory: %w", asset, err)
		}
		policy.PerUserDailyCap, err = parseDecimal(entry.PerUserDailyCap)
		if err != nil {
			return nil, fmt.Errorf("asset %s per_user_daily_cap: %w", asset, err)
		}
		policy.PerUserHourlyCap, err = parseDecimal(entry.PerUserHourlyCap)
		if err != nil {
			return nil, fmt.Errorf("asset %s per_user_hourly_cap: %w", asset, err)
		}
		policy.PerDestinationDailyCap, err = parseDecimal(entry.PerDestinationDailyCap)
		if err != nil {
			return nil, fmt.Errorf("asset %s per_destination_daily_cap: %w", asset, err)
		}
		policy.RequireReviewAbove, err = parseDecimal(entry.RequireReviewAbove)
		if err != nil {
			return nil, fmt.Errorf("asset %s require_review_above: %w", asset, err)
		}
		policies = append(policies, policy)
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

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func sliceToSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// PolicyEnforcer coordinates access to the configured payout caps.
type PolicyEnforcer struct {
	mu            sync.Mutex
	policies      map[string]compiledPolicy
	totals        map[string]map[string]*big.Int
	accountDaily  map[string]map[string]*big.Int
	accountHourly map[string]map[string]*big.Int
	destDaily     map[string]map[string]*big.Int
	countDaily    map[string]map[string]int
	countHourly   map[string]map[string]int
	inventory     map[string]*big.Int
	confirmations map[string]int
}

// NewPolicyEnforcer constructs an enforcer for the supplied policies.
func NewPolicyEnforcer(policies []Policy) (*PolicyEnforcer, error) {
	if len(policies) == 0 {
		return nil, fmt.Errorf("at least one policy must be configured")
	}
	registry := make(map[string]compiledPolicy, len(policies))
	totals := make(map[string]map[string]*big.Int, len(policies))
	accountDaily := make(map[string]map[string]*big.Int, len(policies))
	accountHourly := make(map[string]map[string]*big.Int, len(policies))
	destDaily := make(map[string]map[string]*big.Int, len(policies))
	countDaily := make(map[string]map[string]int, len(policies))
	countHourly := make(map[string]map[string]int, len(policies))
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
		if policy.PerUserDailyCap == nil {
			policy.PerUserDailyCap = big.NewInt(0)
		}
		if policy.PerUserHourlyCap == nil {
			policy.PerUserHourlyCap = big.NewInt(0)
		}
		if policy.PerDestinationDailyCap == nil {
			policy.PerDestinationDailyCap = big.NewInt(0)
		}
		if policy.RequireReviewAbove == nil {
			policy.RequireReviewAbove = big.NewInt(0)
		}
		if policy.Confirmations <= 0 {
			policy.Confirmations = 3
		}
		registry[asset] = compiledPolicy{
			Policy:                   policy,
			blockedDestinations:      sliceToSet(policy.BlockedDestinations),
			blockedAccounts:          sliceToSet(policy.BlockedAccounts),
			blockedRegions:           sliceToSet(policy.BlockedRegions),
			blockedPartners:          sliceToSet(policy.BlockedPartners),
			requireReviewForRegions:  sliceToSet(policy.RequireReviewForRegions),
			requireReviewForPartners: sliceToSet(policy.RequireReviewForPartners),
		}
		totals[asset] = make(map[string]*big.Int)
		accountDaily[asset] = make(map[string]*big.Int)
		accountHourly[asset] = make(map[string]*big.Int)
		destDaily[asset] = make(map[string]*big.Int)
		countDaily[asset] = make(map[string]int)
		countHourly[asset] = make(map[string]int)
		inventory[asset] = new(big.Int).Set(policy.SoftInventory)
		confirmations[asset] = policy.Confirmations
	}
	return &PolicyEnforcer{
		policies:      registry,
		totals:        totals,
		accountDaily:  accountDaily,
		accountHourly: accountHourly,
		destDaily:     destDaily,
		countDaily:    countDaily,
		countHourly:   countHourly,
		inventory:     inventory,
		confirmations: confirmations,
	}, nil
}

// Validate ensures a payout complies with the configured global caps.
func (p *PolicyEnforcer) Validate(asset string, amount *big.Int, now time.Time) error {
	return p.ValidateRequest(PolicyCheck{Asset: asset, Amount: amount}, now)
}

// ValidateRequest ensures a payout complies with the configured caps and screening rules.
func (p *PolicyEnforcer) ValidateRequest(check PolicyCheck, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateLocked(normalizePolicyCheck(check), now)
}

func (p *PolicyEnforcer) validateLocked(check PolicyCheck, now time.Time) error {
	policy, ok := p.policies[check.Asset]
	if !ok {
		return ErrPolicyNotFound
	}
	if check.Amount == nil || check.Amount.Sign() <= 0 {
		return fmt.Errorf("payout amount must be positive")
	}
	if _, blocked := policy.blockedDestinations[strings.ToLower(check.Destination)]; blocked {
		return ErrDestinationBlocked
	}
	if _, blocked := policy.blockedAccounts[strings.ToLower(check.Account)]; blocked {
		return ErrAccountBlocked
	}
	if _, blocked := policy.blockedRegions[strings.ToLower(check.Region)]; blocked {
		return ErrRegionBlocked
	}
	if _, blocked := policy.blockedPartners[strings.ToLower(check.PartnerID)]; blocked {
		return ErrPartnerBlocked
	}
	if len(policy.AllowedDestinationPrefix) > 0 && check.Destination != "" {
		allowed := false
		lowerDest := strings.ToLower(check.Destination)
		for _, prefix := range policy.AllowedDestinationPrefix {
			if strings.HasPrefix(lowerDest, strings.ToLower(strings.TrimSpace(prefix))) {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrDestinationNotAllowed
		}
	}
	if p.requiresManualReviewLocked(policy, check) && check.ApprovedBy == "" {
		return ErrManualReviewRequired
	}
	inventory := p.inventory[policy.Asset]
	if inventory != nil && inventory.Cmp(check.Amount) < 0 {
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
	if remaining.Cmp(check.Amount) < 0 {
		return ErrDailyCapExceeded
	}
	if check.Account != "" {
		if exceedsAmountLimit(p.accountDaily[policy.Asset][policyBucket(check.Account, dayKey)], policy.PerUserDailyCap, check.Amount) {
			return ErrUserDailyCapExceeded
		}
		if exceedsAmountLimit(p.accountHourly[policy.Asset][policyBucket(check.Account, hourBucket(now))], policy.PerUserHourlyCap, check.Amount) {
			return ErrUserHourlyCapExceeded
		}
		if policy.MaxPayoutsPerHour > 0 && p.countHourly[policy.Asset][policyBucket(check.Account, hourBucket(now))] >= policy.MaxPayoutsPerHour {
			return ErrVelocityExceeded
		}
		if policy.MaxPayoutsPerDay > 0 && p.countDaily[policy.Asset][policyBucket(check.Account, dayKey)] >= policy.MaxPayoutsPerDay {
			return ErrVelocityExceeded
		}
	}
	if check.Destination != "" && exceedsAmountLimit(p.destDaily[policy.Asset][policyBucket(check.Destination, dayKey)], policy.PerDestinationDailyCap, check.Amount) {
		return ErrDestinationDailyCapExceeded
	}
	return nil
}

func (p *PolicyEnforcer) requiresManualReviewLocked(policy compiledPolicy, check PolicyCheck) bool {
	if policy.RequireReviewAbove != nil && policy.RequireReviewAbove.Sign() > 0 && check.Amount.Cmp(policy.RequireReviewAbove) >= 0 {
		return true
	}
	if _, ok := policy.requireReviewForRegions[strings.ToLower(check.Region)]; ok && check.Region != "" {
		return true
	}
	if _, ok := policy.requireReviewForPartners[strings.ToLower(check.PartnerID)]; ok && check.PartnerID != "" {
		return true
	}
	return false
}

func exceedsAmountLimit(current, limit, amount *big.Int) bool {
	if limit == nil || limit.Sign() <= 0 {
		return false
	}
	existing := big.NewInt(0)
	if current != nil {
		existing = new(big.Int).Set(current)
	}
	existing.Add(existing, amount)
	return existing.Cmp(limit) > 0
}

func normalizePolicyCheck(check PolicyCheck) PolicyCheck {
	check.Asset = strings.ToUpper(strings.TrimSpace(check.Asset))
	check.Account = strings.TrimSpace(check.Account)
	check.Destination = strings.TrimSpace(check.Destination)
	check.PartnerID = strings.TrimSpace(check.PartnerID)
	check.Region = strings.TrimSpace(check.Region)
	check.RequestedBy = strings.TrimSpace(check.RequestedBy)
	check.ApprovedBy = strings.TrimSpace(check.ApprovedBy)
	check.ApprovalReference = strings.TrimSpace(check.ApprovalReference)
	return check
}

func policyBucket(value, window string) string {
	return strings.ToLower(strings.TrimSpace(value)) + "|" + window
}

// Record notes a successful payout against the configured caps.
func (p *PolicyEnforcer) Record(asset string, amount *big.Int, now time.Time) {
	p.RecordRequest(PolicyCheck{Asset: asset, Amount: amount}, now)
}

// RecordRequest notes a successful payout against the configured caps and metadata counters.
func (p *PolicyEnforcer) RecordRequest(check PolicyCheck, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	check = normalizePolicyCheck(check)
	policy, ok := p.policies[check.Asset]
	if !ok || check.Amount == nil {
		return
	}
	dayKey := dayBucket(now)
	if _, ok := p.totals[policy.Asset][dayKey]; !ok {
		p.totals[policy.Asset][dayKey] = big.NewInt(0)
	}
	p.totals[policy.Asset][dayKey].Add(p.totals[policy.Asset][dayKey], check.Amount)
	if inv := p.inventory[policy.Asset]; inv != nil {
		inv.Sub(inv, check.Amount)
		if inv.Sign() < 0 {
			inv.SetInt64(0)
		}
	}
	if check.Account != "" {
		addToAmountMap(p.accountDaily[policy.Asset], policyBucket(check.Account, dayKey), check.Amount)
		addToAmountMap(p.accountHourly[policy.Asset], policyBucket(check.Account, hourBucket(now)), check.Amount)
		p.countDaily[policy.Asset][policyBucket(check.Account, dayKey)]++
		p.countHourly[policy.Asset][policyBucket(check.Account, hourBucket(now))]++
	}
	if check.Destination != "" {
		addToAmountMap(p.destDaily[policy.Asset], policyBucket(check.Destination, dayKey), check.Amount)
	}
}

func addToAmountMap(target map[string]*big.Int, key string, amount *big.Int) {
	if target == nil || amount == nil {
		return
	}
	if _, ok := target[key]; !ok {
		target[key] = big.NewInt(0)
	}
	target[key].Add(target[key], amount)
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

// Assets returns the configured policy assets in sorted order.
func (p *PolicyEnforcer) Assets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	assets := make([]string, 0, len(p.policies))
	for asset := range p.policies {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	return assets
}

// InventorySnapshot returns the tracked soft inventory per asset.
func (p *PolicyEnforcer) InventorySnapshot() map[string]*big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*big.Int, len(p.inventory))
	for asset, balance := range p.inventory {
		if balance == nil {
			out[asset] = big.NewInt(0)
			continue
		}
		out[asset] = new(big.Int).Set(balance)
	}
	return out
}

func dayBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func hourBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02T15")
}
