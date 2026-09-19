package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	helpersBegin = "# --- begin validator CLI helpers (exercised by tests/scripts) ---\n"
	helpersEnd   = "# --- end validator CLI helpers ---\n"

	// fakeSudo runs the command the way sudo would for the service user.
	fakeSudo = `#!/usr/bin/env bash
if [ "$1" = "-u" ]; then shift 2; fi
exec "$@"
`

	// fakeCLI follows the real nhb-cli's contract for the commands the script
	// uses: rpc-token needs the node's secret on stdin, and the two commands
	// that submit a transaction refuse to run without NHB_RPC_TOKEN
	// (cmd/nhb-cli/main.go, doRPCRequest). Only arguments are logged, never
	// the secret.
	fakeCLI = `#!/usr/bin/env bash
echo "args: $*" >>"$FAKE_CLI_LOG"
case "$1" in
  rpc-token)
    secret=$(cat)
    if [ "$secret" != "$FAKE_NODE_SECRET" ]; then
      echo "Error: bad secret" >&2
      exit 1
    fi
    echo "fake.jwt.token"
    ;;
  set-reward-beneficiary|register-validator)
    echo "env: token=${NHB_RPC_TOKEN:-} url=${RPC_URL:-}" >>"$FAKE_CLI_LOG"
    if [ "${NHB_RPC_TOKEN:-}" != "fake.jwt.token" ]; then
      echo "Error sending transaction: privileged RPC call requires NHB_RPC_TOKEN to be set"
      exit 1
    fi
    if [ "$1" = "${FAKE_FAIL_CMD:-}" ]; then
      echo "Error sending transaction: error from node: rejected"
      exit 1
    fi
    echo "Submitted $1"
    ;;
esac
`

	harness = `set -euo pipefail
FAKE_BIN=$(cd "$FAKE_BIN" && pwd)
export PATH="$FAKE_BIN:$PATH"
SERVICE_USER=nhb
INSTALL_ROOT=$(cd "$FAKE_INSTALL" && pwd)
CONFIG_DIR=/etc/nhbchain
RPC_ADDR=127.0.0.1:8545
VALIDATOR_KEY_FILE=/etc/nhbchain/validator.key
BENEFICIARY=nhb1exampleaddress
JWT_SECRET=the-node-secret
CLI_RETRY_DELAY=0
source "$HELPERS"
RPC_TOKEN=$(mint_rpc_token) || { echo "MINT FAILED"; exit 3; }
status=0
submit_validator_steps || status=$?
echo "STATUS=${status}"
`
)

// validatorHelpers returns the helper functions scripts/deployvalidator.sh
// runs after the node's RPC is up, exactly as written in the script.
func validatorHelpers(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(repoFile(t, "scripts/deployvalidator.sh"))
	if err != nil {
		t.Fatalf("read deployvalidator.sh: %v", err)
	}
	script := string(raw)
	begin := strings.Index(script, helpersBegin)
	end := strings.Index(script, helpersEnd)
	if begin < 0 || end < begin {
		t.Fatalf("scripts/deployvalidator.sh has no validator CLI helper block")
	}
	return script[begin+len(helpersBegin) : end]
}

type harnessRun struct {
	output string
	code   int
	status int
	log    string
}

func runHarness(t *testing.T, failCmd, nodeSecret string) harnessRun {
	t.Helper()
	bash := bashPath(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	install := filepath.Join(dir, "install")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		filepath.Join(bin, "sudo"):               fakeSudo,
		filepath.Join(install, "bin", "nhb-cli"): fakeCLI,
		filepath.Join(dir, "helpers.sh"):         validatorHelpers(t),
		filepath.Join(dir, "harness.sh"):         harness,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	logPath := filepath.Join(dir, "cli.log")
	env := []string{
		"FAKE_BIN=" + bashArg(bin),
		"FAKE_INSTALL=" + bashArg(install),
		"HELPERS=" + bashArg(filepath.Join(dir, "helpers.sh")),
		"FAKE_CLI_LOG=" + bashArg(logPath),
		"FAKE_NODE_SECRET=" + nodeSecret,
		"FAKE_FAIL_CMD=" + failCmd,
	}
	out, code := runBash(t, bash, dir, env, bashArg(filepath.Join(dir, "harness.sh")))
	logBytes, _ := os.ReadFile(logPath)
	status := -1
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "STATUS=") {
			status = int(line[len("STATUS=")] - '0')
		}
	}
	return harnessRun{output: out, code: code, status: status, log: string(logBytes)}
}

// DC-17: the registration steps used to run nhb-cli without NHB_RPC_TOKEN,
// which the CLI refuses, so both always failed.
func TestValidatorStepsPassTheLocalRPCToken(t *testing.T) {
	run := runHarness(t, "", "the-node-secret")
	if run.code != 0 || run.status != 0 {
		t.Fatalf("expected both steps to succeed, status %d:\n%s\n%s", run.status, run.output, run.log)
	}
	for _, want := range []string{
		"args: rpc-token --secret-stdin --ttl 10m",
		"args: set-reward-beneficiary nhb1exampleaddress /etc/nhbchain/validator.key",
		"args: register-validator 0 /etc/nhbchain/validator.key",
	} {
		if !strings.Contains(run.log, want) {
			t.Fatalf("expected %q in the CLI log:\n%s", want, run.log)
		}
	}
	if got := strings.Count(run.log, "env: token=fake.jwt.token url=http://127.0.0.1:8545"); got != 2 {
		t.Fatalf("expected both submissions to carry the token and the node's RPC URL, got %d:\n%s", got, run.log)
	}
	if strings.Contains(run.output, "the-node-secret") || strings.Contains(run.output, "fake.jwt.token") {
		t.Fatalf("the secret or the token was printed:\n%s", run.output)
	}
	if strings.Contains(run.log, "the-node-secret") {
		t.Fatalf("the secret reached a command line:\n%s", run.log)
	}
}

// A failed step must be reported as a failure, with the way to run it again,
// and never followed by a success message.
func TestValidatorStepFailureIsReportedNotHidden(t *testing.T) {
	run := runHarness(t, "register-validator", "the-node-secret")
	if run.code != 0 || run.status != 1 {
		t.Fatalf("expected the steps to report failure, status %d:\n%s", run.status, run.output)
	}
	for _, want := range []string{
		"[ERROR] The node is running, but these steps did not complete:",
		"nhb-cli register-validator 0 /etc/nhbchain/validator.key",
		"rpc-token --secret-stdin",
		`NHB_RPC_TOKEN="$TOKEN"`,
	} {
		if !strings.Contains(run.output, want) {
			t.Fatalf("expected %q in the output:\n%s", want, run.output)
		}
	}
	if strings.Contains(run.output, "  nhb-cli set-reward-beneficiary") {
		t.Fatalf("the step that succeeded was reported as failed:\n%s", run.output)
	}
	if strings.Contains(run.output, "already set automatically") || strings.Contains(run.output, "[OK]") {
		t.Fatalf("success was reported after a failed step:\n%s", run.output)
	}
	if got := strings.Count(run.log, "args: register-validator 0"); got != 3 {
		t.Fatalf("expected the failing step to be attempted three times, got %d:\n%s", got, run.log)
	}
	if strings.Contains(run.output, "the-node-secret") || strings.Contains(run.output, "fake.jwt.token") {
		t.Fatalf("the secret or the token was printed:\n%s", run.output)
	}
}

// Without a token nothing is submitted: a secret the CLI cannot mint from
// stops the script before any step runs. (The fake CLI only mints a token for
// its own secret, so a different one models a secret the script does not know.)
func TestValidatorStepsStopWhenNoTokenCanBeMinted(t *testing.T) {
	run := runHarness(t, "", "a-different-secret")
	if run.code != 3 || !strings.Contains(run.output, "MINT FAILED") {
		t.Fatalf("expected the token step to fail (exit %d):\n%s", run.code, run.output)
	}
	if strings.Contains(run.log, "args: set-reward-beneficiary") || strings.Contains(run.log, "args: register-validator") {
		t.Fatalf("a step ran without a token:\n%s", run.log)
	}
}
