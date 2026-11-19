package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

// The [governance] voting period and timelock are read from each validator's own
// configuration file and applied to every new proposal. A floor under them is
// checked at start-up: below it the node WARNs (the default, because a running
// validator must keep starting), and with [governance] EnforceWindowFloors = true
// it refuses to start.

func TestCheckConfigurationWarnsAboutAGovernanceWindowBelowTheFloorAndDoesNotStop(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.Governance.VotingPeriodSeconds = 600
	cfg.Governance.TimelockSeconds = 120
	logger, buf := capturedLogger()
	found := checkConfiguration(logger, cfg)

	got := warnings(t, buf)
	if found != 2 || len(got) != 2 {
		t.Fatalf("found %d problems and %d warnings, want 2 of each: %s", found, len(got), buf.String())
	}
	for i, window := range []string{"VotingPeriodSeconds 600", "TimelockSeconds 120"} {
		rec := got[i]
		if rec["msg"] != "configuration problem" || rec["section"] != "governance" {
			t.Fatalf("warning %d = %v, want a configuration problem in section governance", i, rec)
		}
		if problem, _ := rec["problem"].(string); !strings.Contains(problem, window) {
			t.Fatalf("warning %d = %q does not name %q", i, problem, window)
		}
	}
	// Default: warned, not stopped.
	if err := enforceStartupConfiguration(cfg); err != nil {
		t.Fatalf("a window below the floor stopped a node that did not ask for enforcement: %v", err)
	}
}

func TestCheckConfigurationIsQuietForGovernanceWindowsAtTheFloors(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.Governance.VotingPeriodSeconds = config.MinVotingPeriodSeconds
	cfg.Governance.TimelockSeconds = config.MinTimelockSeconds
	logger, buf := capturedLogger()
	if found := checkConfiguration(logger, cfg); found != 0 {
		t.Fatalf("%d problems for windows at the floors: %s", found, buf.String())
	}
}

func TestEnforceStartupConfigurationRefusesAWindowBelowTheFloorOnlyWhenAskedTo(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.Governance.VotingPeriodSeconds = config.MinVotingPeriodSeconds - 1
	if err := enforceStartupConfiguration(cfg); err != nil {
		t.Fatalf("refused without EnforceWindowFloors: %v", err)
	}
	cfg.Governance.EnforceWindowFloors = true
	err := enforceStartupConfiguration(cfg)
	if err == nil || !strings.Contains(err.Error(), "VotingPeriodSeconds 3599") {
		t.Fatalf("with EnforceWindowFloors, error = %v, want a refusal naming VotingPeriodSeconds 3599", err)
	}
	cfg.Governance.VotingPeriodSeconds = config.MinVotingPeriodSeconds
	cfg.Governance.TimelockSeconds = config.MinTimelockSeconds - 1
	err = enforceStartupConfiguration(cfg)
	if err == nil || !strings.Contains(err.Error(), "TimelockSeconds 3599") {
		t.Fatalf("with EnforceWindowFloors, error = %v, want a refusal naming TimelockSeconds 3599", err)
	}
	cfg.Governance.TimelockSeconds = config.MinTimelockSeconds
	if err := enforceStartupConfiguration(cfg); err != nil {
		t.Fatalf("windows at the floors were refused: %v", err)
	}
}

const (
	floorSubprocessEnv = "NHB_GOVERNANCE_FLOOR_SUBPROCESS"
	floorConfigEnv     = "NHB_GOVERNANCE_FLOOR_CONFIG"
)

// The real start-up path: main() must stop on its own when the loaded
// configuration has a window below the floor and enforcement is on, before it
// opens a database or loads a key. A node that carried on would sit running until
// the timeout below kills it, and the test fails.
func TestNodeStartRefusesAGovernanceWindowBelowTheFloorWhenEnforced(t *testing.T) {
	if os.Getenv(floorSubprocessEnv) == "1" {
		os.Args = []string{"nhb", "--config", os.Getenv(floorConfigEnv)}
		main()
		return
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	dir := t.TempDir()
	text := string(raw)
	text = strings.Replace(text, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_TEST_VALIDATOR_KEY"`, 1)
	text = regexp.MustCompile(`(?m)^DataDir\s*=.*$`).ReplaceAllString(text, `DataDir = "`+filepath.ToSlash(filepath.Join(dir, "data"))+`"`)
	replaceOnce := func(from, to string) {
		t.Helper()
		if strings.Count(text, from) != 1 {
			t.Fatalf("expected exactly one %q in the shipped config.toml, found %d", from, strings.Count(text, from))
		}
		text = strings.Replace(text, from, to, 1)
	}
	replaceOnce("  VotingPeriodSeconds = 604800", "  VotingPeriodSeconds = 600")
	replaceOnce("  TimelockSeconds = 172800", "  TimelockSeconds = 120")
	replaceOnce("EnforceWindowFloors = false", "EnforceWindowFloors = true")
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestNodeStartRefusesAGovernanceWindowBelowTheFloorWhenEnforced$")
	cmd.Env = append(os.Environ(), floorSubprocessEnv+"=1", floorConfigEnv+"="+path)
	// A node that did NOT refuse would go on to write under its working directory
	// (a copy of the genesis file); keep that out of the repository.
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("the node was still running after 60s instead of refusing to start: %s", output.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 from a refusal, got %v: %s", runErr, output.String())
	}
	out := output.String()
	for _, want := range []string{"VotingPeriodSeconds 600", "TimelockSeconds 120", "EnforceWindowFloors"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q: %s", want, out)
		}
	}
}
