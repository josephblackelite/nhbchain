package config

import (
	"fmt"

	"nhbchain/consensus"
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
	if _, err := g.PaymasterLimits(); err != nil {
		return fmt.Errorf("paymaster: %w", err)
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
