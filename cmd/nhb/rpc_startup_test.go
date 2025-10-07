package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"nhbchain/config"
)

func TestRPCStartupFailurePreventsConsensus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	passphrase := "testpass"
	t.Setenv(validatorPassEnv, passphrase)

	cfg, err := config.Load(configPath, config.WithKeystorePassphrase(passphrase))
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	rpcPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	cfg.RPCAddress = fmt.Sprintf("127.0.0.1:%d", rpcPort)
	cfg.RPCAllowInsecure = false
	cfg.RPCTLSCertFile = ""
	cfg.RPCTLSKeyFile = ""
	cfg.RPCTLSClientCAFile = ""
	cfg.AllowAutogenesis = true
	cfg.DataDir = filepath.Join(tempDir, "data")
	cfg.ListenAddress = fmt.Sprintf("127.0.0.1:%d", rpcPort+1)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatalf("failed to create data dir: %v", err)
	}

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("failed to open config for writing: %v", err)
	}
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		f.Close()
		t.Fatalf("failed to encode config: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("failed to close config file: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)

	cmd := exec.Command("go", "run", ".", "--config", configPath)
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(), validatorPassEnv+"="+passphrase)
	output, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("expected node startup to fail, output: %s", output)
	}
	if exitErr, ok := runErr.(*exec.ExitError); !ok {
		t.Fatalf("failed to run node: %v\n%s", runErr, output)
	} else if exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit code, got 0\n%s", output)
	}

	out := string(output)
	expectedErr := "TLS is required for RPC server; configure certificates or enable AllowInsecure"
	expectedLog := fmt.Sprintf("RPC server failed to start: %s", expectedErr)
	if !strings.Contains(out, expectedLog) {
		t.Fatalf("expected TLS startup error %q, output: %s", expectedLog, out)
	}
	if strings.Contains(out, "--- NHBCoin Node Initialized and Running ---") {
		t.Fatalf("consensus appears to have started despite RPC failure:\n%s", out)
	}
}
