package main

// What the command line does as a process: its exit codes, what generate-key
// does to a key that is already there, and which commands are gone or retired.
// main runs in a child copy of the test binary, so the exit code is the real one
// and nothing here depends on how main is put together.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const helperProcessEnv = "NHB_CLI_TEST_HELPER_PROCESS"

// TestHelperProcess is not a test: it is the child process runCLI starts.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Args = append([]string{"nhb-cli"}, args...)
	main()
	os.Exit(0)
}

type cliResult struct {
	stdout, stderr string
	code           int
}

func (r cliResult) output() string { return r.stdout + r.stderr }

// runCLI runs the command line in dir with the given environment settings
// (NAME=value) on top of the current environment.
func runCLI(t *testing.T, dir string, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), helperProcessEnv+"=1", "NHB_RPC_TOKEN=", "RPC_URL=http://127.0.0.1:1")
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// recordingNode is an RPC endpoint that answers every call with an empty result
// and remembers what it was asked.
type recordingNode struct {
	*httptest.Server
	mu       sync.Mutex
	methods  []string
	authSeen []string
}

func newRecordingNode(t *testing.T) *recordingNode {
	t.Helper()
	node := &recordingNode{}
	node.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		node.mu.Lock()
		node.methods = append(node.methods, req.Method)
		node.authSeen = append(node.authSeen, r.Header.Get("Authorization"))
		node.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(node.Close)
	return node
}

func (n *recordingNode) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.methods...)
}

func TestGenerateKeyNeverOverwritesAnExistingKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "wallet.key")

	first := runCLI(t, dir, nil, "generate-key")
	if first.code != 0 {
		t.Fatalf("the first generate-key failed: %d %s", first.code, first.output())
	}
	original, err := os.ReadFile(keyPath)
	if err != nil || len(original) == 0 {
		t.Fatalf("no key was written: %v", err)
	}

	second := runCLI(t, dir, nil, "generate-key")
	if second.code == 0 {
		t.Fatalf("generate-key overwrote an existing key and exited 0: %s", second.output())
	}
	if !strings.Contains(second.output(), "already exists") {
		t.Fatalf("the refusal does not say why: %q", second.output())
	}
	if now, _ := os.ReadFile(keyPath); !bytes.Equal(now, original) {
		t.Fatalf("the existing key was changed by a refused generate-key")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "wallet.key.bak-*")); len(matches) != 0 {
		t.Fatalf("a refused generate-key left backups behind: %v", matches)
	}

	// An option that is not --force is not taken for it.
	if r := runCLI(t, dir, nil, "generate-key", "--forc"); r.code == 0 {
		t.Fatalf("an unknown option was accepted: %s", r.output())
	}
	if now, _ := os.ReadFile(keyPath); !bytes.Equal(now, original) {
		t.Fatalf("the existing key was changed by generate-key with an unknown option")
	}

	forced := runCLI(t, dir, nil, "generate-key", "--force")
	if forced.code != 0 {
		t.Fatalf("generate-key --force failed: %d %s", forced.code, forced.output())
	}
	replaced, _ := os.ReadFile(keyPath)
	if bytes.Equal(replaced, original) || len(replaced) == 0 {
		t.Fatalf("generate-key --force did not write a new key")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "wallet.key.bak-*"))
	if len(backups) != 1 {
		t.Fatalf("expected exactly one backup, found %v", backups)
	}
	if saved, _ := os.ReadFile(backups[0]); !bytes.Equal(saved, original) {
		t.Fatalf("the backup is not a copy of the key that was replaced")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(backups[0])
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the backup must be private (0600): %v %v", info, err)
		}
	}
}

func TestGenerateKeyForceWithNoExistingKeyJustWritesOne(t *testing.T) {
	dir := t.TempDir()
	if r := runCLI(t, dir, nil, "generate-key", "--force"); r.code != 0 {
		t.Fatalf("generate-key --force failed: %d %s", r.code, r.output())
	}
	if _, err := os.Stat(filepath.Join(dir, "wallet.key")); err != nil {
		t.Fatalf("no key was written: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "wallet.key.bak-*")); len(matches) != 0 {
		t.Fatalf("a backup of nothing was made: %v", matches)
	}
}

func TestFailingCommandsExitNonZero(t *testing.T) {
	dir := t.TempDir()
	// The RPC endpoint is closed (127.0.0.1:1), so every command that needs the
	// node fails; the key files named below do not exist.
	cases := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"no-such-command"}},
		{"balance without an address", []string{"balance"}},
		{"balance with the node down", []string{"balance", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0"}},
		{"claim-username without arguments", []string{"claim-username"}},
		{"claim-username with no key", []string{"claim-username", "bob", "missing.key"}},
		{"un-stake with a bad amount", []string{"un-stake", "0", "missing.key"}},
		{"un-stake with no key", []string{"un-stake", "5", "missing.key"}},
		{"stake with no key", []string{"stake", "5", "missing.key"}},
		{"heartbeat without a key file", []string{"heartbeat"}},
		{"heartbeat with no key", []string{"heartbeat", "missing.key"}},
		{"address without a key file", []string{"address"}},
		{"address with no key", []string{"address", "missing.key"}},
		{"loyalty-create-business with no key", []string{"loyalty-create-business", "Acme", "missing.key"}},
		{"loyalty-create-business without arguments", []string{"loyalty-create-business"}},
		{"loyalty-create-program with bad JSON", []string{"loyalty-create-program", "0x01", "{not json", "missing.key"}},
		{"loyalty-update-program with bad JSON", []string{"loyalty-update-program", "{not json", "missing.key"}},
		{"loyalty-pause-program with no key", []string{"loyalty-pause-program", "0x01", "missing.key"}},
		{"loyalty-get-business with the node down", []string{"loyalty-get-business", "0x01"}},
		{"loyalty-list-programs with the node down", []string{"loyalty-list-programs", "0x01"}},
		{"loyalty-user-qr with a bad mode", []string{"loyalty-user-qr", "bogus", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r := runCLI(t, dir, nil, tc.args...); r.code == 0 {
				t.Fatalf("exited 0 after failing; output: %s", r.output())
			}
		})
	}

	t.Run("help is not a failure", func(t *testing.T) {
		for _, arg := range []string{"help", "--help", "-h"} {
			r := runCLI(t, dir, nil, arg)
			if r.code != 0 || !strings.Contains(r.stdout, "Usage: nhb-cli") {
				t.Fatalf("%s: exit %d, output %q", arg, r.code, r.output())
			}
		}
	})
}

func TestDeployCommandIsGone(t *testing.T) {
	dir := t.TempDir()
	r := runCLI(t, dir, nil, "deploy", "contract.hex", "wallet.key")
	if r.code == 0 || !strings.Contains(r.output(), "Unknown command: deploy") {
		t.Fatalf("deploy: exit %d, output %q; want an unknown command", r.code, r.output())
	}
	usage := runCLI(t, dir, nil, "help")
	if strings.Contains(usage.stdout, "deploy") {
		t.Fatalf("the usage text still lists deploy:\n%s", usage.stdout)
	}
}

func TestRetiredCommandsSayTheyAreRetiredAndDoNotCallTheNode(t *testing.T) {
	node := newRecordingNode(t)
	dir := t.TempDir()
	env := []string{"RPC_URL=" + node.URL, "NHB_RPC_TOKEN=test-token"}
	zeros := "0x" + strings.Repeat("00", 32)
	addr := "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0"

	cases := []struct {
		name   string
		args   []string
		method string
	}{
		{"stake claim", []string{"stake", "claim", addr}, "stake_claimRewards"},
		{"id set-alias", []string{"id", "set-alias", "--addr", addr, "--alias", "builder"}, "identity_setAlias"},
		{"id claim", []string{"id", "claim", "--id", zeros, "--payee", addr}, "identity_claim"},
		{"claimable create", []string{"claimable", "create", "--payer", addr, "--token", "NHB", "--amount", "1", "--deadline", "+1h", "--hash-lock", zeros}, "claimable_create"},
		{"claimable claim", []string{"claimable", "claim", "--id", zeros, "--payee", addr, "--preimage", zeros}, "claimable_claim"},
		{"claimable cancel", []string{"claimable", "cancel", "--id", zeros, "--caller", addr}, "claimable_cancel"},
		{"p2p create-trade", []string{"p2p", "create-trade", "--offer", "OFF-1", "--buyer", addr, "--seller", addr, "--base", "NHB", "--base-amount", "1", "--quote", "ZNHB", "--quote-amount", "1", "--deadline", "+1h"}, "p2p_createTrade"},
		{"p2p settle", []string{"p2p", "settle", "--id", zeros, "--caller", addr}, "p2p_settle"},
		{"p2p dispute", []string{"p2p", "dispute", "--id", zeros, "--caller", addr, "--reason", "x"}, "p2p_dispute"},
		{"p2p resolve", []string{"p2p", "resolve", "--id", zeros, "--caller", addr, "--outcome", "release_both"}, "p2p_resolve"},
		{"pos sweep-voids", []string{"pos", "sweep-voids"}, "pos_sweepVoids"},
		{"potso reward claim", []string{"potso", "reward", "claim", "--epoch", "1", "--addr", addr}, "potso_reward_claim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, dir, env, tc.args...)
			if r.code == 0 {
				t.Fatalf("exited 0; output: %s", r.output())
			}
			if !strings.Contains(r.stderr, "retired") || !strings.Contains(r.stderr, tc.method) {
				t.Fatalf("stderr %q does not say that %s is retired", r.stderr, tc.method)
			}
		})
	}
	if calls := node.calls(); len(calls) != 0 {
		t.Fatalf("retired commands sent %v to the node", calls)
	}

	// The read-only neighbours are not retired.
	for _, tc := range []struct {
		args   []string
		method string
	}{
		{[]string{"id", "resolve", "--alias", "builder"}, "identity_resolve"},
		{[]string{"claimable", "get", "--id", zeros}, "claimable_get"},
		{[]string{"p2p", "get", "--id", zeros}, "p2p_getTrade"},
	} {
		before := len(node.calls())
		if r := runCLI(t, dir, env, tc.args...); r.code != 0 {
			t.Fatalf("%v: exit %d, output %s", tc.args, r.code, r.output())
		}
		if calls := node.calls(); len(calls) != before+1 || calls[len(calls)-1] != tc.method {
			t.Fatalf("%v: the node saw %v, want one more call, %s", tc.args, calls, tc.method)
		}
	}
}

// The node answers the voucher ledger reads only with a credential, so the swap
// commands send the bearer token and stop early when there is none.
func TestSwapVoucherCommandsSendTheBearerToken(t *testing.T) {
	node := newRecordingNode(t)
	dir := t.TempDir()

	r := runCLI(t, dir, []string{"RPC_URL=" + node.URL, "NHB_RPC_TOKEN=test-token"}, "swap", "voucher", "list", "0", "0")
	if r.code != 0 {
		t.Fatalf("swap voucher list: exit %d, output %s", r.code, r.output())
	}
	node.mu.Lock()
	seen := append([]string(nil), node.authSeen...)
	node.mu.Unlock()
	if len(seen) != 1 || seen[0] != "Bearer test-token" {
		t.Fatalf("the node saw Authorization %q, want the bearer token", seen)
	}

	before := len(node.calls())
	r = runCLI(t, dir, []string{"RPC_URL=" + node.URL, "NHB_RPC_TOKEN="}, "swap", "voucher", "get", "PTX-1")
	if r.code == 0 || !strings.Contains(r.output(), "NHB_RPC_TOKEN") {
		t.Fatalf("without a token: exit %d, output %q; want a failure that names NHB_RPC_TOKEN", r.code, r.output())
	}
	if len(node.calls()) != before {
		t.Fatalf("a request went out without a credential")
	}
}

// scripts read the address from stdout (scripts/deployvalidator.sh does), so a
// key that cannot be read must leave stdout empty.
func TestAddressFailureIsOnStderrOnly(t *testing.T) {
	dir := t.TempDir()
	r := runCLI(t, dir, nil, "address", "missing.key")
	if r.code == 0 {
		t.Fatalf("address with no key exited 0")
	}
	if strings.TrimSpace(r.stdout) != "" || !strings.Contains(r.stderr, "Error loading private key") {
		t.Fatalf("stdout %q, stderr %q; want the failure on stderr only", r.stdout, r.stderr)
	}

	if first := runCLI(t, dir, nil, "generate-key"); first.code != 0 {
		t.Fatalf("generate-key: %d %s", first.code, first.output())
	}
	ok := runCLI(t, dir, nil, "address", "wallet.key")
	if ok.code != 0 || !strings.HasPrefix(strings.TrimSpace(ok.stdout), "nhb1") {
		t.Fatalf("address with a key: exit %d, stdout %q", ok.code, ok.stdout)
	}
}
