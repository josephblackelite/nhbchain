package main

// Tests of the check from outside that scripts/deployvalidator.sh asks for before
// it installs a snapshot. A snapshot is not signed by anyone and the bootnode is
// the only peer a new node starts with, so a run needs a reference node of the
// operator's choosing, or the block the snapshot must end at, or an explicit
// waiver. Like the other deploy tests these source the script, or run its main()
// with the steps that need a real server stubbed.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The rules for what counts as a check from outside, one case each.
func TestDeployTrustAnchorRules(t *testing.T) {
	h := newDeployHarness(t)
	pin := "0x" + strings.Repeat("ab", 32)
	const (
		snapshot = "https://snapshots.example.invalid/nhb"
		bootnode = "peer.example.invalid:6001"
	)
	cases := []struct {
		name     string
		snapshot string
		bootnode string
		extra    []string
		mode     string
		wantOK   bool
		want     string // in the output
		notWant  string // not in the output
		marks    string // what the run remembers for its closing message, when it went on
	}{
		{name: "nothing", mode: "install", want: "no reference node was given"},
		{name: "a reference node over https", extra: []string{"--tip-rpc", "https://rpc.example.invalid"}, mode: "install", wantOK: true, notWant: "WARN", marks: "unanchored=0 pinned_only=0"},
		{name: "a reference node over plain http", extra: []string{"--tip-rpc", "http://rpc.example.invalid"}, mode: "install", want: "is not https"},
		{name: "plain http that the operator accepted", extra: []string{"--tip-rpc", "http://rpc.example.invalid", "--allow-insecure-http"}, mode: "install", wantOK: true},
		{name: "a reference on the snapshot's host", extra: []string{"--tip-rpc", "https://snapshots.example.invalid:8545"}, mode: "install", want: "where the snapshot comes from"},
		{name: "the same host written another way", extra: []string{"--tip-rpc", "https://SNAPSHOTS.Example.Invalid/rpc"}, mode: "install", want: "where the snapshot comes from"},
		{name: "a snapshot url with a user and a port", snapshot: "https://user@snapshots.example.invalid:8443/nhb", extra: []string{"--tip-rpc", "https://snapshots.example.invalid"}, mode: "install", want: "where the snapshot comes from"},
		{name: "a reference on the bootnode's host", extra: []string{"--tip-rpc", "https://peer.example.invalid:8545"}, mode: "install", want: "is the bootnode"},
		{name: "a reference whose name only starts like the snapshot's host", extra: []string{"--tip-rpc", "https://snapshots.example.invalid.other.invalid"}, mode: "install", wantOK: true},
		// The pins show where the snapshot ends and nothing above it: the run goes
		// on, says that the blocks after the snapshot are not checked, and remembers
		// it for its closing message.
		{name: "the tip hash and the state root", extra: []string{"--tip-hash", pin, "--state-root", pin}, mode: "install", wantOK: true, want: "check where the snapshot ends, not the blocks this node applies after it", marks: "unanchored=0 pinned_only=1"},
		{name: "the pins and a good reference", extra: []string{"--tip-rpc", "https://rpc.example.invalid", "--tip-hash", pin, "--state-root", pin}, mode: "install", wantOK: true, notWant: "WARN", marks: "unanchored=0 pinned_only=0"},
		{name: "the pins and the waiver", extra: []string{"--tip-hash", pin, "--state-root", pin, "--allow-unanchored"}, mode: "install", wantOK: true, want: "check where the snapshot ends", marks: "unanchored=0 pinned_only=1"},
		{name: "the tip hash alone", extra: []string{"--tip-hash", pin}, mode: "install", want: "no reference node was given"},
		{name: "the state root alone", extra: []string{"--state-root", pin}, mode: "install", want: "no reference node was given"},
		// A reference that cannot count is no check on the blocks after the snapshot
		// either, and the warning says why it does not count.
		{name: "an unusable reference and the pins", extra: []string{"--tip-rpc", "http://rpc.example.invalid", "--tip-hash", pin, "--state-root", pin}, mode: "install", wantOK: true, want: "is not https", marks: "unanchored=0 pinned_only=1"},
		{name: "a reference on the bootnode's host and the pins", extra: []string{"--tip-rpc", "https://peer.example.invalid", "--tip-hash", pin, "--state-root", pin}, mode: "install", wantOK: true, want: "is the bootnode", marks: "unanchored=0 pinned_only=1"},
		{name: "an unusable reference alone", extra: []string{"--tip-rpc", "https://peer.example.invalid"}, mode: "install", want: "is the bootnode"},
		{name: "the waiver", extra: []string{"--allow-unanchored"}, mode: "install", wantOK: true, want: "no reference node was given", marks: "unanchored=1 pinned_only=0"},
		{name: "the waiver, with a reference that cannot count", extra: []string{"--tip-rpc", "https://peer.example.invalid", "--allow-unanchored"}, mode: "install", wantOK: true, want: "is the bootnode", marks: "unanchored=1 pinned_only=0"},
		{name: "a good reference needs no waiver and none is warned about", extra: []string{"--tip-rpc", "https://rpc.example.invalid", "--allow-unanchored"}, mode: "install", wantOK: true, notWant: "WARN", marks: "unanchored=0 pinned_only=0"},
		// An earlier run's snapshot, not yet seen past its height, is not checked
		// against anything now: the pins have no snapshot to be checked against.
		{name: "a recovered snapshot and the pins", extra: []string{"--tip-hash", pin, "--state-root", pin}, mode: "recover", want: "still has to prove it"},
		{name: "a recovered snapshot and a reference", extra: []string{"--tip-rpc", "https://rpc.example.invalid"}, mode: "recover", wantOK: true, marks: "unanchored=0 pinned_only=0"},
		{name: "a recovered snapshot and the waiver", extra: []string{"--allow-unanchored"}, mode: "recover", wantOK: true, marks: "unanchored=1 pinned_only=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, boot := tc.snapshot, tc.bootnode
			if snap == "" {
				snap = snapshot
			}
			if boot == "" {
				boot = bootnode
			}
			args := append([]string{"--beneficiary", goodBeneficiary, "--snapshot-url", snap, "--bootnode", boot}, tc.extra...)
			out, code := h.run("parse_args "+strings.Join(args, " ")+"\nvalidate_inputs\nrequire_trust_anchor "+tc.mode+"\necho \"went on unanchored=${UNANCHORED_RUN} pinned_only=${PINNED_ONLY_RUN}\"",
				"NHB_SNAPSHOT_URL=", "NHB_BOOTNODE=", "NHB_TIP_RPC_URL=")
			if tc.wantOK && (code != 0 || !strings.Contains(out, "went on")) {
				t.Fatalf("refused (exit %d):\n%s", code, out)
			}
			if tc.wantOK && tc.marks != "" && !strings.Contains(out, "went on "+tc.marks) {
				t.Fatalf("the run should remember %q for its closing message:\n%s", tc.marks, out)
			}
			if !tc.wantOK && (code == 0 || strings.Contains(out, "went on")) {
				t.Fatalf("accepted (exit %d):\n%s", code, out)
			}
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("expected %q in the output:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Fatalf("did not expect %q in the output:\n%s", tc.notWant, out)
			}
		})
	}

	t.Run("the waiver is remembered for the closing message", func(t *testing.T) {
		out, code := h.run("parse_args --beneficiary " + goodBeneficiary + " --snapshot-url " + snapshot + " --bootnode " + bootnode + " --allow-unanchored\nvalidate_inputs\nrequire_trust_anchor install\necho \"unanchored=${UNANCHORED_RUN}\"\nVALIDATOR_ADDRESS=nhb1x\nprint_next_steps")
		if code != 0 || !strings.Contains(out, "unanchored=1") || !strings.Contains(out, "[WARN] --allow-unanchored was given") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		out, code = h.run("UNANCHORED_RUN=0\nPINNED_ONLY_RUN=0\nVALIDATOR_ADDRESS=nhb1x\nprint_next_steps")
		if code != 0 || strings.Contains(out, "--allow-unanchored") || strings.Contains(out, "No reference node") {
			t.Fatalf("a run with a check from outside was warned about a waiver (exit %d):\n%s", code, out)
		}
	})

	// The pins say where the snapshot ends. A run that has only them is not told
	// that its node was checked against the network: the closing message says that
	// the blocks after the snapshot rest on the bootnode, and it does not call that
	// a waiver the operator gave.
	t.Run("a run with only the pins says so in the closing message", func(t *testing.T) {
		out, code := h.run("parse_args --beneficiary " + goodBeneficiary + " --snapshot-url " + snapshot + " --bootnode " + bootnode + " --tip-hash " + pin + " --state-root " + pin + "\nvalidate_inputs\nrequire_trust_anchor install\nVALIDATOR_ADDRESS=nhb1x\nprint_next_steps")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		closing := out[strings.Index(out, "[OK] Validator node is running"):]
		for _, want := range []string{
			"[WARN] No reference node (--tip-rpc) checked this node",
			"showed where the snapshot ends",
			"blocks the node has applied since come from the bootnode alone",
			"before you stake to it",
		} {
			if !strings.Contains(strings.Join(strings.Fields(closing), " "), want) {
				t.Fatalf("the closing message does not say %q:\n%s", want, closing)
			}
		}
		if strings.Contains(closing, "--allow-unanchored") {
			t.Fatalf("the closing message of a run that gave no waiver mentions one:\n%s", closing)
		}
	})
}

// A run that has nothing from outside to check the snapshot against stops before
// it installs, builds, downloads or fetches anything, and says what to give it.
func TestDeployRefusesARunWithNoCheckFromOutsideBeforeInstallingAnything(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	m := p.publish(h.stubNodeBinaries(), rerunCommit)
	plain := anchoredMainSnippet(p.server.URL)
	marked := strings.Replace(plain, "install_prerequisites(){ :; }", `install_prerequisites(){ echo "INSTALL-PREREQUISITES-RAN"; }`, 1)
	if marked == plain {
		t.Fatal("the stand-in for the installation was not replaced: this test does not test what it says")
	}
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"no flag about it", nil},
		{"the tip hash alone", []string{"--tip-hash", m.TipHash}},
		{"the state root alone", []string{"--state-root", m.StateRoot}},
		// The snapshot and the bootnode of this run are both on the loopback host.
		{"a reference on the host of the snapshot and the bootnode", []string{"--tip-rpc", "https://127.0.0.1:8545"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(h.log)
			snippet := strings.Replace(marked, "--allow-insecure-http", "--allow-insecure-http "+strings.Join(tc.extra, " "), 1)
			out, code := h.run(snippet)
			if code == 0 {
				t.Fatalf("the run went on:\n%s", lastLines(out))
			}
			for _, want := range []string{"no check from outside on the snapshot", "--tip-rpc", "--tip-hash", "--allow-unanchored", "snapshot-onboarding.md"} {
				if !strings.Contains(out, want) {
					t.Fatalf("the refusal does not say %q:\n%s", want, lastLines(out))
				}
			}
			if strings.Contains(out, "INSTALL-PREREQUISITES-RAN") {
				t.Fatalf("the run installed packages before it asked for the check:\n%s", lastLines(out))
			}
			if strings.Contains(h.calls(), "curl") {
				t.Fatalf("the run went to the snapshot host:\n%s", h.calls())
			}
			if entries, _ := os.ReadDir(filepath.Join(h.state, "nhb-data")); len(entries) > 0 {
				t.Fatalf("the refused run left %d files in the data directory", len(entries))
			}
			if _, err := os.Stat(filepath.Join(h.state, ".snapshot-download")); err == nil {
				t.Fatalf("the refused run made a download directory")
			}
		})
	}
}

// Each kind of check from outside lets the run go on and install the snapshot. The
// waiver says so on the way and at the end, and so does a run that has only the
// pins: they show where the snapshot ends, not that the blocks after it are the
// network's, so such a run is not left to report the node at the tip as if a
// reference node had checked it.
func TestDeployGoesAheadWithACheckFromOutside(t *testing.T) {
	p := newLivePublisher(t)
	// Every harness stubs the node binary with the same bytes, so one snapshot
	// made for that binary serves them all.
	m := p.publish(newDeployHarness(t).stubNodeBinaries(), rerunCommit)
	for _, tc := range []struct {
		name      string
		extra     []string
		unchecked bool
		pinsAlone bool
	}{
		{"a reference node", []string{"--tip-rpc", "https://rpc.example.invalid"}, false, false},
		{"the tip hash and the state root", []string{"--tip-hash", m.TipHash, "--state-root", m.StateRoot}, false, true},
		{"a reference node and the pins", []string{"--tip-rpc", "https://rpc.example.invalid", "--tip-hash", m.TipHash, "--state-root", m.StateRoot}, false, false},
		{"the waiver", []string{"--allow-unanchored"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDeployHarness(t)
			h.stubNodeBinaries()
			out, code := h.run(anchoredMainSnippet(p.server.URL, tc.extra...))
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, lastLines(out))
			}
			if len(chainFiles(t, filepath.Join(h.state, "nhb-data"))) == 0 {
				t.Fatalf("the snapshot was not installed:\n%s", lastLines(out))
			}
			warned := strings.Contains(out, "--allow-unanchored")
			if warned != tc.unchecked {
				t.Fatalf("warned about a waiver: %v, waiver given: %v\n%s", warned, tc.unchecked, lastLines(out))
			}
			if tc.unchecked && (!strings.Contains(out, "[WARN] --allow-unanchored was given") || !strings.Contains(out, "reported at the network tip on their word alone")) {
				t.Fatalf("the waiver was not repeated at the end:\n%s", lastLines(out))
			}
			// The pins alone: warned on the way, and again at the end.
			text := strings.Join(strings.Fields(out), " ")
			for _, want := range []string{
				"check where the snapshot ends, not the blocks this node applies after it",
				"[WARN] No reference node (--tip-rpc) checked this node",
			} {
				if got := strings.Contains(text, want); got != tc.pinsAlone {
					t.Fatalf("%q in the output: %v, a run with only the pins: %v\n%s", want, got, tc.pinsAlone, lastLines(out))
				}
			}
		})
	}
}

// A node that already holds the chain is given no snapshot, so it has nothing to
// be checked against and asks for no flag. A run that finds the record of an
// earlier run's snapshot, which that run never saw the node get past, still has
// the snapshot to prove and asks for a reference node (or the waiver).
func TestDeployRerunAsksForTheCheckOnlyWhileASnapshotIsUnproven(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	m := p.publish(h.stubNodeBinaries(), rerunCommit)
	marker := filepath.Join(h.state, ".snapshot-height")
	height := strconv.FormatUint(m.Height, 10)
	interrupted := strings.Replace(mainSnippet(p.server.URL), `wait_until_synced(){ echo "wait_until_synced: snapshot height [${SNAPSHOT_HEIGHT:-}]"; forget_snapshot_height; }`,
		`wait_until_synced(){ echo "wait_until_synced: snapshot height [${SNAPSHOT_HEIGHT:-}]"; echo interrupted; exit 1; }`, 1)
	if interrupted == mainSnippet(p.server.URL) {
		t.Fatal("the stand-in for the wait was not replaced: this test does not test what it says")
	}
	if out, code := h.run(interrupted); code != 1 || !strings.Contains(out, "interrupted") {
		t.Fatalf("the interrupted run (exit %d):\n%s", code, lastLines(out))
	}
	if got := strings.TrimSpace(string(mustRead(t, marker))); got != height {
		t.Fatalf("the run recorded %q, the snapshot's height is %s", got, height)
	}

	rerun := func(extra ...string) (string, int) {
		_ = os.Remove(h.log)
		return h.run(anchoredMainSnippet(p.server.URL, extra...), "FAKE_ACTIVE=0")
	}
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"no flag about it", nil},
		{"the pins, which have no snapshot to be checked against now", []string{"--tip-hash", m.TipHash, "--state-root", m.StateRoot}},
	} {
		out, code := rerun(tc.extra...)
		if code == 0 || !strings.Contains(out, "still has to prove it") || !strings.Contains(out, "--tip-rpc") {
			t.Fatalf("a run that still has an unproven snapshot went on with %s (exit %d):\n%s", tc.name, code, lastLines(out))
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the record of the snapshot was dropped by a refused run: %v", err)
		}
	}
	out, code := rerun("--tip-rpc", "https://rpc.example.invalid")
	if code != 0 || !strings.Contains(out, "snapshot height ["+height+"]") {
		t.Fatalf("a run with a reference node did not go on (exit %d):\n%s", code, lastLines(out))
	}
	if strings.Contains(h.calls(), "curl") {
		t.Fatalf("a run that installs no snapshot went to the snapshot host:\n%s", h.calls())
	}
	// The node has been seen past the height: nothing is left to prove.
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the record was kept after the node had been seen at the tip")
	}
	out, code = rerun()
	if code != 0 || strings.Contains(out, "no check from outside") || strings.Contains(out, "still has to prove it") || strings.Contains(h.calls(), "curl") {
		t.Fatalf("a run on a node that holds the chain asked for the check (exit %d):\n%s", code, lastLines(out))
	}
}

// The wait for the tip is told whether it has a reference: with --tip-rpc it gets
// it, and without one it is told that on purpose.
func TestDeployWaitUntilSyncedSaysWhetherItHasAReference(t *testing.T) {
	h := newDeployHarness(t)
	fakeTools := filepath.Join(h.root, "faketools")
	if err := os.MkdirAll(fakeTools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeTools, "nhb-snapshot"), []byte("#!/usr/bin/env bash\necho \"TOOL: $*\" >> \"$FAKE_LOG\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		tipRPC  string
		want    string
		notWant string
	}{
		{"with a reference", "https://rpc.example.invalid", "--tip-rpc https://rpc.example.invalid", "--no-reference"},
		{"without one", "", "--no-reference", "--tip-rpc"},
	} {
		_ = os.Remove(h.log)
		out, code := h.run("TOOL_DIR=" + slash(fakeTools) + "\nTIP_RPC='" + tc.tipRPC + "'\nwait_until_synced")
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", tc.name, code, out)
		}
		var call string
		for _, line := range strings.Split(h.calls(), "\n") {
			if strings.HasPrefix(line, "TOOL: wait-synced") {
				call = line
			}
		}
		if call == "" || !strings.Contains(call, tc.want) || strings.Contains(call, tc.notWant) {
			t.Fatalf("%s: wait-synced was run as %q, expected %q and not %q", tc.name, call, tc.want, tc.notWant)
		}
	}
}
