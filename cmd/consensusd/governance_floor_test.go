package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

// consensusd reads the [governance] block like nhb does. A voting period or
// timelock below its floor is a warning; with EnforceWindowFloors = true it is a
// refusal to start, before the node opens a database or loads a key.

func TestWarnGovernanceWindowsLogsEachWindowBelowItsFloorAndStopsNothing(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	if found := warnGovernanceWindows(logger, config.GovConfig{VotingPeriodSeconds: 600, TimelockSeconds: 120}); found != 2 {
		t.Fatalf("found %d, want 2", found)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want 2: %s", len(lines), buf.String())
	}
	for i, window := range []string{"VotingPeriodSeconds 600", "TimelockSeconds 120"} {
		var rec map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			t.Fatalf("log line %q: %v", lines[i], err)
		}
		problem, _ := rec["problem"].(string)
		if rec["level"] != "WARN" || rec["msg"] != "configuration problem" || rec["section"] != "governance" || !strings.Contains(problem, window) {
			t.Fatalf("line %d = %v, want a governance WARN naming %q", i, rec, window)
		}
	}

	buf.Reset()
	if found := warnGovernanceWindows(logger, config.GovConfig{VotingPeriodSeconds: config.MinVotingPeriodSeconds, TimelockSeconds: config.MinTimelockSeconds}); found != 0 || buf.Len() != 0 {
		t.Fatalf("windows at the floors logged %d problems: %s", found, buf.String())
	}
}

const (
	floorSubprocessEnv = "CONSENSUSD_GOVFLOOR_SUBPROCESS"
	floorConfigEnv     = "CONSENSUSD_GOVFLOOR_CONFIG"
)

func TestConsensusdRefusesAGovernanceWindowBelowTheFloorWhenEnforced(t *testing.T) {
	if os.Getenv(floorSubprocessEnv) == "1" {
		os.Args = []string{"consensusd", "--config", os.Getenv(floorConfigEnv)}
		main()
		return
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	// Everything valid except the [governance] windows: the [global] blocks pass
	// ValidateConfig, so a refusal can only come from the windows.
	contents := fmt.Sprintf(`ListenAddress = "127.0.0.1:0"
RPCAddress = "127.0.0.1:0"
DataDir = %q
GenesisFile = ""
AllowAutogenesis = true
ValidatorKeystorePath = %q

[global.Governance]
QuorumBPS = 6000
PassThresholdBPS = 5000
VotingPeriodSecs = 604800

[global.Slashing]
MinWindowSecs = 60
MaxWindowSecs = 600

[global.Mempool]
MaxBytes = 16777216

[global.Blocks]
MaxTxs = 500

[governance]
  VotingPeriodSeconds = 600
  TimelockSeconds = 120
  EnforceWindowFloors = true
`, filepath.ToSlash(filepath.Join(dir, "data")), filepath.ToSlash(keystorePath))
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestConsensusdRefusesAGovernanceWindowBelowTheFloorWhenEnforced$")
	cmd.Env = append(os.Environ(), floorSubprocessEnv+"=1", floorConfigEnv+"="+cfgPath, "NHB_VALIDATOR_PASS=test-passphrase")
	// A node that did NOT refuse would go on to write under its working directory;
	// keep that out of the repository.
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("consensusd was still running after 60s instead of refusing to start: %s", output.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("expected a non-zero exit from a refusal, got %v: %s", runErr, output.String())
	}
	out := output.String()
	// the windows are also warned about, before the refusal (a JSON log line with section governance)
	for _, want := range []string{"configuration problem", `"section":"governance"`, "invalid configuration", "VotingPeriodSeconds 600", "TimelockSeconds 120", "EnforceWindowFloors"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q: %s", want, out)
		}
	}
}
