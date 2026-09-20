package main

import (
	"log/slog"

	"nhbchain/config"
)

// checkConfiguration logs a structured WARN for every problem in the
// configuration the node is about to run with, and returns how many it found.
// It never stops the node: a check that is stricter than what a running
// validator needs would take that validator down, and everything it looks at
// has a value the node can run on (a timer that is not positive runs on the
// engine's default, see config.EffectiveConsensus). What it makes visible is a
// setting the operator believes is in force and is not, or one that a stricter
// binary (consensusd) would refuse.
//
// The consensus settings are checked as the node uses them, with the built-in
// defaults in place of the zeros a config may carry, not as written: both live
// validators write "0s" for the four round timers and must keep starting.
func checkConfiguration(logger *slog.Logger, cfg *config.Config) int {
	if logger == nil {
		logger = slog.Default()
	}
	found := 0
	for _, problem := range config.ConfigProblems(cfg.Global) {
		found++
		logger.Warn("configuration problem", slog.String("section", "global"), slog.String("problem", problem.Error()))
	}
	effective := config.EffectiveConsensus(cfg.Consensus)
	for _, problem := range config.ConsensusProblems(effective) {
		found++
		logger.Warn("configuration problem",
			slog.String("section", "consensus"),
			slog.String("problem", problem.Error()),
			slog.Duration("proposalTimeout", effective.ProposalTimeout),
			slog.Duration("prevoteTimeout", effective.PrevoteTimeout),
			slog.Duration("precommitTimeout", effective.PrecommitTimeout),
			slog.Duration("commitTimeout", effective.CommitTimeout),
			slog.Duration("minBlockInterval", effective.MinBlockInterval))
	}
	return found
}
