package hygiene

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Code that changes state while a block executes must take its time from the
// block, never from the machine's clock: two validators executing the same block at
// different moments would otherwise disagree (PL-R5T-89, PL-DC-40, PL-DC-55).
//
// This is a ratchet over the packages that hold or are called by state-transition
// code: core and native. Every non-comment time.Now( call in a non-test file is
// counted per file. The counts below are the reviewed baseline on the day this test
// was written: they include default clocks that the state processor replaces with
// the block clock, and node-local paths that no block reaches (the retired RPC
// methods, snapshot writing, the oracle HTTP client). A NEW call fails the test, so
// it gets looked at; a call that is removed makes the test ask for the baseline to be
// lowered, so the number only goes down.
var wallClockBaseline = map[string]int{
	"core/node.go":                      20,
	"core/pricing/pricefeed.go":         1,
	"core/proposal_containment.go":      1,
	"core/state/loyalty_engine.go":      3,
	"core/state/manager.go":             7,
	"core/state/staking_rewards.go":     1,
	"core/state_transition.go":          1,
	"core/sync/snapshot_writer.go":      1,
	"native/creator/engine.go":          3,
	"native/escrow/engine.go":           3,
	"native/escrow/engine_milestone.go": 1,
	"native/escrow/trade_engine.go":     3,
	"native/governance/engine.go":       3,
	"native/market/engine.go":           3,
	"native/pos/auth.go":                3,
	"native/reputation/storage.go":      4,
	"native/swap/engine.go":             1,
	"native/swap/oracle.go":             6,
}

var timeNowCall = regexp.MustCompile(`\btime\.Now\(`)

func countWallClockReads(t *testing.T) map[string]int {
	t.Helper()
	root := repoRoot(t)
	got := map[string]int{}
	for _, dir := range []string{"core", "native"} {
		for _, path := range repoFileList(t, root, dir) {
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			n := 0
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				code, _, _ := strings.Cut(line, "//")
				n += len(timeNowCall.FindAllString(code, -1))
			}
			if n > 0 {
				rel, _ := filepath.Rel(root, path)
				got[filepath.ToSlash(rel)] = n
			}
		}
	}
	return got
}

func TestNoNewWallClockReadsInStateTransitionPackages(t *testing.T) {
	got := countWallClockReads(t)
	var files []string
	for f := range got {
		files = append(files, f)
	}
	for f := range wallClockBaseline {
		if _, ok := got[f]; !ok {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		have, want := got[f], wallClockBaseline[f]
		switch {
		case have > want:
			t.Errorf("%s reads the machine's clock %d time(s), the reviewed baseline is %d: take the time from the block (the state processor's clock) or, if this path never runs inside block execution, raise the baseline in tests/hygiene/wallclock_test.go with a reason in the commit", f, have, want)
		case have < want:
			t.Errorf("%s now reads the machine's clock %d time(s), below the baseline %d: lower the baseline in tests/hygiene/wallclock_test.go so the number only goes down", f, have, want)
		}
	}
}
