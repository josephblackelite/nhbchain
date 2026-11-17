package main

// Two more end-to-end experiments on the local network of e2e_test.go. Each
// runs real nodes for many minutes and only runs when asked:
//
//	NHB_SNAPSHOT_E2E_DRIFT=1 ... -run TestFollowerWithDriftedConfigDiverges
//	NHB_SNAPSHOT_E2E_GAP=1 NHB_SNAPSHOT_E2E_GAPS=1000,5000,20000 ... -run TestSnapshotGapCatchUp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func e2eEnabledFor(t *testing.T, name string) {
	t.Helper()
	if os.Getenv(name) != "1" {
		t.Skipf("set %s=1 to run this experiment (it runs real nodes for many minutes)", name)
	}
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
}

// driftedTemplate is config.toml as it was shipped before it was corrected: the
// treasury of the network that came before this one where the live network's
// treasury belongs, and quorum-certificate verification set for a height.
func driftedTemplate(t *testing.T, dir string) string {
	t.Helper()
	raw := string(mustRead(t, filepath.Join(repoRoot(t), "config.toml")))
	swaps := [][2]string{
		{"znhb1spruw63528zhhys2zxfgu2yf5ulcrlclx6hfhn", "znhb10lephh6ffd79cc7lk6edc6rkxe9ha8xemef0ak"},
		{"nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj", "nhb10lephh6ffd79cc7lk6edc6rkxe9ha8xekt0y8h"},
	}
	for _, s := range swaps {
		if !strings.Contains(raw, s[0]) {
			t.Fatalf("config.toml does not hold %s", s[0])
		}
		raw = strings.ReplaceAll(raw, s[0], s[1])
	}
	path := filepath.Join(dir, "config.drifted.toml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A follower whose config names another treasury executes the same blocks to a
// different state and stops at the first block that touches it. This is why the
// deployment script checks the config before it starts a node.
func TestFollowerWithDriftedConfigDiverges(t *testing.T) {
	e2eEnabledFor(t, "NHB_SNAPSHOT_E2E_DRIFT")
	e := newE2EEnv(t)
	src, traffic := e.startSource(1000, 0)
	defer traffic.stop()
	snap := e.makeSnapshot(src, "drift")
	folData := filepath.Join(e.dir, "follower", "data")
	if err := os.MkdirAll(filepath.Dir(folData), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive, "--target", folData}, e.pinArgs()...)...); code != 0 {
		t.Fatalf("extract: %s", out)
	}

	// The same snapshot, the same blocks, the corrected config: no difficulty.
	good := e.newNode("follower-good", e.keys["follower"], folData, src, e2eTimeout(), "")
	good.start()
	out, code := e.runTool(append([]string{"wait-synced", "--rpc", good.rpcURL(), "--tip-rpc", src.rpcURL(), "--interval", "1s", "--timeout", "3m", "--stall-timeout", "60s"}, e.pinArgs()...)...)
	if code != 0 {
		t.Fatalf("the follower with the shipped config did not sync (%d):\n%s\n%s", code, out, good.logTail(20))
	}
	good.kill()
	if err := os.RemoveAll(folData); err != nil {
		t.Fatal(err)
	}
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive, "--target", folData}, e.pinArgs()...)...); code != 0 {
		t.Fatalf("second extract: %s", out)
	}

	// The follower with the drifted config.
	bad := e.newNode("follower-drifted", e.keys["follower"], folData, src, e2eTimeout(), driftedTemplate(t, e.dir))
	bad.start()
	out, code = e.runTool(append([]string{"wait-synced", "--rpc", bad.rpcURL(), "--tip-rpc", src.rpcURL(), "--interval", "1s", "--timeout", "3m", "--stall-timeout", "45s"}, e.pinArgs()...)...)
	t.Logf("wait-synced with the drifted config (exit %d):\n%s", code, out)
	if code == 0 {
		t.Fatalf("a follower with a drifted config kept up with the network")
	}
	mismatches := bad.logCount("state root mismatch")
	if mismatches == 0 {
		t.Fatalf("the drifted follower stopped without a state root mismatch:\n%s", bad.logTail(30))
	}
	line := ""
	for _, l := range strings.Split(string(mustRead(t, bad.logPath)), "\n") {
		if strings.Contains(l, "commit state root mismatch") {
			line = l
			break
		}
	}
	failedAt := uint64(0)
	if m := regexp.MustCompile(`"height":(\d+)`).FindStringSubmatch(line); m != nil {
		failedAt, _ = strconv.ParseUint(m[1], 10, 64)
	}
	t.Logf("the drifted follower rejected block %d (%d state root mismatches); the snapshot was at %d, the network at %d; epoch length 120, so the first epoch boundary after the snapshot is %d",
		failedAt, mismatches, snap.m.Height, src.height(), (snap.m.Height/120+1)*120)
	t.Logf("first mismatch line: %s", line)
	if failedAt != 0 && failedAt <= snap.m.Height {
		t.Fatalf("the mismatch is at height %d, not after the snapshot at %d", failedAt, snap.m.Height)
	}
}

type gapResult struct {
	Gap            uint64  `json:"gapBlocks"`
	SnapshotHeight uint64  `json:"snapshotHeight"`
	TargetHeight   uint64  `json:"targetHeight"`
	FirstAdvanceS  float64 `json:"secondsBeforeFirstBlock"`
	SyncSeconds    float64 `json:"secondsToSync"`
	BlocksPerSec   float64 `json:"blocksPerSecond"`
	SnapshotBytes  int64   `json:"snapshotArchiveBytes"`
}

// gapSnapshot is a snapshot taken from the source at some height.
type gapSnapshot struct {
	gap  uint64
	snap *sourceSnapshot
}

// buildGapChain runs the source with fast timeouts, takes snapshots at
// increasing heights (the oldest first) and stops the source at a final height.
// It returns the snapshots, largest gap first, and the stopped source.
func (e *e2eEnv) buildGapChain(gaps []uint64, timeout string) ([]gapSnapshot, *e2eNode) {
	t := e.t
	start := uint64(1500)
	final := start + gaps[0]
	src, traffic := e.startSourceWith(start, 0, timeout)
	var snaps []gapSnapshot
	for _, gap := range gaps {
		at := final - gap
		e.waitUntil(fmt.Sprintf("the source to reach height %d", at), 60*time.Minute, src, func() bool { return src.height() >= at })
		snaps = append(snaps, gapSnapshot{gap, e.makeSnapshot(src, fmt.Sprintf("gap%d", gap))})
	}
	e.waitUntil(fmt.Sprintf("the source to reach height %d", final), 60*time.Minute, src, func() bool { return src.height() >= final })
	t.Logf("driver sent %d transactions", traffic.stop())
	src.kill()
	return snaps, src
}

// loadGapChain finds the snapshots and the stopped source an earlier run of
// TestSnapshotGapCatchUp left in the directory.
func (e *e2eEnv) loadGapChain() ([]gapSnapshot, *e2eNode) {
	t := e.t
	dirs, err := filepath.Glob(filepath.Join(e.dir, "snapshots-gap*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no snapshots-gap* directories in %s to reuse", e.dir)
	}
	var snaps []gapSnapshot
	for _, dir := range dirs {
		gap, err := strconv.ParseUint(strings.TrimPrefix(filepath.Base(dir), "snapshots-gap"), 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		m, err := readManifestFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, gapSnapshot{gap, &sourceSnapshot{m: m, dir: dir,
			manifest: filepath.Join(dir, "manifest.json"), archive: filepath.Join(dir, m.Archive.Name)}})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].gap > snaps[j].gap })
	src := &e2eNode{env: e, name: "source", dataDir: filepath.Join(e.dir, "source", "data")}
	return snaps, src
}

// TestSnapshotGapCatchUp measures how many blocks a follower can catch up by
// block requests, from snapshots of different ages.
//
// The source runs (fast timeouts, the usual traffic) while snapshots are taken
// at increasing heights, then stops at a final height. The validator is then
// started again from a copy of its database with the shipped consensus
// timeouts, so it produces a block every few seconds as the live validators do,
// and serves the blocks. Each follower is started from a different snapshot,
// also with the shipped timeouts, and timed from its first block to the final
// height. The chain of a live network grows far more slowly than a follower
// syncs, so what matters for it is how long such a catch-up takes.
//
// NHB_SNAPSHOT_E2E_REUSE=1 with NHB_SNAPSHOT_E2E_DIR set to the directory of an
// earlier run measures again on the chain and snapshots that run left.
func TestSnapshotGapCatchUp(t *testing.T) {
	e2eEnabledFor(t, "NHB_SNAPSHOT_E2E_GAP")
	gapsText := os.Getenv("NHB_SNAPSHOT_E2E_GAPS")
	if gapsText == "" {
		gapsText = "1000,5000,20000"
	}
	var gaps []uint64
	for _, g := range strings.Split(gapsText, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(g), 10, 64)
		if err != nil || v == 0 {
			t.Fatalf("NHB_SNAPSHOT_E2E_GAPS: %q", g)
		}
		gaps = append(gaps, v)
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] > gaps[j] })
	fast := os.Getenv("NHB_SNAPSHOT_E2E_TIMEOUT")
	if fast == "" {
		fast = "5ms"
	}
	e := newE2EEnv(t)

	var snaps []gapSnapshot
	var src *e2eNode
	if os.Getenv("NHB_SNAPSHOT_E2E_REUSE") == "1" {
		snaps, src = e.loadGapChain()
	} else {
		snaps, src = e.buildGapChain(gaps, fast)
	}

	// The chain no longer grows. A node with another key serves it.
	srcID, err := openAndReadIdentity(src.dataDir, checkOptions{})
	if err != nil {
		t.Fatalf("the stopped source's database: %v", err)
	}
	target := srcID.Height
	serverData := filepath.Join(e.dir, "server", "data")
	if err := os.RemoveAll(filepath.Dir(serverData)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(serverData, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range chainFiles(t, src.dataDir) {
		if allowedFileName(name) {
			if err := os.WriteFile(filepath.Join(serverData, name), mustRead(t, filepath.Join(src.dataDir, name)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The serving node is the validator again, from a copy of its database and
	// with the shipped consensus timeouts, so it produces a block every few
	// seconds as the live validators do: what a follower's requests are answered
	// by, and what makes it ask again after a request was not answered.
	server := e.newNode("server", e.keys["validator"], serverData, nil, "0s", "")
	server.start()
	e.waitUntil("the serving node's RPC", 60*time.Second, server, func() bool { return server.info() != nil })
	if h := server.height(); h < target {
		t.Fatalf("the serving node is at %d, the stopped source was at %d", h, target)
	}
	var heights []uint64
	for _, s := range snaps {
		heights = append(heights, s.snap.m.Height)
	}
	t.Logf("the network is frozen at height %d; snapshots at %v", target, heights)

	var results []gapResult
	var followerDirs []string
	for i := len(snaps) - 1; i >= 0; i-- { // smallest gap first
		s := snaps[i]
		dir := filepath.Join(e.dir, fmt.Sprintf("follower-gap%d", s.gap), "data")
		if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, code := e.runTool(append([]string{"extract", "--manifest", s.snap.manifest, "--archive", s.snap.archive, "--target", dir}, e.pinArgs()...)...); code != 0 {
			t.Fatalf("extract gap %d: %s", s.gap, out)
		}
		// "0s" is what the shipped config.toml says: the node's own default timeouts.
		f := e.newNode(fmt.Sprintf("follower-gap%d", s.gap), e.keys["follower"], dir, server, "0s", "")
		began := time.Now()
		f.start()
		var firstAdvance, done time.Time
		deadline := time.Now().Add(60 * time.Minute)
		for time.Now().Before(deadline) {
			h := f.height()
			if firstAdvance.IsZero() && h > s.snap.m.Height {
				firstAdvance = time.Now()
			}
			if h >= target {
				done = time.Now()
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if done.IsZero() {
			f.kill()
			t.Fatalf("the follower from the snapshot at %d (gap %d) did not reach %d in an hour (at %d)\n%s", s.snap.m.Height, target-s.snap.m.Height, target, f.height(), f.logTail(15))
		}
		gap := target - s.snap.m.Height
		res := gapResult{
			Gap: gap, SnapshotHeight: s.snap.m.Height, TargetHeight: target,
			FirstAdvanceS: firstAdvance.Sub(began).Seconds(), SyncSeconds: done.Sub(firstAdvance).Seconds(),
			SnapshotBytes: s.snap.m.Archive.Size,
		}
		if res.SyncSeconds > 0 {
			res.BlocksPerSec = float64(gap) / res.SyncSeconds
		}
		results = append(results, res)
		t.Logf("gap %d blocks (snapshot %d -> %d): first block after %.1fs, synced in %.1fs = %.1f blocks/s", gap, s.snap.m.Height, target, res.FirstAdvanceS, res.SyncSeconds, res.BlocksPerSec)
		f.kill()
		followerDirs = append(followerDirs, dir)
	}

	t.Logf("the serving node dropped over-budget block requests in %d one-second windows and is now at height %d", server.logCount("Dropping chain data requests"), server.height())
	server.kill()
	for _, dir := range followerDirs {
		if _, b := compareChains(t, serverData, dir, 0); b < target {
			t.Fatalf("follower %s ended at %d, want at least %d", dir, b, target)
		}
	}
	encoded, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile(filepath.Join(e.dir, "gap-results.json"), encoded, 0o644)
	t.Logf("results:\n%s", encoded)
}
