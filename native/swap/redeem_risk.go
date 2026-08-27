package swap

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Conservative defaults for the redeem-side (swap-out: burn NHB on-chain,
// pay USDT off-chain via NOWPayments) circuit breaker, applied by
// core/swap_risk_params.go's effectiveRedeemRiskParameters whenever no
// policy.swapRiskParams governance proposal has ever executed. Swap-out is a
// brand-new, unproven money-moving pathway -- unlike the mint-side
// RiskConfig's historical local-config behaviour (where an absent cap meant
// "unbounded"), absence of a governed redeem-risk value must mean "a safe
// conservative default", never "no limit". Governance can raise (or lower)
// these once the pathway has a production track record; the exact figures
// are not load-bearing, only that some bound always applies.
//
// These constants (and RedeemRiskParameters below) are a deliberate clone
// of, not a reuse of, risk.go's mint-side equivalents: RiskEngine/
// RedeemRiskEngine's key builders use distinct storage prefixes
// (swap/redeemrisk/... vs swap/risk/...) so mint and redeem activity for the
// same address never share a counter -- see the key-prefix doc comment
// below.
const (
	// DefaultRedeemPerTxMaxWei caps a single redemption at 1,000 NHB.
	DefaultRedeemPerTxMaxWei = "1000000000000000000000"
	// DefaultRedeemPerAddressDailyCapWei caps a single address's redemptions
	// at 2,000 NHB per rolling calendar day (UTC).
	DefaultRedeemPerAddressDailyCapWei = "2000000000000000000000"
	// DefaultRedeemPerAddressMonthlyCapWei caps a single address's
	// redemptions at 20,000 NHB per rolling calendar month (UTC).
	DefaultRedeemPerAddressMonthlyCapWei = "20000000000000000000000"
	// DefaultRedeemPerTxMinWei is "no floor" -- a floor is a convenience,
	// not a safety control, so unlike the three caps above this default
	// deliberately does not enforce any minimum.
	DefaultRedeemPerTxMinWei = "0"
)

// RedeemRiskParameters is the runtime-ready, big.Int form of the redeem-side
// circuit breaker's limits, resolved by
// core/swap_risk_params.go's effectiveRedeemRiskParameters from the
// governance param store (if ever set) falling back to the conservative
// defaults above.
type RedeemRiskParameters struct {
	PerAddressDailyCapWei   *big.Int
	PerAddressMonthlyCapWei *big.Int
	PerTxMinWei             *big.Int
	PerTxMaxWei             *big.Int
}

// RedeemRiskCode enumerates redeem-side (swap-out) limit violation
// categories, distinct from RiskCode's mint-side codes.
type RedeemRiskCode string

const (
	// RedeemRiskCodePerTxMin indicates the redeem amount was below the
	// configured floor.
	RedeemRiskCodePerTxMin RedeemRiskCode = "redeem_per_tx_min"
	// RedeemRiskCodePerTxMax indicates the redeem amount exceeded the
	// configured (or default) per-transaction ceiling.
	RedeemRiskCodePerTxMax RedeemRiskCode = "redeem_per_tx_max"
	// RedeemRiskCodeDailyCap indicates the address exhausted its daily
	// redeem allowance.
	RedeemRiskCodeDailyCap RedeemRiskCode = "redeem_daily_cap"
	// RedeemRiskCodeMonthlyCap indicates the address exhausted its monthly
	// redeem allowance.
	RedeemRiskCodeMonthlyCap RedeemRiskCode = "redeem_monthly_cap"
)

// RedeemRiskViolation conveys a violated redeem guardrail alongside
// diagnostic context, mirroring RiskViolation's shape.
type RedeemRiskViolation struct {
	Code    RedeemRiskCode
	Message string
	Limit   *big.Int
	Current *big.Int
}

// Error satisfies the error interface so RedeemRiskViolation can propagate
// through call sites like RiskViolation does.
func (rv *RedeemRiskViolation) Error() string {
	if rv == nil {
		return ""
	}
	if strings.TrimSpace(rv.Message) != "" {
		return rv.Message
	}
	return fmt.Sprintf("redeem risk violation: %s", rv.Code)
}

// RedeemRiskEngine manages per-address redeem (burn) counters within
// storage, entirely separate from RiskEngine's mint-side counters -- see the
// distinct key prefixes below.
type RedeemRiskEngine struct {
	store Storage
	clock func() time.Time
}

// NewRedeemRiskEngine constructs a redeem risk engine backed by the provided
// storage adapter.
func NewRedeemRiskEngine(store Storage) *RedeemRiskEngine {
	return &RedeemRiskEngine{store: store, clock: time.Now}
}

// SetClock overrides the time source, enabling deterministic unit tests.
func (re *RedeemRiskEngine) SetClock(clock func() time.Time) {
	if re == nil || clock == nil {
		return
	}
	re.clock = clock
}

// CheckLimits evaluates the configured limits against the pending redeem
// (burn) and returns a violation when enforcement should block it. Callers
// must invoke this before the burn is applied -- unlike RecordRedeem, this
// performs no writes.
func (re *RedeemRiskEngine) CheckLimits(addr [20]byte, amount *big.Int, params RedeemRiskParameters) (*RedeemRiskViolation, error) {
	if re == nil {
		return nil, fmt.Errorf("redeem risk engine not initialised")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("redeemRisk: amount must be positive")
	}
	now := re.clock().UTC()
	if params.PerTxMinWei != nil && params.PerTxMinWei.Sign() > 0 {
		if amount.Cmp(params.PerTxMinWei) < 0 {
			return &RedeemRiskViolation{
				Code:    RedeemRiskCodePerTxMin,
				Message: fmt.Sprintf("amount %s below minimum %s", amount, params.PerTxMinWei),
				Limit:   new(big.Int).Set(params.PerTxMinWei),
				Current: new(big.Int).Set(amount),
			}, nil
		}
	}
	if params.PerTxMaxWei != nil && params.PerTxMaxWei.Sign() > 0 {
		if amount.Cmp(params.PerTxMaxWei) > 0 {
			return &RedeemRiskViolation{
				Code:    RedeemRiskCodePerTxMax,
				Message: fmt.Sprintf("amount %s exceeds maximum %s", amount, params.PerTxMaxWei),
				Limit:   new(big.Int).Set(params.PerTxMaxWei),
				Current: new(big.Int).Set(amount),
			}, nil
		}
	}
	dayTotal, err := re.bucketTotal(redeemRiskDailyKey(now, addr))
	if err != nil {
		return nil, err
	}
	if params.PerAddressDailyCapWei != nil && params.PerAddressDailyCapWei.Sign() > 0 {
		projected := new(big.Int).Add(dayTotal, amount)
		if projected.Cmp(params.PerAddressDailyCapWei) > 0 {
			return &RedeemRiskViolation{
				Code:    RedeemRiskCodeDailyCap,
				Message: fmt.Sprintf("daily redeem cap %s exceeded", params.PerAddressDailyCapWei),
				Limit:   new(big.Int).Set(params.PerAddressDailyCapWei),
				Current: projected,
			}, nil
		}
	}
	monthTotal, err := re.bucketTotal(redeemRiskMonthlyKey(now, addr))
	if err != nil {
		return nil, err
	}
	if params.PerAddressMonthlyCapWei != nil && params.PerAddressMonthlyCapWei.Sign() > 0 {
		projected := new(big.Int).Add(monthTotal, amount)
		if projected.Cmp(params.PerAddressMonthlyCapWei) > 0 {
			return &RedeemRiskViolation{
				Code:    RedeemRiskCodeMonthlyCap,
				Message: fmt.Sprintf("monthly redeem cap %s exceeded", params.PerAddressMonthlyCapWei),
				Limit:   new(big.Int).Set(params.PerAddressMonthlyCapWei),
				Current: projected,
			}, nil
		}
	}
	return nil, nil
}

// RecordRedeem persists the redeem (burn) against the relevant counters
// following a successful burn. Callers must invoke this only after the burn
// itself has been applied, mirroring RiskEngine.RecordMint's placement
// relative to the mint it accounts for.
func (re *RedeemRiskEngine) RecordRedeem(addr [20]byte, amount *big.Int) error {
	if re == nil {
		return fmt.Errorf("redeem risk engine not initialised")
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("redeemRisk: amount must be positive")
	}
	now := re.clock().UTC()
	if err := re.pruneBuckets(addr, now); err != nil {
		return err
	}
	if err := re.addToBucket(redeemRiskDailyKey(now, addr), amount); err != nil {
		return err
	}
	return re.addToBucket(redeemRiskMonthlyKey(now, addr), amount)
}

func (re *RedeemRiskEngine) bucketTotal(key []byte) (*big.Int, error) {
	var record amountRecord
	ok, err := re.store.KVGet(key, &record)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(record.Amount) == "" {
		return big.NewInt(0), nil
	}
	return parseWeiAmount(record.Amount)
}

func (re *RedeemRiskEngine) addToBucket(key []byte, amount *big.Int) error {
	current, err := re.bucketTotal(key)
	if err != nil {
		return err
	}
	updated := new(big.Int).Add(current, amount)
	return re.store.KVPut(key, amountRecord{Amount: updated.String()})
}

// pruneBuckets clears the previous day's/month's counters once the clock has
// rolled over, mirroring RiskEngine.pruneBuckets -- otherwise stale buckets
// would linger in storage forever under their own now-unreachable keys.
func (re *RedeemRiskEngine) pruneBuckets(addr [20]byte, now time.Time) error {
	key := redeemRiskIndexKey(addr)
	var index riskIndexRecord
	_, err := re.store.KVGet(key, &index)
	if err != nil {
		return err
	}
	currentDay := formatDay(now)
	currentMonth := formatMonth(now)
	if strings.TrimSpace(index.CurrentDay) != "" && index.CurrentDay != currentDay {
		if err := re.store.KVDelete(redeemRiskDailyKeyForDay(index.CurrentDay, addr)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(index.CurrentMonth) != "" && index.CurrentMonth != currentMonth {
		if err := re.store.KVDelete(redeemRiskMonthlyKeyForMonth(index.CurrentMonth, addr)); err != nil {
			return err
		}
	}
	index.CurrentDay = currentDay
	index.CurrentMonth = currentMonth
	return re.store.KVPut(key, index)
}

// Key prefixes deliberately distinct from risk.go's riskDailyPrefix /
// riskMonthlyPrefix / riskIndexPrefix ("swap/risk/...") -- this is what
// keeps mint-side and redeem-side risk tracking from ever colliding in the
// same storage bucket for a given address.
var (
	redeemRiskDailyPrefix   = []byte("swap/redeemrisk/daily/")
	redeemRiskMonthlyPrefix = []byte("swap/redeemrisk/monthly/")
	redeemRiskIndexPrefix   = []byte("swap/redeemrisk/index/")
)

func redeemRiskDailyKey(now time.Time, addr [20]byte) []byte {
	return redeemRiskDailyKeyForDay(formatDay(now), addr)
}

func redeemRiskDailyKeyForDay(day string, addr [20]byte) []byte {
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(redeemRiskDailyPrefix)+len(day)+1+len(suffix))
	copy(key, redeemRiskDailyPrefix)
	copy(key[len(redeemRiskDailyPrefix):], day)
	key[len(redeemRiskDailyPrefix)+len(day)] = '/'
	copy(key[len(redeemRiskDailyPrefix)+len(day)+1:], suffix)
	return key
}

func redeemRiskMonthlyKey(now time.Time, addr [20]byte) []byte {
	return redeemRiskMonthlyKeyForMonth(formatMonth(now), addr)
}

func redeemRiskMonthlyKeyForMonth(month string, addr [20]byte) []byte {
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(redeemRiskMonthlyPrefix)+len(month)+1+len(suffix))
	copy(key, redeemRiskMonthlyPrefix)
	copy(key[len(redeemRiskMonthlyPrefix):], month)
	key[len(redeemRiskMonthlyPrefix)+len(month)] = '/'
	copy(key[len(redeemRiskMonthlyPrefix)+len(month)+1:], suffix)
	return key
}

func redeemRiskIndexKey(addr [20]byte) []byte {
	suffix := hex.EncodeToString(addr[:])
	key := make([]byte, len(redeemRiskIndexPrefix)+len(suffix))
	copy(key, redeemRiskIndexPrefix)
	copy(key[len(redeemRiskIndexPrefix):], suffix)
	return key
}
