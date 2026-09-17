package main

import (
	"bytes"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestStartRelayerBalanceMonitorWarnsWhenLow proves the monitor added to
// main.go actually calls RelayerBalance and logs a WARN when the balance is
// at or below the configured threshold -- this service previously had
// nothing watching whether its relayer's gas balance was running low (see
// docs/escrow/nhbchain-escrow-gateway.md's Production Deployment section).
func TestStartRelayerBalanceMonitorWarnsWhenLow(t *testing.T) {
	var buf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(originalLogger)

	node := &mockNodeClient{balanceResp: big.NewInt(500)}
	minBalance := big.NewInt(1_000)

	stop := startRelayerBalanceMonitor(node, "nhb1relayer...", minBalance, time.Hour)
	stop()

	node.mu.Lock()
	calls := node.balanceCalls
	node.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly one balance check on startup, got %d", calls)
	}

	logged := buf.String()
	if !strings.Contains(logged, "escrow gateway relayer balance is low") {
		t.Fatalf("expected a low-balance warning, got log output: %s", logged)
	}
	if !strings.Contains(logged, `"balanceWei":"500"`) {
		t.Fatalf("expected balanceWei=500 in log output: %s", logged)
	}
	if !strings.Contains(logged, `"minBalanceWei":"1000"`) {
		t.Fatalf("expected minBalanceWei=1000 in log output: %s", logged)
	}
}

// TestStartRelayerBalanceMonitorSilentWhenHealthy proves a healthy balance
// produces no warning at all -- this monitor is meant to be silent noise-wise
// until there's actually something to act on.
func TestStartRelayerBalanceMonitorSilentWhenHealthy(t *testing.T) {
	var buf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(originalLogger)

	node := &mockNodeClient{balanceResp: big.NewInt(5_000_000)}
	minBalance := big.NewInt(1_000)

	stop := startRelayerBalanceMonitor(node, "nhb1relayer...", minBalance, time.Hour)
	stop()

	if logged := buf.String(); strings.Contains(logged, "low") {
		t.Fatalf("expected no low-balance warning for a healthy balance, got: %s", logged)
	}
}

// TestStartRelayerBalanceMonitorWarnsOnQueryFailure proves an RPC failure
// while checking the balance also surfaces as a WARN, rather than silently
// vanishing (an escrow-gateway that can't even reach the node to check its
// own balance is itself worth knowing about).
func TestStartRelayerBalanceMonitorWarnsOnQueryFailure(t *testing.T) {
	var buf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(originalLogger)

	node := &mockNodeClient{balanceErr: errors.New("connection refused")}

	stop := startRelayerBalanceMonitor(node, "nhb1relayer...", big.NewInt(1_000), time.Hour)
	stop()

	logged := buf.String()
	if !strings.Contains(logged, "escrow gateway relayer balance check failed") {
		t.Fatalf("expected a check-failed warning, got: %s", logged)
	}
	if !strings.Contains(logged, "connection refused") {
		t.Fatalf("expected the underlying error in the log output: %s", logged)
	}
}

// TestStartRelayerBalanceMonitorTicks proves the ticker actually fires more
// than the one startup check when the interval elapses.
func TestStartRelayerBalanceMonitorTicks(t *testing.T) {
	node := &mockNodeClient{balanceResp: big.NewInt(5_000_000)}
	stop := startRelayerBalanceMonitor(node, "nhb1relayer...", big.NewInt(1_000), 10*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		node.mu.Lock()
		calls := node.balanceCalls
		node.mu.Unlock()
		if calls >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected at least 3 balance checks within 2s, got %d", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
