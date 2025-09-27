package config

import "fmt"

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
	if g.Blocks.MaxTxs <= 0 {
		return fmt.Errorf("blocks: max_txs <= 0")
	}
	return nil
}
