package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The [governance] block (GovConfig) sets the voting period and the timelock
// every validator applies to a new governance proposal. They come from each
// validator's own configuration file, not from chain state, and nothing used to
// check them: the 3600-second floor in ValidateConfig covers [global.Governance],
// a different block that the proposal engine does not read
// (docs/gov/policy-invariants.md). GovernancePolicyProblems puts the same
// floor under [governance]; EnforceGovernanceWindowFloors turns it into a
// refusal to start, which is off unless the operator sets EnforceWindowFloors.

func TestGovernanceWindowFloorsAreAnHour(t *testing.T) {
	// The voting-period floor is the one [global.Governance] already has; the
	// timelock floor is the engine's own lower bound for gov.timelock.DurationSeconds.
	if MinVotingPeriodSeconds != 3600 {
		t.Fatalf("voting period floor = %d, want 3600", MinVotingPeriodSeconds)
	}
	if MinTimelockSeconds != 3600 {
		t.Fatalf("timelock floor = %d, want 3600", MinTimelockSeconds)
	}
}

func TestGovernancePolicyProblemsIsQuietAtAndAboveTheFloors(t *testing.T) {
	for name, g := range map[string]GovConfig{
		"at the floors":    {VotingPeriodSeconds: MinVotingPeriodSeconds, TimelockSeconds: MinTimelockSeconds},
		"the defaults":     {VotingPeriodSeconds: 604800, TimelockSeconds: 172800},
		"a long timelock":  {VotingPeriodSeconds: 7200, TimelockSeconds: 2592000},
		"one over a floor": {VotingPeriodSeconds: MinVotingPeriodSeconds + 1, TimelockSeconds: MinTimelockSeconds + 1},
	} {
		if problems := GovernancePolicyProblems(g); len(problems) != 0 {
			t.Errorf("%s: unexpected problems %v", name, problems)
		}
	}
}

func TestGovernancePolicyProblemsNamesEachWindowBelowItsFloor(t *testing.T) {
	cases := []struct {
		name string
		g    GovConfig
		want []string // substrings, one per problem, in order
	}{
		{"voting period one second short", GovConfig{VotingPeriodSeconds: MinVotingPeriodSeconds - 1, TimelockSeconds: 172800}, []string{"VotingPeriodSeconds 3599"}},
		{"timelock one second short", GovConfig{VotingPeriodSeconds: 604800, TimelockSeconds: MinTimelockSeconds - 1}, []string{"TimelockSeconds 3599"}},
		{"both short (a 10 minute vote and a 2 minute timelock)", GovConfig{VotingPeriodSeconds: 600, TimelockSeconds: 120}, []string{"VotingPeriodSeconds 600", "TimelockSeconds 120"}},
		{"both zero", GovConfig{}, []string{"VotingPeriodSeconds 0", "TimelockSeconds 0"}},
	}
	for _, tc := range cases {
		problems := GovernancePolicyProblems(tc.g)
		if len(problems) != len(tc.want) {
			t.Errorf("%s: %d problems %v, want %d", tc.name, len(problems), problems, len(tc.want))
			continue
		}
		for i, want := range tc.want {
			msg := problems[i].Error()
			if !strings.Contains(msg, want) {
				t.Errorf("%s: problem %d = %q, want it to contain %q", tc.name, i, msg, want)
			}
			if !strings.Contains(msg, "3600") {
				t.Errorf("%s: problem %d = %q does not state the floor", tc.name, i, msg)
			}
			if !strings.HasPrefix(msg, "governance: ") {
				t.Errorf("%s: problem %d = %q does not name its section", tc.name, i, msg)
			}
		}
	}
}

func TestEnforceGovernanceWindowFloorsIsOffUnlessAskedFor(t *testing.T) {
	g := GovConfig{VotingPeriodSeconds: 600, TimelockSeconds: 120}
	if err := EnforceGovernanceWindowFloors(g); err != nil {
		t.Fatalf("a policy below the floors was refused without EnforceWindowFloors: %v", err)
	}
}

func TestEnforceGovernanceWindowFloorsRefusesAPolicyBelowTheFloors(t *testing.T) {
	for name, g := range map[string]GovConfig{
		"voting period": {VotingPeriodSeconds: MinVotingPeriodSeconds - 1, TimelockSeconds: 172800, EnforceWindowFloors: true},
		"timelock":      {VotingPeriodSeconds: 604800, TimelockSeconds: MinTimelockSeconds - 1, EnforceWindowFloors: true},
		"both":          {VotingPeriodSeconds: 600, TimelockSeconds: 120, EnforceWindowFloors: true},
	} {
		err := EnforceGovernanceWindowFloors(g)
		if err == nil {
			t.Errorf("%s: a policy below the floor was not refused", name)
			continue
		}
		if !strings.Contains(err.Error(), "EnforceWindowFloors") {
			t.Errorf("%s: %q does not say which setting turned the refusal on", name, err)
		}
	}
	// A refusal names everything wrong, not only the first problem.
	err := EnforceGovernanceWindowFloors(GovConfig{VotingPeriodSeconds: 600, TimelockSeconds: 120, EnforceWindowFloors: true})
	if err == nil || !strings.Contains(err.Error(), "VotingPeriodSeconds 600") || !strings.Contains(err.Error(), "TimelockSeconds 120") {
		t.Errorf("refusal %v does not name both windows", err)
	}
}

func TestEnforceGovernanceWindowFloorsAllowsAPolicyAtTheFloors(t *testing.T) {
	g := GovConfig{VotingPeriodSeconds: MinVotingPeriodSeconds, TimelockSeconds: MinTimelockSeconds, EnforceWindowFloors: true}
	if err := EnforceGovernanceWindowFloors(g); err != nil {
		t.Fatalf("a policy at the floors was refused: %v", err)
	}
}

// loadGovernanceBlock loads the shipped config.toml with its [governance] block
// replaced by the given lines, the way an operator edits the file.
func loadGovernanceBlock(t *testing.T, lines string) *Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	text := string(raw)
	start := strings.Index(text, "\n[governance]\n")
	if start < 0 {
		t.Fatal("no [governance] block in the shipped config.toml")
	}
	rest := text[start+len("\n[governance]\n"):]
	end := strings.Index(rest, "\n[")
	if end < 0 {
		t.Fatal("no section after [governance] in the shipped config.toml")
	}
	text = text[:start] + "\n[governance]\n" + lines + "\n" + rest[end:]

	dir := t.TempDir()
	text = strings.Replace(text, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_TEST_VALIDATOR_KEY"`, 1)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// Both live validators run EnforceWindowFloors on (ledger PL-PUB-06: the settings digest of a node started from the shipped
// file differed from theirs while it said off), so the shipped file says on. That is safe only while the shipped windows are
// above the floors, which is what this test proves, both by the policy check and by loading the shipped windows with the
// setting on (a node with a window below a floor would refuse to start).
func TestShippedConfigHasWindowsAboveTheFloorsAndEnforcementOnLikeTheLiveValidators(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	cfg := loadGovernanceBlock(t, `  VotingPeriodSeconds = 604800
  TimelockSeconds = 172800`)
	if problems := GovernancePolicyProblems(cfg.Governance); len(problems) != 0 {
		t.Fatalf("the shipped windows are below the floors: %v", problems)
	}
	// the shipped windows load with enforcement on
	on := loadGovernanceBlock(t, `  VotingPeriodSeconds = 604800
  TimelockSeconds = 172800
  EnforceWindowFloors = true`)
	if !on.Governance.EnforceWindowFloors {
		t.Fatal("the loader did not read EnforceWindowFloors = true")
	}
	// the shipped file carries the live setting
	if !strings.Contains(string(raw), "\n  EnforceWindowFloors = true\n") || strings.Contains(string(raw), "EnforceWindowFloors = false") {
		t.Fatal("the shipped config.toml does not carry EnforceWindowFloors = true (the live validators' setting)")
	}
}

func TestLoaderReadsEnforceWindowFloorsFromTheGovernanceBlock(t *testing.T) {
	cfg := loadGovernanceBlock(t, `  VotingPeriodSeconds = 600
  TimelockSeconds = 120
  EnforceWindowFloors = true`)
	if !cfg.Governance.EnforceWindowFloors {
		t.Fatal("the loader did not read EnforceWindowFloors (a key in the wrong place is ignored)")
	}
	if cfg.Governance.VotingPeriodSeconds != 600 || cfg.Governance.TimelockSeconds != 120 {
		t.Fatalf("windows = %d/%d, want 600/120", cfg.Governance.VotingPeriodSeconds, cfg.Governance.TimelockSeconds)
	}
	if len(cfg.UndecodedKeys) != 0 {
		t.Fatalf("keys the build does not know: %v", cfg.UndecodedKeys)
	}
	if err := EnforceGovernanceWindowFloors(cfg.Governance); err == nil {
		t.Fatal("the loaded 600/120 policy with EnforceWindowFloors on was not refused")
	}
}

func TestLoaderLeavesAnUnsetWindowAtItsDefaultAboveTheFloor(t *testing.T) {
	// A window left out of the file is filled with the default (7 days, 2 days),
	// which is above the floor, so leaving it out cannot trip the check.
	cfg := loadGovernanceBlock(t, `  EnforceWindowFloors = true`)
	if cfg.Governance.VotingPeriodSeconds != 604800 || cfg.Governance.TimelockSeconds != 172800 {
		t.Fatalf("defaults = %d/%d, want 604800/172800", cfg.Governance.VotingPeriodSeconds, cfg.Governance.TimelockSeconds)
	}
	if err := EnforceGovernanceWindowFloors(cfg.Governance); err != nil {
		t.Fatalf("defaults were refused: %v", err)
	}
}
