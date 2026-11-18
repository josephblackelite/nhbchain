package main

import (
	"log/slog"

	"nhbchain/config"
)

// warnGovernanceWindows logs a structured WARN for each [governance] voting
// window below its floor (config.GovernancePolicyProblems) and returns how many
// it found. It never stops the node; EnforceGovernanceWindowFloors does that, for
// an operator who turned EnforceWindowFloors on.
func warnGovernanceWindows(logger *slog.Logger, g config.GovConfig) int {
	if logger == nil {
		logger = slog.Default()
	}
	found := 0
	for _, problem := range config.GovernancePolicyProblems(g) {
		found++
		logger.Warn("configuration problem", slog.String("section", "governance"), slog.String("problem", problem.Error()))
	}
	return found
}
