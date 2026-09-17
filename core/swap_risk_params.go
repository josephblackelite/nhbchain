package core

import (
	"fmt"
	"math/big"
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
