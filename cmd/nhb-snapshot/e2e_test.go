package main

// End-to-end test of snapshot onboarding on a local network.
//
// A single validator is started from the shipped genesis (with its own
// validator address), run for a few thousand blocks under a steady mix of
// transactions, and crashed and restarted twice so its database holds table
// files as well as a journal, like a long-running node's. A snapshot is made
// from the RUNNING node with scripts/make-snapshot.sh. A second node with a
// different key, different ports and a fresh data directory is started from
// that snapshot with nhb-snapshot extract, as a follower, and must catch up
// with the network, follow it for as long as the test runs, survive being
// crashed and restarted, and hold exactly the source's block hash and state
// root at every height the two have in common.
//
// It runs real processes for minutes and needs bash, so it only runs when asked:
//
//	NHB_SNAPSHOT_E2E=1 NHB_TEST_BASH="C:/Program Files/Git/bin/bash.exe" \
//	    go test ./cmd/nhb-snapshot -run TestSnapshotOnboardingEndToEnd -v -timeout 30m

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

func e2eEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("NHB_SNAPSHOT_E2E") != "1" {
		t.Skip("set NHB_SNAPSHOT_E2E=1 to run the end-to-end test (it runs real nodes for several minutes)")
	}
	if testing.Short() {
		t.Skip("skipping the end-to-end test in short mode")
	}
}

func e2eTimeout() string {
	if v := strings.TrimSpace(os.Getenv("NHB_SNAPSHOT_E2E_TIMEOUT")); v != "" {
		return v
	}
	return "40ms"
}

// runScript runs a script of the repository with bash and returns its output.
func (e *e2eEnv) runScript(script string, args ...string) (string, error) {
	full := append([]string{slash(filepath.Join(e.repo, script))}, args...)
	cmd := exec.Command(e.bash, full...)
	cmd.Dir = e.repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runTool runs the built nhb-snapshot and returns its output and exit code.
func (e *e2eEnv) runTool(args ...string) (string, int) {
	cmd := exec.Command(e.tool, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	e.t.Fatalf("run %s: %v", e.tool, err)
	return "", -1
}

// pinArgs are the pinned network identity every consumer command is given.
func (e *e2eEnv) pinArgs() []string {
	return []string{"--chain-id", strconv.FormatUint(e.chainID, 10), "--genesis-hash", hex0x(e.genHash)}
}

// sourceSnapshot is a running source validator and the snapshot made from it.
type sourceSnapshot struct {
	src      *e2eNode
	traffic  *trafficDriver
	m        *manifest
	manifest string
	archive  string
	dir      string
}

// startSource starts the source validator, restarts it hard the given number of
// times (each restart turns the journal into a table file), and returns when it
// has reached the given height.
func (e *e2eEnv) startSource(minHeight uint64, restarts int) (*e2eNode, *trafficDriver) {
	return e.startSourceWith(minHeight, restarts, e2eTimeout())
}

func (e *e2eEnv) startSourceWith(minHeight uint64, restarts int, timeout string) (*e2eNode, *trafficDriver) {
	t := e.t
	srcData := filepath.Join(e.dir, "source", "data")
	src := e.newNode("source", e.keys["validator"], srcData, nil, timeout, "")
	src.start()
	e.waitUntil("the source's RPC", 60*time.Second, src, func() bool { return src.info() != nil })
	e.mintToken()
	traffic := e.startTraffic(src)

	// Two hard restarts: each one recovers the journal into a table file, so the
	// database the snapshot is made from has tables, a MANIFEST that has been
	// appended to, and a fresh journal, as a long-running node's does.
	step := minHeight / uint64(restarts+1)
	for i := uint64(1); i <= uint64(restarts); i++ {
		target := step * i
		e.waitUntil(fmt.Sprintf("the source to reach height %d", target), 8*time.Minute, src, func() bool { return src.height() >= target })
		t.Logf("crashing the source at height %d", src.height())
		src.kill()
		src.start()
		e.waitUntil("the source's RPC after a restart", 60*time.Second, src, func() bool { return src.info() != nil })
	}
	e.waitUntil(fmt.Sprintf("the source to reach height %d", minHeight), 8*time.Minute, src, func() bool { return src.height() >= minHeight })
	tables := 0
	for _, name := range dbFiles(t, srcData) {
		if strings.HasSuffix(name, ".ldb") {
			tables++
		}
	}
	if restarts > 0 && tables == 0 {
		t.Fatalf("the source's database holds no table files after %d restarts: %v", restarts, dbFiles(t, srcData))
	}
	t.Logf("the source is at height %d with %d table files: %v", src.height(), tables, dbFiles(t, srcData))
	return src, traffic
}

// makeSnapshot runs the producer script against the running source.
func (e *e2eEnv) makeSnapshot(src *e2eNode, name string) *sourceSnapshot {
	t := e.t
	dir := filepath.Join(e.dir, "snapshots-"+name)
	before := src.height()
	started := time.Now()
	out, err := e.runScript("scripts/make-snapshot.sh",
		"--data-dir", slash(src.dataDir),
		"--out-dir", slash(dir),
		"--work-dir", slash(filepath.Join(e.dir, "source", "work-"+name)),
		"--tool", slash(e.tool),
		"--node-binary", slash(e.nhb),
		"--binary-version", "e2e-test",
		"--binary-commit", "0123456789abcdef0123456789abcdef01234567",
	)
	t.Logf("make-snapshot.sh took %s; output:\n%s", time.Since(started).Round(time.Millisecond), out)
	if err != nil {
		t.Fatalf("make-snapshot.sh failed: %v", err)
	}
	after := src.height()
	m, err := readManifestFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read the manifest the script wrote: %v", err)
	}
	if m.Height == 0 || m.Height > after {
		t.Fatalf("the snapshot is at height %d, but the source was at %d..%d while it was taken", m.Height, before, after)
	}
	return &sourceSnapshot{src: src, m: m, dir: dir,
		manifest: filepath.Join(dir, "manifest.json"), archive: filepath.Join(dir, m.Archive.Name)}
}

// archiveBytes returns the uncompressed tar stream of an archive.
func archiveBytes(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	_, sum, err := hashRegularFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestSnapshotOnboardingEndToEnd(t *testing.T) {
	e2eEnabled(t)
	e := newE2EEnv(t)
	src, traffic := e.startSource(1800, 2)
	trafficStopped := false
	stopTraffic := func() {
		if !trafficStopped {
			trafficStopped = true
			t.Logf("the driver sent %d transactions in total", traffic.stop())
		}
	}
	defer stopTraffic()

	// ---- 1. a snapshot from the running node ------------------------------
	snap := e.makeSnapshot(src, "1")
	m := snap.m
	t.Logf("snapshot: chain %s height %d tip %s root %s, %d bytes, %d files, sha256 %s",
		m.ChainID, m.Height, m.TipHash, m.StateRoot, m.Archive.Size, len(m.Archive.Files), m.Archive.Sha256)
	if m.chainIDValue() != e.chainID || m.GenesisHash != hex0x(e.genHash) {
		t.Fatalf("the manifest names chain %s / %s, the genesis gives %d / %s", m.ChainID, m.GenesisHash, e.chainID, hex0x(e.genHash))
	}
	if m.Producer.BinaryCommit != "0123456789abcdef0123456789abcdef01234567" || m.Producer.BinarySha256 != fileSHA256(t, e.nhb) {
		t.Fatalf("the manifest does not describe the binary: %+v", m.Producer)
	}
	tableFiles := 0
	for _, f := range m.Archive.Files {
		if strings.HasSuffix(f.Name, ".ldb") {
			tableFiles++
		}
	}
	if tableFiles == 0 {
		t.Fatalf("the snapshot holds no table files: %+v", m.Archive.Files)
	}
	if !bytes.Equal(mustRead(t, snap.manifest), mustRead(t, strings.TrimSuffix(snap.archive, ".tar.gz")+".manifest.json")) {
		t.Fatalf("manifest.json differs from the archive's own manifest")
	}
	// The archive carries no key of any kind: neither the source's consensus key
	// nor its p2p identity key appears anywhere in it, in hex or as raw bytes.
	stream := archiveBytes(t, snap.archive)
	nodeKey := mustRead(t, filepath.Join(src.dataDir, "p2p", "node_key.json"))
	var identity struct {
		PrivateKey string `json:"privateKey"`
	}
	if err := json.Unmarshal(nodeKey, &identity); err != nil || len(identity.PrivateKey) != 64 {
		t.Fatalf("cannot read the source's p2p identity: %v %q", err, identity.PrivateKey)
	}
	identityRaw, _ := hex.DecodeString(identity.PrivateKey)
	for name, needle := range map[string][]byte{
		"consensus key (hex)":       []byte(e.keys["validator"].hex),
		"consensus key (raw)":       e.keys["validator"].raw,
		"p2p identity key (hex)":    []byte(identity.PrivateKey),
		"p2p identity key (raw)":    identityRaw,
		"the node_key.json name":    []byte("node_key.json"),
		"the JWT secret":            []byte(e.secret),
		"a user key (raw)":          e.keys["alice"].raw,
		"the sign state file name":  []byte("bft_sign_state"),
		"the peer store":            []byte("peerstore"),
		"the resolved genesis file": []byte("genesis.resolved.json"),
	} {
		if bytes.Contains(stream, needle) {
			t.Fatalf("the snapshot contains the %s", name)
		}
	}
	// The producer does not touch the running node: it keeps producing blocks.
	h := src.height()
	e.waitUntil("the source to keep producing blocks after the snapshot", 60*time.Second, src, func() bool { return src.height() >= h+20 })

	// ---- 2. the consumer's checks ----------------------------------------
	if out, code := e.runTool(append([]string{"verify", "--manifest", snap.manifest, "--archive", snap.archive}, e.pinArgs()...)...); code != 0 {
		t.Fatalf("verify failed (%d): %s", code, out)
	}
	wrongPin := []string{"--chain-id", strconv.FormatUint(e.chainID+1, 10), "--genesis-hash", hex0x(e.genHash)}
	if out, code := e.runTool(append([]string{"verify", "--manifest", snap.manifest, "--archive", snap.archive}, wrongPin...)...); code == 0 {
		t.Fatalf("verify accepted a wrong pinned chain id: %s", out)
	}
	folData := filepath.Join(e.dir, "follower", "data")
	if err := os.MkdirAll(filepath.Dir(folData), 0o755); err != nil {
		t.Fatal(err)
	}
	// A key that is already a validator in the snapshot must not start a node.
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive,
		"--target", filepath.Join(e.dir, "follower", "refused"), "--reject-validator", e.keys["validator"].addr}, e.pinArgs()...)...); code == 0 || !strings.Contains(out, "is a validator in this snapshot") {
		t.Fatalf("extract accepted the source validator's key (%d): %s", code, out)
	}
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive,
		"--target", folData, "--reject-validator", e.keys["follower"].addr}, e.pinArgs()...)...); code != 0 {
		t.Fatalf("extract failed (%d): %s", code, out)
	} else {
		t.Logf("extract:\n%s", out)
	}
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive, "--target", folData}, e.pinArgs()...)...); code == 0 || !strings.Contains(out, "not empty") {
		t.Fatalf("a second extract over the same directory was not refused (%d): %s", code, out)
	}
	// What was extracted is the chain database and nothing that identifies a node.
	for _, name := range dbFiles(t, folData) {
		if !allowedFileName(name) {
			t.Fatalf("the extracted directory holds %q", name)
		}
	}
	for _, name := range []string{"p2p", "bft_sign_state.json", "polc_lock.json", "genesis.resolved.json", "LOCK"} {
		if _, err := os.Stat(filepath.Join(folData, name)); err == nil {
			t.Fatalf("the extracted directory holds %s", name)
		}
	}

	// ---- 3. the follower ---------------------------------------------------
	follower := e.newNode("follower", e.keys["follower"], folData, src, e2eTimeout(), "")
	follower.start()
	// As the deployment script runs it: the follower is at the tip only once it
	// has applied a block past its snapshot, has a peer, and holds the same
	// blocks as the source.
	syncArgs := append([]string{"wait-synced", "--rpc", follower.rpcURL(), "--tip-rpc", src.rpcURL(), "--min-height", strconv.FormatUint(m.Height, 10),
		"--max-lag-blocks", "8", "--interval", "1s", "--timeout", "4m", "--stall-timeout", "90s"}, e.pinArgs()...)
	started := time.Now()
	out, code := e.runTool(syncArgs...)
	t.Logf("wait-synced (exit %d) after %s:\n%s", code, time.Since(started).Round(time.Second), out)
	if code != 0 {
		t.Fatalf("the follower did not catch up (exit %d)\n--- follower log ---\n%s", code, follower.logTail(30))
	}

	// It is a different node: another p2p identity, and no vote of its own counts.
	srcInfo, folInfo := src.info(), follower.info()
	if srcInfo == nil || folInfo == nil || srcInfo.NodeID == folInfo.NodeID {
		t.Fatalf("the follower shares the source's p2p identity: %+v %+v", srcInfo, folInfo)
	}
	folKey := mustRead(t, filepath.Join(folData, "p2p", "node_key.json"))
	if bytes.Equal(folKey, nodeKey) {
		t.Fatalf("the follower has the source's node_key.json")
	}
	wantSet := []string{strings.ToLower(e.keys["validator"].addr)}
	for name, n := range map[string]*e2eNode{"source": src, "follower": follower} {
		got := n.validators()
		if len(got) != 1 || !strings.EqualFold(got[0], mustHexAddress(t, e.keys["validator"].addr)) {
			t.Fatalf("the %s's validator set is %v, want only %v", name, got, wantSet)
		}
	}
	// It follows the chain: the network grows and so does the follower.
	fh, sh := follower.height(), src.height()
	e.waitUntil("the follower to follow new blocks", 90*time.Second, follower, func() bool { return follower.height() >= fh+50 })
	t.Logf("the follower advanced from %d to %d while the source went from %d to %d", fh, follower.height(), sh, src.height())

	// Live cross-check: the newest blocks both nodes report agree (their
	// heights overlap because each is at most a few blocks behind the other).
	assertRecentBlocksAgree(t, src, follower)

	// ---- 4. the follower survives a crash ---------------------------------
	before := follower.height()
	t.Logf("crashing the follower at height %d", before)
	follower.kill()
	follower.start()
	e.waitUntil("the restarted follower's RPC", 60*time.Second, follower, func() bool { return follower.info() != nil })
	out, code = e.runTool(syncArgs...)
	t.Logf("wait-synced after the restart (exit %d):\n%s", code, out)
	if code != 0 {
		t.Fatalf("the restarted follower did not catch up (exit %d)\n--- follower log ---\n%s", code, follower.logTail(30))
	}
	if after := follower.height(); after < before {
		t.Fatalf("the restarted follower is at height %d, below the %d it had reached", after, before)
	}
	fh = follower.height()
	e.waitUntil("the restarted follower to follow new blocks", 90*time.Second, follower, func() bool { return follower.height() >= fh+50 })
	assertRecentBlocksAgree(t, src, follower)

	// Its consensus engine starts half a minute after the node does. Once it
	// runs, the follower prevotes like any node would, and the validator
	// rejects every one of those votes: it is not in the validator set, so it
	// takes no part in consensus. The chain does not notice it.
	e.waitUntil("the source to reject a vote from the follower", 120*time.Second, src, func() bool {
		return src.logCount("vote from non-validator") > 0
	})
	t.Logf("the source rejected %d votes from the follower (a key that is not a validator): it does not vote", src.logCount("vote from non-validator"))
	sh = src.height()
	e.waitUntil("the chain to go on after the follower's votes", 60*time.Second, src, func() bool { return src.height() >= sh+40 })
	e.waitUntil("the follower to go on after its own votes were rejected", 60*time.Second, follower, func() bool { return follower.height() >= src.height()-8 })

	// The follower reached a block only by executing it: it never rejected one
	// of the network's blocks, and it never had a block of its own to commit.
	for _, bad := range []string{"state root mismatch", "quorum certificate verification failed", "tx root mismatch", "block height mismatch: got"} {
		if n := follower.logCount(bad); n > 0 {
			t.Fatalf("the follower's log holds %d lines with %q\n%s", n, bad, follower.logTail(20))
		}
	}
	// Enough blocks after the snapshot to have crossed several reward epochs.
	e.waitUntil("the follower to be well past the snapshot", 120*time.Second, follower, func() bool { return follower.height() >= m.Height+400 })

	// ---- 5. every common height matches -----------------------------------
	stopTraffic()
	src.kill()
	follower.kill()
	srcTip, folTip := compareChains(t, src.dataDir, follower.dataDir, m.Height)
	t.Logf("compared block hashes and state roots at every height from 0 to %d: source tip %d, follower tip %d, snapshot at %d", min(srcTip, folTip), srcTip, folTip, m.Height)
	if folTip <= m.Height+100 {
		t.Fatalf("the follower only reached height %d from a snapshot at %d", folTip, m.Height)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// mustHexAddress returns the 0x-prefixed hex form of a bech32 address, the form
// nhb_getValidatorSet reports.
func mustHexAddress(t *testing.T, bech string) string {
	t.Helper()
	addr, err := crypto.DecodeAddress(bech)
	if err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(addr.Bytes())
}

// assertRecentBlocksAgree compares the newest blocks two nodes report.
func assertRecentBlocksAgree(t *testing.T, a, b *e2eNode) {
	t.Helper()
	get := func(n *e2eNode) map[uint64]string {
		raw, err := n.call("nhb_getLatestBlocks", 20)
		if err != nil {
			t.Fatalf("%s: %v", n.name, err)
		}
		var blocks []struct {
			Header struct {
				Height    uint64 `json:"height"`
				StateRoot string `json:"stateRoot"`
				PrevHash  string `json:"prevHash"`
			} `json:"Header"`
		}
		if err := json.Unmarshal(raw, &blocks); err != nil {
			t.Fatal(err)
		}
		out := map[uint64]string{}
		for _, blk := range blocks {
			out[blk.Header.Height] = blk.Header.StateRoot + "/" + blk.Header.PrevHash
		}
		return out
	}
	ma, mb := get(a), get(b)
	common := 0
	for h, va := range ma {
		if vb, ok := mb[h]; ok {
			common++
			if va != vb {
				t.Fatalf("at height %d the nodes disagree: %s has %s, %s has %s", h, a.name, va, b.name, vb)
			}
		}
	}
	if common == 0 {
		t.Logf("note: the newest 20 blocks of %s and %s do not overlap", a.name, b.name)
	}
}

// compareChains opens two stopped nodes' databases read-only and compares the
// block hash and the state root at every height they have in common.
func compareChains(t *testing.T, dirA, dirB string, snapshotHeight uint64) (tipA, tipB uint64) {
	t.Helper()
	a, err := openChainDB(dirA)
	if err != nil {
		t.Fatalf("open %s: %v", dirA, err)
	}
	defer a.close()
	b, err := openChainDB(dirB)
	if err != nil {
		t.Fatalf("open %s: %v", dirB, err)
	}
	defer b.close()
	ida, err := a.readIdentity(checkOptions{HeaderWindow: 0})
	if err != nil {
		t.Fatalf("read %s: %v", dirA, err)
	}
	idb, err := b.readIdentity(checkOptions{HeaderWindow: 0})
	if err != nil {
		t.Fatalf("read %s: %v", dirB, err)
	}
	if ida.ChainID != idb.ChainID || !bytes.Equal(ida.GenesisHash, idb.GenesisHash) {
		t.Fatalf("the two databases are of different chains")
	}
	last := min(ida.Height, idb.Height)
	for h := uint64(0); h <= last; h++ {
		blockA, hashA, err := a.blockAt(h)
		if err != nil {
			t.Fatalf("source height %d: %v", h, err)
		}
		blockB, hashB, err := b.blockAt(h)
		if err != nil {
			t.Fatalf("follower height %d: %v", h, err)
		}
		if !bytes.Equal(hashA, hashB) {
			t.Fatalf("height %d: block hash %x on the source, %x on the follower", h, hashA, hashB)
		}
		if !bytes.Equal(blockA.Header.StateRoot, blockB.Header.StateRoot) {
			t.Fatalf("height %d: state root %x on the source, %x on the follower", h, blockA.Header.StateRoot, blockB.Header.StateRoot)
		}
		if len(blockA.Transactions) != len(blockB.Transactions) {
			t.Fatalf("height %d: %d transactions on the source, %d on the follower", h, len(blockA.Transactions), len(blockB.Transactions))
		}
	}
	// The state at the follower's tip opens completely too (its own database is
	// as sound as the one it started from).
	if _, err := openAndReadIdentity(dirB, checkOptions{HeaderWindow: 0}); err != nil {
		t.Fatalf("the follower's own database does not pass the full check: %v", err)
	}
	return ida.Height, idb.Height
}
