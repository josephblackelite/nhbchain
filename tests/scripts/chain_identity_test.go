package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bootstrap script must stop, with the reason, when the node it just started is not on the live network's chain, and must stay quiet
// and continue when it is. The check reads the chain id the node logs on every start ("P2P server listening"), because the obvious
// alternative does not work: net_info is an operator-only RPC method and the token nhb-cli rpc-token mints carries no operator role, so
// asking the node over RPC answers 403 and would make a healthy node look wrong. The log line below is copied from a real node started
// from config/genesis.relaunch.json.

const (
	liveChainID = "18346390202490284624"

	realStartupLine = `{"timestamp":"2026-10-08T07:44:03.6429289+04:00","severity":"INFO","message":"P2P server listening","service":"nhb","env":"prod","component":"p2p_server","listen_address":"[REDACTED]","chain_id":18346390202490284624,"genesis":"fe9b78af…c63210a2","node_id":"[REDACTED]","client_version":"nhbchain/node"}`

	// journalctl stands in for the unit's log: it prints $FAKE_JOURNAL (or nothing) and records how it was asked.
	fakeJournalctl = `#!/usr/bin/env bash
echo "journalctl $*" >>"$FAKE_JOURNAL_LOG"
if [ -n "${FAKE_JOURNAL:-}" ]; then printf '%s\n' "$FAKE_JOURNAL"; fi
`

	chainHarness = `set -euo pipefail
FAKE_BIN=$(cd "$FAKE_BIN" && pwd)
export PATH="$FAKE_BIN:$PATH"
SERVICE_USER=nhb
INSTALL_ROOT=/opt/nhbchain
CONFIG_DIR=/etc/nhbchain
GENESIS_FILE_REL='config/genesis.relaunch.json'
NETWORK_ID='` + liveChainID + `'
CHAIN_ID_RETRY_DELAY=0
source "$HELPERS"
status=0
verify_chain_identity || status=$?
echo "STATUS=${status}"
`
)

func runChainIdentity(t *testing.T, journal string) (output string, status string, journalLog string) {
	t.Helper()
	bash := bashPath(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		filepath.Join(bin, "sudo"):       fakeSudo,
		filepath.Join(bin, "journalctl"): fakeJournalctl,
		filepath.Join(dir, "helpers.sh"): validatorHelpers(t),
		filepath.Join(dir, "harness.sh"): chainHarness,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	logPath := filepath.Join(dir, "journal.log")
	env := []string{
		"FAKE_BIN=" + bashArg(bin),
		"HELPERS=" + bashArg(filepath.Join(dir, "helpers.sh")),
		"FAKE_JOURNAL=" + journal,
		"FAKE_JOURNAL_LOG=" + bashArg(logPath),
	}
	out, _ := runBash(t, bash, dir, env, bashArg(filepath.Join(dir, "harness.sh")))
	status = "?"
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "STATUS=") {
			status = strings.TrimSpace(strings.TrimPrefix(line, "STATUS="))
		}
	}
	logBytes, _ := os.ReadFile(logPath)
	return out, status, string(logBytes)
}

func TestChainIdentityAcceptsTheLiveNetworksChain(t *testing.T) {
	out, status, journalLog := runChainIdentity(t, realStartupLine)
	if status != "0" || !strings.Contains(out, "[INFO] node reports chain id "+liveChainID) {
		t.Fatalf("expected the live chain id to be accepted, status %s:\n%s", status, out)
	}
	if strings.Contains(out, "[ERROR]") {
		t.Fatalf("an error was printed for the right chain:\n%s", out)
	}
	if !strings.Contains(journalLog, "-u nhb.service") {
		t.Fatalf("expected the nhb.service log to be read:\n%s", journalLog)
	}
}

func TestChainIdentityRefusesAnotherChainAndSaysWhy(t *testing.T) {
	// the retired Phase E chain: what a node built from the old shipped config.toml announced
	other := strings.Replace(realStartupLine, liveChainID, "430060579445266314", 1)
	out, status, _ := runChainIdentity(t, other)
	if status != "1" {
		t.Fatalf("expected the script to stop (status 1), got %s:\n%s", status, out)
	}
	for _, want := range []string{
		"[ERROR] this node reports chain id '430060579445266314', expected " + liveChainID,
		"cannot join that network",
		"/opt/nhbchain/config/genesis.relaunch.json",
		"--reset-state",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in the output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[INFO] node reports chain id") {
		t.Fatalf("success was reported for the wrong chain:\n%s", out)
	}
}

func TestChainIdentityStopsWhenTheNodeNeverAnnouncesOne(t *testing.T) {
	out, status, journalLog := runChainIdentity(t, "")
	if status != "1" || !strings.Contains(out, "reports chain id 'unknown'") {
		t.Fatalf("expected the script to stop when no chain id is logged, status %s:\n%s", status, out)
	}
	if got := strings.Count(journalLog, "journalctl"); got < 5 {
		t.Fatalf("expected the node's log to be read repeatedly while it finishes starting, got %d reads:\n%s", got, journalLog)
	}
}

func TestChainIdentityUsesTheMostRecentStart(t *testing.T) {
	// an older start on the wrong chain followed by a restart on the right one: only the latest counts
	old := strings.Replace(realStartupLine, liveChainID, "430060579445266314", 1)
	out, status, _ := runChainIdentity(t, old+"\n"+realStartupLine)
	if status != "0" {
		t.Fatalf("expected the latest start to decide, status %s:\n%s", status, out)
	}
}
