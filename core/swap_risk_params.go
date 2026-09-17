package core

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
	"nhbchain/native/swap"
)

// effectiveRedeemRiskParameters resolves the redeem-side (swap-out burn,
// TxTypeRedeemNHB) circuit-breaker's complete RedeemRiskParameters, entirely
// from the governance param store falling back to
// native/swap/redeem_risk.go's Default*Wei constants. There is no local
// config.toml-based RiskConfig to overlay onto here -- redeem's four caps
// were governance-only from the start. Read fresh from state on every call
// so a passed policy.swapRiskParams proposal takes effect on the very next
// transaction, network-wide, with no node restart. Mirrors
// core/buyback_settlement.go's effectiveBuybackConfig precedent.
func (sp *StateProcessor) effectiveRedeemRiskParameters(manager *nhbstate.Manager) (swap.RedeemRiskParameters, error) {
	minWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeySwapRiskRedeemPerTxMinWei, swap.DefaultRedeemPerTxMinWei)
	if err != nil {
		return swap.RedeemRiskParameters{}, err
	}
	maxWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeySwapRiskRedeemPerTxMaxWei, swap.DefaultRedeemPerTxMaxWei)
	if err != nil {
		return swap.RedeemRiskParameters{}, err
	}
	dailyWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeySwapRiskRedeemPerAddressDailyCapWei, swap.DefaultRedeemPerAddressDailyCapWei)
	if err != nil {
		return swap.RedeemRiskParameters{}, err
	}
	monthlyWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeySwapRiskRedeemPerAddressMonthlyCapWei, swap.DefaultRedeemPerAddressMonthlyCapWei)
	if err != nil {
		return swap.RedeemRiskParameters{}, err
	}
	return swap.RedeemRiskParameters{
		PerTxMinWei:             minWei,
		PerTxMaxWei:             maxWei,
		PerAddressDailyCapWei:   dailyWei,
		PerAddressMonthlyCapWei: monthlyWei,
	}, nil
}

// effectiveRedemptionFeeParameters resolves the redeem-side redemption fee
// policy (rate plus floor/cap), entirely from the governance param store
// falling back to native/swap/redemption_fee.go's Default* constants.
// Mirrors effectiveRedeemRiskParameters exactly -- read fresh from state on
// every call so a passed policy.redemptionFeeParams proposal takes effect on
// the very next transaction, network-wide, with no node restart.
func (sp *StateProcessor) effectiveRedemptionFeeParameters(manager *nhbstate.Manager) (swap.RedemptionFeeParameters, error) {
	bps, err := readGovernedRedemptionFeeBps(manager, governance.ParamKeyRedemptionFeeBps, swap.DefaultRedemptionFeeBps)
	if err != nil {
		return swap.RedemptionFeeParameters{}, err
	}
	floorWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeyRedemptionFeeFloorWei, swap.DefaultRedemptionFeeFloorWei)
	if err != nil {
		return swap.RedemptionFeeParameters{}, err
	}
	capWei, err := readGovernedSwapRiskWei(manager, governance.ParamKeyRedemptionFeeCapWei, swap.DefaultRedemptionFeeCapWei)
	if err != nil {
		return swap.RedemptionFeeParameters{}, err
	}
	return swap.RedemptionFeeParameters{FeeBps: bps, FeeFloorWei: floorWei, FeeCapWei: capWei}, nil
}

// readGovernedRedemptionFeeBps reads the governed redemption fee rate back
// from the param store (governance.Engine.applyRedemptionFeeParams's write
// side), falling back to defaultBps when the key has never been set --
// mirrors readGovernedSwapRiskWei's error-propagating style (fail loudly on
// corrupt stored data) rather than core/buyback_settlement.go's
// readGovernedBps, which silently treats a parse error as "not set".
func readGovernedRedemptionFeeBps(manager *nhbstate.Manager, key string, defaultBps uint32) (uint32, error) {
	raw, ok, err := manager.ParamStoreGet(key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return defaultBps, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return defaultBps, nil
	}
	parsed, err := strconv.ParseUint(trimmed, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("redemption fee: invalid stored value for %s: %q", key, trimmed)
	}
	if parsed > 10_000 {
		return 0, fmt.Errorf("redemption fee: stored bps for %s exceeds 10000: %d", key, parsed)
	}
	return uint32(parsed), nil
}

// readGovernedSwapRiskWei reads a single governed wei amount back from the
// param store (governance.Engine.applySwapRiskParams's write side), falling
// back to defaultWei when the key has never been set -- mirrors
// core/buyback_settlement.go's readGovernedBps, but for wei-denominated
// big.Int amounts (via big.Int.String(), matching applySwapRiskParams's own
// encoding) rather than basis points.
func readGovernedSwapRiskWei(manager *nhbstate.Manager, key, defaultWei string) (*big.Int, error) {
	text := defaultWei
	raw, ok, err := manager.ParamStoreGet(key)
	if err != nil {
		return nil, err
	}
	if ok {
		if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
			text = trimmed
		}
	}
	amount, valid := new(big.Int).SetString(text, 10)
	if !valid {
		return nil, fmt.Errorf("swap risk: invalid stored value for %s: %q", key, text)
	}
	if amount.Sign() < 0 {
		return nil, fmt.Errorf("swap risk: negative stored value for %s", key)
	}
	return amount, nil
}
