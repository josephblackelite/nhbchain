package config

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	"nhbchain/consensus"
	"nhbchain/native/fees"
)

var (
	MinVotingPeriodSeconds = uint64(3600)
)

// ValidateConfig returns the first problem ConfigProblems finds, or nil.
func ValidateConfig(g Global) error {
	if problems := ConfigProblems(g); len(problems) > 0 {
		return problems[0]
	}
	return nil
}

// ConfigProblems reports everything wrong with the global settings, in the order
// the checks run, and nil when there is nothing. The node's startup check logs
// them all; ValidateConfig, which stops at the first, is what refuses a start.
func ConfigProblems(g Global) []error {
	var problems []error
	if g.Governance.QuorumBPS < g.Governance.PassThresholdBPS {
		problems = append(problems, fmt.Errorf("governance: quorum_bps < pass_threshold_bps"))
	}
	if g.Governance.VotingPeriodSecs < MinVotingPeriodSeconds {
		problems = append(problems, fmt.Errorf("governance: voting_period_seconds too small"))
	}
	if g.Slashing.MinWindowSecs == 0 || g.Slashing.MinWindowSecs > g.Slashing.MaxWindowSecs {
		problems = append(problems, fmt.Errorf("slashing: min_window > max_window or zero"))
	}
	if g.Mempool.MaxBytes <= 0 {
		problems = append(problems, fmt.Errorf("mempool: max_bytes <= 0"))
	}
	if g.Mempool.POSReservationBPS > consensus.BPSDenominator {
		problems = append(problems, fmt.Errorf("mempool: pos_reservation_bps > %d", consensus.BPSDenominator))
	}
	if g.Blocks.MaxTxs <= 0 {
		problems = append(problems, fmt.Errorf("blocks: max_txs <= 0"))
	}
	if g.Staking.AprBps > consensus.BPSDenominator {
		problems = append(problems, fmt.Errorf("staking: apr_bps must be <= %d", consensus.BPSDenominator))
	}
	if g.Staking.PayoutPeriodDays == 0 {
		problems = append(problems, fmt.Errorf("staking: payout_period_days must be >= 1"))
	}
	if g.Staking.UnbondingDays == 0 {
		problems = append(problems, fmt.Errorf("staking: unbonding_days must be >= 1"))
	}
	if trimmed := strings.TrimSpace(g.Staking.MinStakeWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			problems = append(problems, fmt.Errorf("staking: min_stake_wei must be a base-10 integer"))
		} else if amount.Sign() < 0 {
			problems = append(problems, fmt.Errorf("staking: min_stake_wei must be >= 0"))
		}
	}
	if trimmed := strings.TrimSpace(g.Staking.MaxEmissionPerYearWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			problems = append(problems, fmt.Errorf("staking: max_emission_per_year_wei must be a base-10 integer"))
		} else if amount.Sign() < 0 {
			problems = append(problems, fmt.Errorf("staking: max_emission_per_year_wei must be >= 0"))
		}
	}
	if strings.TrimSpace(g.Staking.RewardAsset) == "" {
		problems = append(problems, fmt.Errorf("staking: reward_asset must not be empty"))
	}
	if _, err := g.PaymasterLimits(); err != nil {
		problems = append(problems, fmt.Errorf("paymaster: %w", err))
	}
	if trimmed := strings.TrimSpace(g.Fees.TransferFreeTierSpendWei); trimmed != "" {
		amount, err := ParseAmount(trimmed)
		if err != nil {
			problems = append(problems, fmt.Errorf("fees: invalid transfer_free_tier_spend_wei: %w", err))
		} else if amount.Sign() < 0 {
			problems = append(problems, fmt.Errorf("fees: transfer_free_tier_spend_wei must be >= 0"))
		}
	}
	switch strings.ToLower(strings.TrimSpace(g.Fees.TransferFreeTierWindow)) {
	case "", "lifetime", "monthly":
	default:
		problems = append(problems, fmt.Errorf("fees: transfer_free_tier_window must be one of lifetime or monthly"))
	}
	if g.Loyalty.Dynamic.MinBPS > g.Loyalty.Dynamic.MaxBPS {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: min_bps must be <= max_bps"))
	}
	if g.Loyalty.Dynamic.MaxBPS > consensus.BPSDenominator {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: max_bps must be <= %d", consensus.BPSDenominator))
	}
	if g.Loyalty.Dynamic.TargetBPS < g.Loyalty.Dynamic.MinBPS || g.Loyalty.Dynamic.TargetBPS > g.Loyalty.Dynamic.MaxBPS {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: target_bps must lie within the configured band"))
	}
	if g.Loyalty.Dynamic.SmoothingStepBPS == 0 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: smoothing_step_bps must be >= 1"))
	}
	if g.Loyalty.Dynamic.CoverageLookbackDays == 0 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: coverage_lookback_days must be >= 1"))
	}
	if math.IsNaN(g.Loyalty.Dynamic.CoverageMax) || math.IsInf(g.Loyalty.Dynamic.CoverageMax, 0) {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: coverage_max must be a finite number"))
	}
	if g.Loyalty.Dynamic.CoverageMax < 0 || g.Loyalty.Dynamic.CoverageMax > 1 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: coverage_max must be between 0 and 1"))
	}
	if math.IsNaN(g.Loyalty.Dynamic.DailyCapPctOf7dFees) || math.IsInf(g.Loyalty.Dynamic.DailyCapPctOf7dFees, 0) {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: daily_cap_pct_of_7d_fees must be a finite number"))
	}
	if g.Loyalty.Dynamic.DailyCapPctOf7dFees < 0 || g.Loyalty.Dynamic.DailyCapPctOf7dFees > 1 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: daily_cap_pct_of_7d_fees must be between 0 and 1"))
	}
	if math.IsNaN(g.Loyalty.Dynamic.DailyCapUSD) || math.IsInf(g.Loyalty.Dynamic.DailyCapUSD, 0) {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: daily_cap_usd must be a finite number"))
	}
	if g.Loyalty.Dynamic.DailyCapUSD < 0 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: daily_cap_usd must be >= 0"))
	}
	if math.IsNaN(g.Loyalty.Dynamic.YearlyCapPctOfInitialSupply) || math.IsInf(g.Loyalty.Dynamic.YearlyCapPctOfInitialSupply, 0) {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: yearly_cap_pct_of_initial_supply must be a finite number"))
	}
	if g.Loyalty.Dynamic.YearlyCapPctOfInitialSupply < 0 || g.Loyalty.Dynamic.YearlyCapPctOfInitialSupply > 100 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic: yearly_cap_pct_of_initial_supply must be between 0 and 100"))
	}
	if strings.TrimSpace(g.Loyalty.Dynamic.PriceGuard.PricePair) == "" {
		problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: price_pair must not be empty"))
	}
	if g.Loyalty.Dynamic.PriceGuard.TwapWindowSeconds == 0 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: twap_window_seconds must be >= 1"))
	}
	if g.Loyalty.Dynamic.PriceGuard.PriceMaxAgeSeconds == 0 {
		problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: price_max_age_seconds must be >= 1"))
	}
	if g.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS > consensus.BPSDenominator {
		problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: max_deviation_bps must be <= %d", consensus.BPSDenominator))
	}
	if trimmed := strings.TrimSpace(g.Loyalty.Dynamic.PriceGuard.FallbackMinEmissionZNHBWei); trimmed != "" {
		amount, ok := new(big.Int).SetString(trimmed, 10)
		if !ok {
			problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: fallback_min_emission_znhb_wei must be a base-10 integer"))
		} else if amount.Sign() < 0 {
			problems = append(problems, fmt.Errorf("loyalty.dynamic.price_guard: fallback_min_emission_znhb_wei must be >= 0"))
		}
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
			problems = append(problems, fmt.Errorf("fees: route_wallet_by_asset.%s must be configured when %s fees are enabled", fees.AssetZNHB, fees.AssetZNHB))
		}
	}
	return problems
}

// EffectiveConsensus returns the consensus settings a node actually runs with.
// The BFT engine keeps its built-in value for a round timer that is not positive
// (bft.WithTimeouts), so a configuration that writes "0s" for a timer -- as the
// shipped one did, and as both live validators' do -- runs on that default:
// 2s for the proposal, prevote and precommit timers and 4s for the commit timer.
// The result has those defaults in place of the zeros; MinBlockInterval is left as
// written (zero is a setting: it turns the wait off). Check this, not the raw
// values, to know whether the node's timing is usable.
func EffectiveConsensus(c Consensus) Consensus {
	defaults := defaultConsensusConfig()
	if c.ProposalTimeout <= 0 {
		c.ProposalTimeout = defaults.ProposalTimeout
	}
	if c.PrevoteTimeout <= 0 {
		c.PrevoteTimeout = defaults.PrevoteTimeout
	}
	if c.PrecommitTimeout <= 0 {
		c.PrecommitTimeout = defaults.PrecommitTimeout
	}
	if c.CommitTimeout <= 0 {
		c.CommitTimeout = defaults.CommitTimeout
	}
	return c
}

// ValidateConsensus returns the first problem ConsensusProblems finds, or nil.
func ValidateConsensus(c Consensus) error {
	if problems := ConsensusProblems(c); len(problems) > 0 {
		return problems[0]
	}
	return nil
}

// ConsensusProblems reports everything wrong with the consensus settings: a
// timeout that is not a positive duration, and a minimum block interval that is
// negative or longer than half the commit timeout (zero turns the wait off; the
// engine lowers a longer one to that bound).
func ConsensusProblems(c Consensus) []error {
	var problems []error
	if c.ProposalTimeout <= 0 {
		problems = append(problems, fmt.Errorf("consensus: proposal timeout must be positive"))
	}
	if c.PrevoteTimeout <= 0 {
		problems = append(problems, fmt.Errorf("consensus: prevote timeout must be positive"))
	}
	if c.PrecommitTimeout <= 0 {
		problems = append(problems, fmt.Errorf("consensus: precommit timeout must be positive"))
	}
	if c.CommitTimeout <= 0 {
		problems = append(problems, fmt.Errorf("consensus: commit timeout must be positive"))
	}
	if c.MinBlockInterval < 0 {
		problems = append(problems, fmt.Errorf("consensus: minimum block interval must not be negative"))
	}
	if c.MinBlockInterval > c.CommitTimeout/2 {
		problems = append(problems, fmt.Errorf("consensus: minimum block interval %s must not exceed half the commit timeout (%s)", c.MinBlockInterval, c.CommitTimeout/2))
	}
	return problems
}
