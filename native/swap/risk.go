package swap

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RiskConfig captures operator-defined mint guardrails parsed from configuration.
type RiskConfig struct {
	PerAddressDailyCapWei   string `toml:"PerAddressDailyCapWei"`
	PerAddressMonthlyCapWei string `toml:"PerAddressMonthlyCapWei"`
	PerTxMinWei             string `toml:"PerTxMinWei"`
	PerTxMaxWei             string `toml:"PerTxMaxWei"`
	VelocityWindowSeconds   int64  `toml:"VelocityWindowSeconds"`
	VelocityMaxMints        uint64 `toml:"VelocityMaxMints"`
	SanctionsCheckEnabled   bool   `toml:"SanctionsCheckEnabled"`
}

// RiskParameters represents canonical, runtime-ready interpretations of the risk settings.
type RiskParameters struct {
	PerAddressDailyCapWei   *big.Int
	PerAddressMonthlyCapWei *big.Int
	PerTxMinWei             *big.Int
	PerTxMaxWei             *big.Int
	VelocityWindowSeconds   uint64
	VelocityMaxMints        uint64
	SanctionsCheckEnabled   bool
}

// Normalise trims whitespace and applies canonical defaults to defensive copies.
func (rc RiskConfig) Normalise() RiskConfig {
	cfg := RiskConfig{
		PerAddressDailyCapWei:   strings.TrimSpace(rc.PerAddressDailyCapWei),
		PerAddressMonthlyCapWei: strings.TrimSpace(rc.PerAddressMonthlyCapWei),
		PerTxMinWei:             strings.TrimSpace(rc.PerTxMinWei),
		PerTxMaxWei:             strings.TrimSpace(rc.PerTxMaxWei),
		VelocityWindowSeconds:   rc.VelocityWindowSeconds,
		VelocityMaxMints:        rc.VelocityMaxMints,
		SanctionsCheckEnabled:   rc.SanctionsCheckEnabled,
	}
	if cfg.VelocityWindowSeconds < 0 {
		cfg.VelocityWindowSeconds = 0
	}
	return cfg
}

// Parameters converts the textual configuration into runtime big integers and bounds.
func (rc RiskConfig) Parameters() (RiskParameters, error) {
	normalized := rc.Normalise()
	params := RiskParameters{SanctionsCheckEnabled: normalized.SanctionsCheckEnabled}
	if normalized.PerAddressDailyCapWei != "" {
		amount, err := parseWeiAmount(normalized.PerAddressDailyCapWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid PerAddressDailyCapWei: %w", err)
		}
		params.PerAddressDailyCapWei = amount
	}
	if normalized.PerAddressMonthlyCapWei != "" {
		amount, err := parseWeiAmount(normalized.PerAddressMonthlyCapWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid PerAddressMonthlyCapWei: %w", err)
		}
		params.PerAddressMonthlyCapWei = amount
	}
	if normalized.PerTxMinWei != "" {
		amount, err := parseWeiAmount(normalized.PerTxMinWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid PerTxMinWei: %w", err)
		}
		params.PerTxMinWei = amount
	}
	if normalized.PerTxMaxWei != "" {
		amount, err := parseWeiAmount(normalized.PerTxMaxWei)
		if err != nil {
			return params, fmt.Errorf("risk: invalid PerTxMaxWei: %w", err)
		}
		params.PerTxMaxWei = amount
	}
	if normalized.VelocityWindowSeconds > 0 {
		params.VelocityWindowSeconds = uint64(normalized.VelocityWindowSeconds)
	}
	if normalized.VelocityMaxMints > 0 {
		params.VelocityMaxMints = normalized.VelocityMaxMints
	}
	return params, nil
}

// ProviderConfig describes which fiat providers are authorised to submit mints.
type ProviderConfig struct {
	Allow []string `toml:"Allow"`
}

// Normalise returns a deduplicated, lower-cased allow list to simplify downstream checks.
func (pc ProviderConfig) Normalise() ProviderConfig {
	trimmed := make([]string, 0, len(pc.Allow))
	seen := make(map[string]struct{}, len(pc.Allow))
	for _, entry := range pc.Allow {
		normalised := strings.ToLower(strings.TrimSpace(entry))
		if normalised == "" {
			continue
		}
		if _, exists := seen[normalised]; exists {
			continue
		}
		seen[normalised] = struct{}{}
		trimmed = append(trimmed, normalised)
	}
	sort.Strings(trimmed)
	return ProviderConfig{Allow: trimmed}
}

// AllowList returns a defensive copy of the configured provider identifiers.
func (pc ProviderConfig) AllowList() []string {
	return append([]string{}, pc.Allow...)
}

// IsAllowed reports whether the provided provider identifier is present in the allow list.
func (pc ProviderConfig) IsAllowed(provider string) bool {
	needle := strings.ToLower(strings.TrimSpace(provider))
	if needle == "" {
		return false
	}
	for _, entry := range pc.Allow {
		if entry == needle {
			return true
		}
	}
	return false
}

// ProviderStatus summarises the provider controls for operator dashboards and tooling.
type ProviderStatus struct {
	Allow                 []string
	LastOracleHealthCheck int64
}

// RiskCode enumerates supported limit violation categories.
type RiskCode string

const (
	// RiskCodePerTxMin indicates the mint amount was below the allowed floor.
	RiskCodePerTxMin RiskCode = "per_tx_min"
	// RiskCodePerTxMax indicates the mint amount exceeded the configured ceiling.
	RiskCodePerTxMax RiskCode = "per_tx_max"
	// RiskCodeDailyCap indicates the recipient exhausted the daily allowance.
	RiskCodeDailyCap RiskCode = "daily_cap"
	// RiskCodeMonthlyCap indicates the recipient exhausted the monthly allowance.
	RiskCodeMonthlyCap RiskCode = "monthly_cap"
	// RiskCodeVelocity indicates the mint frequency exceeded the allowed burst.
	RiskCodeVelocity RiskCode = "velocity"
)

// RiskViolation conveys a violated guardrail alongside diagnostic context for alerts.
type RiskViolation struct {
	Code          RiskCode
	Message       string
	Limit         *big.Int
	Current       *big.Int
	WindowSeconds uint64
	Count         int
}

// Error satisfies the error interface to allow RiskViolation to propagate through call sites.
func (rv *RiskViolation) Error() string {
	if rv == nil {
		return ""
	}
	if strings.TrimSpace(rv.Message) != "" {
		return rv.Message
	}
	return fmt.Sprintf("risk violation: %s", rv.Code)
}

// RiskEngine manages per-address mint counters and velocity samples within storage.
type RiskEngine struct {
	store Storage
	clock func() time.Time
}

// NewRiskEngine constructs a risk engine backed by the provided storage adapter.
func NewRiskEngine(store Storage) *RiskEngine {
	return &RiskEngine{store: store, clock: time.Now}
}

// SetClock overrides the time source, enabling deterministic unit tests.
func (re *RiskEngine) SetClock(clock func() time.Time) {
	if re == nil || clock == nil {
		return
	}
	re.clock = clock
}

// CheckLimits evaluates the configured limits against the pending mint and returns a violation when enforcement should block the mint.
func (re *RiskEngine) CheckLimits(addr [20]byte, amount *big.Int, params RiskParameters) (*RiskViolation, error) {
	if re == nil {
		return nil, fmt.Errorf("risk engine not initialised")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("risk: amount must be positive")
	}
	now := re.clock().UTC()
	if params.PerTxMinWei != nil && params.PerTxMinWei.Sign() > 0 {
		if amount.Cmp(params.PerTxMinWei) < 0 {
			return &RiskViolation{
				Code:    RiskCodePerTxMin,
				Message: fmt.Sprintf("amount %s below minimum %s", amount, params.PerTxMinWei),
				Limit:   new(big.Int).Set(params.PerTxMinWei),
				Current: new(big.Int).Set(amount),
			}, nil
		}
	}
	if params.PerTxMaxWei != nil && params.PerTxMaxWei.Sign() > 0 {
		if amount.Cmp(params.PerTxMaxWei) > 0 {
			return &RiskViolation{
				Code:    RiskCodePerTxMax,
				Message: fmt.Sprintf("amount %s exceeds maximum %s", amount, params.PerTxMaxWei),
				Limit:   new(big.Int).Set(params.PerTxMaxWei),
				Current: new(big.Int).Set(amount),
			}, nil
		}
	}
	dayKey := riskDailyKey(now, addr)
	dayTotal, err := re.bucketTotal(dayKey)
	if err != nil {
		return nil, err
	}
	if params.PerAddressDailyCapWei != nil && params.PerAddressDailyCapWei.Sign() > 0 {
		projected := new(big.Int).Add(dayTotal, amount)
		if projected.Cmp(params.PerAddressDailyCapWei) > 0 {
			return &RiskViolation{
				Code:    RiskCodeDailyCap,
				Message: fmt.Sprintf("daily cap %s exceeded", params.PerAddressDailyCapWei),
				Limit:   new(big.Int).Set(params.PerAddressDailyCapWei),
				Current: projected,
			}, nil
		}
	}
	monthKey := riskMonthlyKey(now, addr)
	monthTotal, err := re.bucketTotal(monthKey)
	if err != nil {
		return nil, err
	}
	if params.PerAddressMonthlyCapWei != nil && params.PerAddressMonthlyCapWei.Sign() > 0 {
		projected := new(big.Int).Add(monthTotal, amount)
		if projected.Cmp(params.PerAddressMonthlyCapWei) > 0 {
			return &RiskViolation{
				Code:    RiskCodeMonthlyCap,
				Message: fmt.Sprintf("monthly cap %s exceeded", params.PerAddressMonthlyCapWei),
				Limit:   new(big.Int).Set(params.PerAddressMonthlyCapWei),
				Current: projected,
			}, nil
		}
	}
	if params.VelocityWindowSeconds > 0 && params.VelocityMaxMints > 0 {
		samples, err := re.loadVelocity(addr)
		if err != nil {
			return nil, err
		}
		cutoff := now.Add(-time.Duration(params.VelocityWindowSeconds) * time.Second)
		recent := filterSamples(samples, cutoff)
		if len(recent) >= int(params.VelocityMaxMints) {
			return &RiskViolation{
				Code:          RiskCodeVelocity,
				Message:       fmt.Sprintf("velocity exceeded %d events in %d seconds", params.VelocityMaxMints, params.VelocityWindowSeconds),
				WindowSeconds: params.VelocityWindowSeconds,
				Count:         len(recent),
			}, nil
		}
	}
	return nil, nil
}

// RecordMint persists the mint against the relevant counters following a successful mint.
func (re *RiskEngine) RecordMint(addr [20]byte, amount *big.Int) error {
	if re == nil {
		return fmt.Errorf("risk engine not initialised")
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("risk: amount must be positive")
	}
	now := re.clock().UTC()
	dayKey := riskDailyKey(now, addr)
	if err := re.addToBucket(dayKey, amount); err != nil {
		return err
	}
	monthKey := riskMonthlyKey(now, addr)
	if err := re.addToBucket(monthKey, amount); err != nil {
		return err
	}
	if err := re.appendVelocity(addr, now); err != nil {
		return err
	}
	return nil
}

// RiskUsage captures the aggregated counters for an address.
type RiskUsage struct {
	Address            [20]byte
	Day                string
	DayTotalWei        *big.Int
	Month              string
	MonthTotalWei      *big.Int
	VelocityTimestamps []int64
}

// Copy returns a deep copy to shield callers from accidental mutation.
func (ru *RiskUsage) Copy() *RiskUsage {
	if ru == nil {
		return nil
	}
	clone := &RiskUsage{
		Address:            ru.Address,
		Day:                ru.Day,
		Month:              ru.Month,
		VelocityTimestamps: append([]int64{}, ru.VelocityTimestamps...),
	}
	if ru.DayTotalWei != nil {
		clone.DayTotalWei = new(big.Int).Set(ru.DayTotalWei)
	}
	if ru.MonthTotalWei != nil {
		clone.MonthTotalWei = new(big.Int).Set(ru.MonthTotalWei)
	}
	return clone
}

// Usage retrieves the current counters for the provided address.
func (re *RiskEngine) Usage(addr [20]byte) (*RiskUsage, error) {
	if re == nil {
		return nil, fmt.Errorf("risk engine not initialised")
	}
	now := re.clock().UTC()
	day := formatDay(now)
	month := formatMonth(now)
	dayTotal, err := re.bucketTotal(riskDailyKey(now, addr))
	if err != nil {
		return nil, err
	}
	monthTotal, err := re.bucketTotal(riskMonthlyKey(now, addr))
	if err != nil {
		return nil, err
	}
	samples, err := re.loadVelocity(addr)
	if err != nil {
		return nil, err
	}
	timestamps := make([]int64, len(samples))
	for i := range samples {
		timestamps[i] = int64(samples[i])
	}
	usage := &RiskUsage{
		Address:            addr,
		Day:                day,
		DayTotalWei:        dayTotal,
		Month:              month,
		MonthTotalWei:      monthTotal,
		VelocityTimestamps: timestamps,
	}
	return usage, nil
}

func (re *RiskEngine) bucketTotal(key []byte) (*big.Int, error) {
	var record amountRecord
	ok, err := re.store.KVGet(key, &record)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(record.Amount) == "" {
		return big.NewInt(0), nil
	}
	value, err := parseWeiAmount(record.Amount)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (re *RiskEngine) addToBucket(key []byte, amount *big.Int) error {
	current, err := re.bucketTotal(key)
	if err != nil {
		return err
	}
	updated := new(big.Int).Add(current, amount)
	record := amountRecord{Amount: updated.String()}
	return re.store.KVPut(key, record)
}

func (re *RiskEngine) appendVelocity(addr [20]byte, now time.Time) error {
	samples, err := re.loadVelocity(addr)
	if err != nil {
		return err
	}
	cleaned := filterSamples(samples, now.Add(-24*time.Hour))
	cleaned = append(cleaned, uint64(now.UTC().Unix()))
	record := velocityRecord{Samples: cleaned}
	return re.store.KVPut(riskVelocityKey(addr), record)
}

func (re *RiskEngine) loadVelocity(addr [20]byte) ([]uint64, error) {
	var record velocityRecord
	ok, err := re.store.KVGet(riskVelocityKey(addr), &record)
	if err != nil {
		return nil, err
	}
	if !ok || len(record.Samples) == 0 {
		return []uint64{}, nil
	}
	out := append([]uint64{}, record.Samples...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func parseWeiAmount(value string) (*big.Int, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	normalized := trimmed
	var exponent int64
	if idx := strings.IndexAny(normalized, "eE"); idx != -1 {
		expPart := strings.TrimSpace(normalized[idx+1:])
		if expPart == "" {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		expValue, err := strconv.ParseInt(expPart, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		exponent = expValue
		normalized = strings.TrimSpace(normalized[:idx])
	}
	normalized = strings.TrimPrefix(normalized, "+")
	if strings.HasPrefix(normalized, "-") {
		return nil, fmt.Errorf("amount must not be negative")
	}
	parts := strings.Split(normalized, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("invalid amount format")
	}
	integerPart := parts[0]
	fractionalPart := ""
	if len(parts) == 2 {
		fractionalPart = parts[1]
	}
	digits := integerPart + fractionalPart
	if digits == "" {
		return big.NewInt(0), nil
	}
	if !isDigits(digits) {
		return nil, fmt.Errorf("invalid amount format")
	}
	fracLen := len(fractionalPart)
	for fracLen > 0 && len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		fracLen--
	}
	digits = strings.TrimLeft(digits, "0")
	totalExponent := exponent - int64(fracLen)
	if totalExponent < 0 {
		return nil, fmt.Errorf("amount must be an integer")
	}
	if digits == "" {
		digits = "0"
	}
	if totalExponent > 0 {
		digits += strings.Repeat("0", int(totalExponent))
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(digits, 10); !ok {
		return nil, fmt.Errorf("invalid amount value")
	}
	return amount, nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func riskDailyKey(now time.Time, addr [20]byte) []byte {
	day := formatDay(now)
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(riskDailyPrefix)+len(day)+1+len(suffix))
	copy(key, riskDailyPrefix)
	copy(key[len(riskDailyPrefix):], day)
	key[len(riskDailyPrefix)+len(day)] = '/'
	copy(key[len(riskDailyPrefix)+len(day)+1:], suffix)
	return key
}

func riskMonthlyKey(now time.Time, addr [20]byte) []byte {
	month := formatMonth(now)
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(riskMonthlyPrefix)+len(month)+1+len(suffix))
	copy(key, riskMonthlyPrefix)
	copy(key[len(riskMonthlyPrefix):], month)
	key[len(riskMonthlyPrefix)+len(month)] = '/'
	copy(key[len(riskMonthlyPrefix)+len(month)+1:], suffix)
	return key
}

func riskVelocityKey(addr [20]byte) []byte {
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(riskVelocityPrefix)+len(suffix))
	copy(key, riskVelocityPrefix)
	copy(key[len(riskVelocityPrefix):], suffix)
	return key
}

func formatDay(ts time.Time) string {
	return ts.UTC().Format("2006-01-02")
}

func formatMonth(ts time.Time) string {
	return ts.UTC().Format("2006-01")
}

func filterSamples(samples []uint64, cutoff time.Time) []uint64 {
	if len(samples) == 0 {
		return []uint64{}
	}
	threshold := cutoff.Unix()
	filtered := make([]uint64, 0, len(samples))
	for _, sample := range samples {
		if int64(sample) >= threshold {
			filtered = append(filtered, sample)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i] < filtered[j] })
	return filtered
}

type amountRecord struct {
	Amount string
}

type velocityRecord struct {
	Samples []uint64
}

var (
	riskDailyPrefix    = []byte("swap/risk/daily/")
	riskMonthlyPrefix  = []byte("swap/risk/monthly/")
	riskVelocityPrefix = []byte("swap/risk/velocity/")
)

// SanctionsChecker evaluates whether an address is eligible for mints.
type SanctionsChecker func([20]byte) bool

// DefaultSanctionsChecker allows all addresses, providing a safe default when operators do not wire external services.
func DefaultSanctionsChecker([20]byte) bool { return true }
