package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/config"
)

const (
	subprocessEnv = "CONSENSUSD_SUBPROCESS"
	configPathEnv = "CONSENSUSD_CONFIG"
)

func TestConsensusdFailsOnInvalidGlobalConfig(t *testing.T) {
	if os.Getenv(subprocessEnv) == "1" {
		cfgPath := os.Getenv(configPathEnv)
		if cfgPath == "" {
			t.Fatalf("missing %s", configPathEnv)
		}
		os.Args = []string{"consensusd", "--config", cfgPath}
		main()
		return
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "127.0.0.1:0"
RPCAddress = "127.0.0.1:0"
DataDir = %q
GenesisFile = ""
AllowAutogenesis = true
ValidatorKeystorePath = %q

[global.governance]
QuorumBPS = 6000
PassThresholdBPS = 5000
VotingPeriodSecs = %d

[global.slashing]
MinWindowSecs = 1
MaxWindowSecs = 10

[global.mempool]
MaxBytes = 100

[global.blocks]
MaxTxs = 0
`, dir, keystorePath, config.MinVotingPeriodSeconds)
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestConsensusdFailsOnInvalidGlobalConfig$")
	cmd.Env = append(os.Environ(), subprocessEnv+"=1", configPathEnv+"="+cfgPath, "NHB_VALIDATOR_PASS=test-passphrase")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected consensusd to exit with error, output=%s", output.String())
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 0 {
			t.Fatalf("expected non-zero exit code")
		}
	} else {
		t.Fatalf("unexpected error type: %v", err)
	}

	if !strings.Contains(output.String(), "invalid configuration") {
		t.Fatalf("expected output to mention invalid configuration, got %s", output.String())
	}
}
