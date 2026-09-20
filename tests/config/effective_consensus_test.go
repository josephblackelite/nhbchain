package config_test

// What the node's own checks make of a config like the live validators' (the four
// round timers written as "0s", a minimum block interval of 2s), and the
// collecting forms of the validators.

import (
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

func TestALiveValidatorConfigHasNoProblemInTheEffectiveSettings(t *testing.T) {
	// The four timers as the live validators write them, and their interval.
	cfg := loadShippedConfigWith(t, map[string]string{
		`(?m)^(\s*ProposalTimeout = )"[^"]*"`:  `${1}"0s"`,
		`(?m)^(\s*PrevoteTimeout = )"[^"]*"`:   `${1}"0s"`,
		`(?m)^(\s*PrecommitTimeout = )"[^"]*"`: `${1}"0s"`,
		`(?m)^(\s*CommitTimeout = )"[^"]*"`:    `${1}"0s"`,
		`(?m)^(\s*MinBlockInterval = )"[^"]*"`: `${1}"2s"`,
	})
	if cfg.Consensus.ProposalTimeout != 0 || cfg.Consensus.CommitTimeout != 0 || cfg.Consensus.MinBlockInterval != 2*time.Second {
		t.Fatalf("the loaded config is not the live one: %+v", cfg.Consensus)
	}

	// As written it is not something consensusd would start on, and the node
	// must not be judged on that: it runs on the built-in defaults.
	effective := config.EffectiveConsensus(cfg.Consensus)
	want := config.Consensus{
		ProposalTimeout:  2 * time.Second,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: 2 * time.Second,
	}
	if effective != want {
		t.Fatalf("effective consensus settings %+v, want %+v", effective, want)
	}
	if problems := config.ConsensusProblems(effective); len(problems) != 0 {
		t.Fatalf("the live consensus settings have problems: %v", problems)
	}
	if err := config.ValidateConsensus(effective); err != nil {
		t.Fatalf("ValidateConsensus: %v", err)
	}
	if problems := config.ConfigProblems(cfg.Global); len(problems) != 0 {
		t.Fatalf("the global settings of the live config have problems: %v", problems)
	}
}

func TestEffectiveConsensusOnlyReplacesTimersThatAreNotPositive(t *testing.T) {
	got := config.EffectiveConsensus(config.Consensus{
		ProposalTimeout:  500 * time.Millisecond,
		PrevoteTimeout:   -time.Second,
		CommitTimeout:    9 * time.Second,
		MinBlockInterval: 0,
	})
	want := config.Consensus{
		ProposalTimeout:  500 * time.Millisecond,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    9 * time.Second,
		MinBlockInterval: 0, // zero is a setting: it turns the wait off
	}
	if got != want {
		t.Fatalf("EffectiveConsensus = %+v, want %+v", got, want)
	}
}

func TestConsensusProblemsListsEveryProblem(t *testing.T) {
	problems := config.ConsensusProblems(config.Consensus{
		ProposalTimeout:  0,
		PrevoteTimeout:   -time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: 3 * time.Second,
	})
	if len(problems) != 3 {
		t.Fatalf("got %d problems (%v), want 3: proposal, prevote and the interval", len(problems), problems)
	}
	err := config.ValidateConsensus(config.Consensus{PrevoteTimeout: -time.Second, CommitTimeout: 4 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "proposal timeout") {
		t.Fatalf("ValidateConsensus returns the first problem, got %v", err)
	}
}

// ValidateConfig still stops at the first problem, in the order it always has;
// ConfigProblems reports the others after it.
func TestConfigProblemsListsEveryProblemAndValidateConfigTheFirst(t *testing.T) {
	var zero config.Global
	problems := config.ConfigProblems(zero)
	if len(problems) < 8 {
		t.Fatalf("an empty global config has %d problems, want many: %v", len(problems), problems)
	}
	err := config.ValidateConfig(zero)
	if err == nil || err.Error() != "governance: voting_period_seconds too small" {
		t.Fatalf("ValidateConfig(empty) = %v, want its first problem, the voting period", err)
	}
	if err.Error() != problems[0].Error() {
		t.Fatalf("ValidateConfig returned %q, the first of the problems is %q", err, problems[0])
	}

	// A value that cannot be parsed is one problem, not a crash on the next line.
	g := zero
	g.Staking = validStaking()
	g.Staking.MinStakeWei = "not-a-number"
	g.Staking.MaxEmissionPerYearWei = "-5"
	found := 0
	for _, p := range config.ConfigProblems(g) {
		if strings.Contains(p.Error(), "min_stake_wei must be a base-10 integer") || strings.Contains(p.Error(), "max_emission_per_year_wei must be >= 0") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("found %d of the 2 staking problems in %v", found, config.ConfigProblems(g))
	}
}
