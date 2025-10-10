package config

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/consensus"
	"nhbchain/native/fees"
)

var (
	MinVotingPeriodSeconds = uint64(3600)
)

func ValidateConfig(g Global) error {
	if g.Governance.QuorumBPS < g.Governance.PassThresholdBPS {
		return fmt.Errorf("governance: quorum_bps < pass_threshold_bps")
	}
	if g.Governance.VotingPeriodSecs < MinVotingPeriodSeconds {
		return fmt.Errorf("governance: voting_period_seconds too small")
	}
	if g.Slashing.MinWindowSecs == 0 || g.Slashing.MinWindowSecs > g.Slashing.MaxWindowSecs {
		return fmt.Errorf("slashing: min_window > max_window or zero")
	}
	if g.Mempool.MaxBytes <= 0 {
		return fmt.Errorf("mempool: max_bytes <= 0")
	}
	if g.Mempool.POSReservationBPS > consensus.BPSDenominator {
		return fmt.Errorf("mempool: pos_reservation_bps > %d", consensus.BPSDenominator)
	}
	if g.Blocks.MaxTxs <= 0 {
		return fmt.Errorf("blocks: max_txs <= 0")
	}
	if g.Staking.AprBps > consensus.BPSDenominator {
		return fmt.Errorf("staking: apr_bps must be <= %d", consensus.BPSDenominator)
	}
	if g.Staking.PayoutPeriodDays == 0 {
		return fmt.Errorf("staking: payout_period_days must be >= 1")
	}
	if g.Staking.UnbondingDays == 0 {
		return fmt.Errorf("staking: unbonding_days must be >= 1")
	}
	if trimmed := strings.TrimSpace(g.Staking.MinStakeWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			return fmt.Errorf("staking: min_stake_wei must be a base-10 integer")
		}
		if amount.Sign() < 0 {
			return fmt.Errorf("staking: min_stake_wei must be >= 0")
		}
	}
	if trimmed := strings.TrimSpace(g.Staking.MaxEmissionPerYearWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			return fmt.Errorf("staking: max_emission_per_year_wei must be a base-10 integer")
		}
		if amount.Sign() < 0 {
			return fmt.Errorf("staking: max_emission_per_year_wei must be >= 0")
		}
	}
	if strings.TrimSpace(g.Staking.RewardAsset) == "" {
		return fmt.Errorf("staking: reward_asset must not be empty")
	}
	if _, err := g.PaymasterLimits(); err != nil {
		return fmt.Errorf("paymaster: %w", err)
	}
	if g.Loyalty.Dynamic.MinBPS > g.Loyalty.Dynamic.MaxBPS {
		return fmt.Errorf("loyalty.dynamic: min_bps must be <= max_bps")
	}
	if g.Loyalty.Dynamic.MaxBPS > consensus.BPSDenominator {
		return fmt.Errorf("loyalty.dynamic: max_bps must be <= %d", consensus.BPSDenominator)
	}
	if g.Loyalty.Dynamic.TargetBPS < g.Loyalty.Dynamic.MinBPS || g.Loyalty.Dynamic.TargetBPS > g.Loyalty.Dynamic.MaxBPS {
		return fmt.Errorf("loyalty.dynamic: target_bps must lie within the configured band")
	}
	if trimmed := strings.TrimSpace(g.Loyalty.Dynamic.DailyCapWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			return fmt.Errorf("loyalty.dynamic: daily_cap_wei must be a base-10 integer")
		}
		if amount.Sign() < 0 {
			return fmt.Errorf("loyalty.dynamic: daily_cap_wei must be >= 0")
		}
	}
	if trimmed := strings.TrimSpace(g.Loyalty.Dynamic.YearlyCapWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			return fmt.Errorf("loyalty.dynamic: yearly_cap_wei must be a base-10 integer")
		}
		if amount.Sign() < 0 {
			return fmt.Errorf("loyalty.dynamic: yearly_cap_wei must be >= 0")
		}
	}
	if g.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS > consensus.BPSDenominator {
		return fmt.Errorf("loyalty.dynamic.price_guard: max_deviation_bps must be <= %d", consensus.BPSDenominator)
	}
	znhbEnabled := false
	for _, asset := range g.Fees.Assets {
		if strings.EqualFold(strings.TrimSpace(asset.Asset), fees.AssetZNHB) {
			znhbEnabled = true
			break
		}
	}
	if znhbEnabled {
		wallets := g.Fees.RouteWalletByAsset()
		if strings.TrimSpace(wallets[fees.AssetZNHB]) == "" {
			return fmt.Errorf("fees: route_wallet_by_asset.%s must be configured when %s fees are enabled", fees.AssetZNHB, fees.AssetZNHB)
		}
	}
	return nil
}

// ValidateConsensus ensures consensus timeouts are positive durations.
func ValidateConsensus(c Consensus) error {
	if c.ProposalTimeout <= 0 {
		return fmt.Errorf("consensus: proposal timeout must be positive")
	}
	if c.PrevoteTimeout <= 0 {
		return fmt.Errorf("consensus: prevote timeout must be positive")
	}
	if c.PrecommitTimeout <= 0 {
		return fmt.Errorf("consensus: precommit timeout must be positive")
	}
	if c.CommitTimeout <= 0 {
		return fmt.Errorf("consensus: commit timeout must be positive")
	}
	return nil
}
