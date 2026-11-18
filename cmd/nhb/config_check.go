package main

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"nhbchain/config"
)

// enforceStartupConfiguration returns the error that must stop the node, nil for
// every configuration that may run. It is nil unless the operator opted in: today
// the only setting is [governance] EnforceWindowFloors, which makes a voting
// period or timelock below its floor a refusal to start instead of a warning.
// checkConfiguration has already logged the same problems.
func enforceStartupConfiguration(cfg *config.Config) error {
	if err := config.EnforceGovernanceWindowFloors(cfg.Governance); err != nil {
		return err
	}
	return config.EnforceFeeScheduleFloors(cfg)
}

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
	for _, key := range cfg.UndecodedKeys {
		found++
		logger.Warn("configuration problem", slog.String("section", "file"),
			slog.String("problem", "the key "+key+" in the config file is not a setting this build knows, so it is ignored (a misspelling, or a setting from another version)"))
	}
	for _, problem := range config.ConfigProblems(cfg.Global) {
		found++
		logger.Warn("configuration problem", slog.String("section", "global"), slog.String("problem", problem.Error()))
	}
	// The [governance] block is a different block from [global.Governance] above:
	// it is the one the proposal engine reads. Its voting period and timelock get
	// the same floor as the global one; see enforceStartupConfiguration for the
	// setting that turns the warning into a refusal to start.
	for _, problem := range config.GovernancePolicyProblems(cfg.Governance) {
		found++
		logger.Warn("configuration problem", slog.String("section", "governance"), slog.String("problem", problem.Error()))
	}
	// The transfer fee schedule under governance (reports nothing while it is off).
	for _, problem := range config.FeeScheduleProblems(cfg) {
		found++
		logger.Warn("configuration problem", slog.String("section", "fees"), slog.String("problem", problem.Error()))
	}
	// A partner key with no PartnerProviders entry reads no swap voucher: say so,
	// or an operator who has just added the key sees an empty ledger and no why.
	// The keys are compared trimmed, the way the node matches them; the warning
	// names a key, never a secret.
	mapped := make(map[string]struct{}, len(cfg.RPCSwapAuth.PartnerProviders))
	for key := range cfg.RPCSwapAuth.PartnerProviders {
		mapped[strings.TrimSpace(key)] = struct{}{}
	}
	unmapped := make([]string, 0)
	for key, secret := range cfg.RPCSwapAuth.Secrets {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" || strings.TrimSpace(secret) == "" {
			continue
		}
		if _, ok := mapped[trimmed]; !ok {
			unmapped = append(unmapped, trimmed)
		}
	}
	sort.Strings(unmapped)
	for _, key := range unmapped {
		found++
		logger.Warn("configuration problem", slog.String("section", "swap-auth"),
			slog.String("problem", fmt.Sprintf("the swap partner key %q has no entry in RPCSwapAuth.PartnerProviders, so it reads no swap voucher (swap_voucher_get, _list and _export return nothing for it); list the providers it may read, or \"*\" for all", key)))
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
