package swap

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	nativecommon "nhbchain/native/common"
)

var (
	// ErrOracleStale indicates the oracle quote exceeded the freshness window.
	ErrOracleStale = errors.New("swap: oracle quote stale")
	// ErrOracleDeviation indicates the oracle observation deviated beyond tolerance.
	ErrOracleDeviation = errors.New("swap: oracle deviation exceeded")
	// ErrSlippageExceeded indicates the voucher amount fell outside the allowed tolerance.
	ErrSlippageExceeded = errors.New("swap: slippage exceeds tolerance")
	// ErrCashOutDailyCapExceeded indicates the asset-level daily cap would be breached.
	ErrCashOutDailyCapExceeded = errors.New("swap: cash-out asset cap exceeded")
	// ErrCashOutTierCapExceeded indicates the submitter's tier allowance would be exceeded.
	ErrCashOutTierCapExceeded = errors.New("swap: cash-out tier cap exceeded")
	// ErrSwapPaused indicates the swap module has been paused by governance.
	ErrSwapPaused = errors.New("swap: module paused")
)

// ValidateOracleFreshness enforces the oracle freshness window using the supplied guardrails.
func ValidateOracleFreshness(guard OracleGuardrails, now, observed time.Time) (*RiskViolation, error) {
	if guard.MaxAge <= 0 {
		return nil, nil
	}
	if observed.IsZero() {
		violation := &RiskViolation{
			Code:          RiskCodeOracleStale,
			Message:       "oracle timestamp missing",
			WindowSeconds: uint64(guard.MaxAge / time.Second),
		}
		return violation, ErrOracleStale
	}
	if now.Before(observed) {
		return nil, fmt.Errorf("swap: oracle observation in future")
	}
	if now.Sub(observed) > guard.MaxAge {
		violation := &RiskViolation{
			Code:          RiskCodeOracleStale,
			Message:       fmt.Sprintf("oracle sample older than %s", guard.MaxAge),
			WindowSeconds: uint64(guard.MaxAge / time.Second),
		}
		return violation, ErrOracleStale
	}
	return nil, nil
}

// ValidateOracleDeviation enforces that a new oracle observation does not deviate beyond tolerance.
func ValidateOracleDeviation(guard OracleGuardrails, previous, current *big.Rat) (*RiskViolation, error) {
	if guard.MaxDeviationBps == 0 {
		return nil, nil
	}
	if previous == nil || current == nil {
		return nil, fmt.Errorf("swap: oracle deviation requires observations")
	}
	if previous.Sign() <= 0 || current.Sign() <= 0 {
		return nil, fmt.Errorf("swap: oracle observations must be positive")
	}
	diff := new(big.Rat).Sub(current, previous)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	ratio := new(big.Rat).Quo(diff, previous)
	ratio.Mul(ratio, big.NewRat(10000, 1))
	num := new(big.Int).Set(ratio.Num())
	den := new(big.Int).Set(ratio.Denom())
	if den.Sign() == 0 {
		return nil, fmt.Errorf("swap: oracle deviation denominator zero")
	}
	bps := num.Quo(num, den)
	threshold := big.NewInt(int64(guard.MaxDeviationBps))
	if bps.Cmp(threshold) > 0 {
		violation := &RiskViolation{
			Code:          RiskCodeOracleDeviation,
			Message:       fmt.Sprintf("oracle deviation %s bps above %d", bps.String(), guard.MaxDeviationBps),
			Limit:         new(big.Int).Set(threshold),
			Current:       new(big.Int).Set(bps),
			WindowSeconds: uint64(guard.MaxAge / time.Second),
		}
		return violation, ErrOracleDeviation
	}
	return nil, nil
}

// ValidateSlippage enforces the configured slippage tolerance between expected and submitted amounts.
func ValidateSlippage(tolerance SlippageTolerance, expected, submitted *big.Int) (*RiskViolation, error) {
	if !tolerance.Enabled() {
		return nil, nil
	}
	if expected == nil || submitted == nil {
		return nil, fmt.Errorf("swap: slippage requires amounts")
	}
	if expected.Sign() <= 0 {
		return nil, fmt.Errorf("swap: expected amount must be positive")
	}
	diff := new(big.Int).Sub(expected, submitted)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	bps := new(big.Int).Mul(diff, big.NewInt(10000))
	bps.Div(bps, expected)
	threshold := big.NewInt(int64(tolerance.MaxBps))
	if bps.Cmp(threshold) > 0 {
		violation := &RiskViolation{
			Code:    RiskCodeSlippage,
			Message: fmt.Sprintf("slippage %s bps exceeds %d", bps.String(), tolerance.MaxBps),
			Limit:   new(big.Int).Set(threshold),
			Current: bps,
		}
		return violation, ErrSlippageExceeded
	}
	return nil, nil
}

// ValidateDailyCashOut enforces per-asset and per-tier daily caps, including pending escrow.
func (p CashOutParameters) ValidateDailyCashOut(asset StableAsset, tier string, settledToday, pendingEscrow, requested *big.Int) (*RiskViolation, error) {
	if requested == nil || requested.Sign() <= 0 {
		return nil, fmt.Errorf("swap: requested cash-out must be positive")
	}
	total := new(big.Int).Set(requested)
	if settledToday != nil {
		total.Add(total, settledToday)
	}
	if pendingEscrow != nil {
		total.Add(total, pendingEscrow)
	}
	if cap, ok := p.AssetDailyCaps[asset]; ok && cap != nil && cap.Sign() > 0 {
		if total.Cmp(cap) > 0 {
			violation := &RiskViolation{
				Code:    RiskCodeCashOutAssetCap,
				Message: fmt.Sprintf("asset %s daily cap %s exceeded", asset, cap.String()),
				Limit:   new(big.Int).Set(cap),
				Current: total,
			}
			return violation, ErrCashOutDailyCapExceeded
		}
	}
	trimmedTier := strings.TrimSpace(tier)
	if trimmedTier != "" {
		if limits, ok := p.TierCaps[strings.ToLower(trimmedTier)]; ok && limits.DailyCapWei != nil && limits.DailyCapWei.Sign() > 0 {
			if total.Cmp(limits.DailyCapWei) > 0 {
				violation := &RiskViolation{
					Code:    RiskCodeCashOutTierCap,
					Message: fmt.Sprintf("tier %s daily cap %s exceeded", limits.Tier, limits.DailyCapWei.String()),
					Limit:   new(big.Int).Set(limits.DailyCapWei),
					Current: total,
				}
				return violation, ErrCashOutTierCapExceeded
			}
		}
	}
	return nil, nil
}

// ValidateSwapActive reports a violation when the swap module is paused.
func ValidateSwapActive(pauseView nativecommon.PauseView) (*RiskViolation, error) {
	if pauseView == nil {
		return nil, nil
	}
	if pauseView.IsPaused("swap") {
		violation := &RiskViolation{
			Code:    RiskCodeModulePaused,
			Message: "swap module paused by governance",
		}
		return violation, ErrSwapPaused
	}
	return nil, nil
}
