package main

// What "at the network tip" has to mean. A snapshot is nobody's signed word, so a
// node that started from a forged one must not be reported at the tip, and neither
// must a node that never heard from the network at all. wait-synced is run the
// way scripts/deployvalidator.sh runs it, against stand-ins for the node and the
// reference node it is measured against.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/core/types"
	"nhbchain/storage/trie"
)

const (
	waitOK      = 0
	waitTimeout = 3
	waitStalled = 4
	waitFork    = 6
	waitNoRPC   = 7
)

// waitCLI runs wait-synced against a node stand-in (and a reference stand-in when
// there is one) and returns its exit code and everything it printed.
func waitCLI(t *testing.T, local, ref *fakeNode, extra ...string) (int, string) {
	t.Helper()
	args := []string{"wait-synced", "--rpc", local.serve(t), "--interval", "10ms", "--timeout", "600ms", "--stall-timeout", "10s"}
	if ref != nil {
		args = append(args, "--tip-rpc", ref.serve(t))
	}
	var out, errOut bytes.Buffer
	code := run(append(args, extra...), &out, &errOut)
	return code, out.String() + errOut.String()
}

func nowUnix() int64 { return time.Now().Unix() }

func TestWaitSyncedNeedsAPeer(t *testing.T) {
	none := 0
	lonely := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: nowUnix, peers: &none}
	code, out := waitCLI(t, lonely, nil)
	if code != waitTimeout || !strings.Contains(out, "connected to no peer") {
		t.Fatalf("a node that hears from nobody was reported at the tip (exit %d):\n%s", code, out)
	}
	// The same node with a peer is at the tip, and the result says how many.
	code, out = waitCLI(t, &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: nowUnix}, nil)
	if code != waitOK || !strings.Contains(out, `"peers": 2`) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Against a reference node too.
	same := func() *fakeNode {
		return &fakeNode{height: 1000, max: 1000, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	}
	lonely = same()
	lonely.peers = &none
	if code, out := waitCLI(t, lonely, same()); code != waitTimeout {
		t.Fatalf("a node with no peer that shares the reference's blocks was reported at the tip (exit %d):\n%s", code, out)
	}
}

// A block dated in the future says nothing about being at the tip of a chain that
// is still producing blocks; only what is close to this host's clock does.
func TestWaitSyncedIsNotFooledByATipDatedInTheFuture(t *testing.T) {
	future := func(secs int64) *fakeNode {
		return &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() + secs }}
	}
	code, out := waitCLI(t, future(3600), nil)
	if code != waitTimeout || !strings.Contains(out, "dated in the future") {
		t.Fatalf("a tip dated an hour ahead was reported at the tip (exit %d):\n%s", code, out)
	}
	// Ordinary clock skew is not held against a node.
	if code, out := waitCLI(t, future(30), nil); code != waitOK {
		t.Fatalf("a tip dated 30 s ahead was refused (exit %d):\n%s", code, out)
	}
}

// A snapshot that is not part of the network's chain can never be followed past:
// the node must have applied a block above the snapshot's height.
func TestWaitSyncedNeedsBlocksPastTheSnapshot(t *testing.T) {
	stuck := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: nowUnix}
	code, out := waitCLI(t, stuck, nil, "--min-height", "100")
	if code != waitTimeout || !strings.Contains(out, "no block above the snapshot's height 100") {
		t.Fatalf("a node that has not passed its snapshot was reported at the tip (exit %d):\n%s", code, out)
	}
	moving := &fakeNode{height: 100, step: 1, max: 105, chainID: 7, genesis: "aa", timestamp: nowUnix}
	if code, out := waitCLI(t, moving, nil, "--min-height", "100"); code != waitOK {
		t.Fatalf("a node that went on past its snapshot was refused (exit %d):\n%s", code, out)
	}
	// With a reference node too: the same blocks, but not one past the snapshot.
	ref := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	frozen := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	if code, out := waitCLI(t, frozen, ref, "--min-height", "100"); code != waitTimeout {
		t.Fatalf("a node frozen at its snapshot was reported at the tip (exit %d):\n%s", code, out)
	}
}

// Against a reference node, the blocks have to be the same ones, not just as many.
func TestWaitSyncedRefusesAChainThatDisagreesWithTheReference(t *testing.T) {
	ref := &fakeNode{height: 1000, max: 1000, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	forked := &fakeNode{height: 1000, max: 1000, chainID: 7, genesis: "aa", blockTime: sameChainTime, salt: 9}
	code, out := waitCLI(t, forked, ref)
	if code != waitFork || !strings.Contains(out, "not on the same chain") {
		t.Fatalf("a node on another chain, at the reference's height, was not refused (exit %d):\n%s", code, out)
	}
	// One block ahead of the reference is a fork too when the block it shares is not the same.
	ahead := &fakeNode{height: 1001, max: 1001, chainID: 7, genesis: "aa", blockTime: sameChainTime, salt: 9}
	if code, out := waitCLI(t, ahead, ref); code != waitFork {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The same chain is at the tip.
	same := &fakeNode{height: 999, max: 999, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	if code, out := waitCLI(t, same, ref); code != waitOK {
		t.Fatalf("a node one block behind the reference on the same chain was refused (exit %d):\n%s", code, out)
	}
}

func TestWaitSyncedNeedsTheNodeAndTheReferenceToShareABlock(t *testing.T) {
	// Each answers with its newest block alone, and they are two blocks apart:
	// nothing to compare them by.
	ref := &fakeNode{height: 1000, max: 1000, chainID: 7, genesis: "aa", blockTime: sameChainTime, newestOnly: true}
	local := &fakeNode{height: 998, max: 998, chainID: 7, genesis: "aa", blockTime: sameChainTime, newestOnly: true}
	code, out := waitCLI(t, local, ref)
	if code != waitTimeout || !strings.Contains(out, "have no height in common") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWaitSyncedRefusesANodeAheadOfTheReference(t *testing.T) {
	ref := &fakeNode{height: 900, max: 900, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	local := &fakeNode{height: 1000, max: 1000, chainID: 7, genesis: "aa", blockTime: sameChainTime}
	code, out := waitCLI(t, local, ref)
	if code != waitTimeout || !strings.Contains(out, "blocks ahead") {
		t.Fatalf("a node 100 blocks ahead of the network's tip was reported at it (exit %d):\n%s", code, out)
	}
}

func TestWaitSyncedRefusesALagThatCannotBeComparedWithTheReference(t *testing.T) {
	local := &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", timestamp: nowUnix}
	ref := &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", timestamp: nowUnix}
	var out, errOut bytes.Buffer
	code := run([]string{"wait-synced", "--rpc", local.serve(t), "--tip-rpc", ref.serve(t), "--max-lag-blocks", "16"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "15 blocks") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	// Without a reference the number means nothing and is not held to that.
	code, _ = waitCLI(t, &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", timestamp: nowUnix}, nil, "--max-lag-blocks", "16")
	if code != waitOK {
		t.Fatalf("exit %d", code)
	}
}

// addForgedBlock appends a block that nothing in the chain vouches for: a new
// state, valid links and no signatures of anyone.
func (b *chainBuilder) addForgedBlock(timestamp int64) {
	b.t.Helper()
	quietly(func() {
		height := b.bc.Height() + 1
		tr, err := trie.NewTrie(b.db, b.root.Bytes())
		if err != nil {
			b.t.Fatal(err)
		}
		if err := tr.Update(bytes.Repeat([]byte{0xEE}, 32), []byte("attacker chosen balance")); err != nil {
			b.t.Fatal(err)
		}
		root, err := tr.Commit(b.root, height)
		if err != nil {
			b.t.Fatal(err)
		}
		hdr := &types.BlockHeader{Height: height, Timestamp: timestamp, PrevHash: b.bc.Tip(), StateRoot: root.Bytes(), TxRoot: gethtypes.EmptyRootHash.Bytes()}
		if err := b.bc.AddBlock(types.NewBlock(hdr, nil)); err != nil {
			b.t.Fatal(err)
		}
	})
}

// forgedSnapshot packs a chain of real blocks with one forged block on top, dated
// tipTime, and returns what an extraction of it needs.
func forgedSnapshot(t *testing.T, tipTime int64) (f *fixture, expect expectations) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, db, false, testAddress(0x33))
	for i := 0; i < 50; i++ {
		b.addBlock(5)
	}
	b.addForgedBlock(tipTime)
	expect = expectations{ChainID: b.bc.ChainID(), GenesisHash: b.bc.GenesisHash()}
	b.close()
	f = &fixture{t: t, stage: stageChainFiles(t, db), outDir: filepath.Join(t.TempDir(), "out"), genesis: expect.GenesisHash, chainID: expect.ChainID}
	m, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: f.outDir, CreatedAt: time.Now(), Producer: producerInfo{BinaryVersion: "x", BinaryCommit: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	f.archive = filepath.Join(f.outDir, m.Archive.Name)
	f.manifest = strings.TrimSuffix(f.archive, ".tar.gz") + ".manifest.json"
	return f, expect
}

// serveBlocks answers like a node whose chain is the given blocks (newest last)
// and that is connected to the given number of peers.
func serveBlocks(t *testing.T, blocks []*types.Block, chainID uint64, genesis []byte, peers int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "net_info":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"chainId":%d,"genesisHash":"0x%s","peerCounts":{"total":%d}}}`, chainID, hex.EncodeToString(genesis), peers)
		case "nhb_getLatestBlocks":
			newestFirst := make([]*types.Block, 0, len(blocks))
			for i := len(blocks) - 1; i >= 0; i-- {
				newestFirst = append(newestFirst, blocks[i])
			}
			raw, _ := json.Marshal(newestFirst)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, raw)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A forged chain, made from the live chain's genesis and real blocks with one
// invented block on top, dated far in the future, used to be extracted without a
// word and then reported at the tip by a node that never heard from the network.
func TestAForgedSnapshotIsRefusedAndAnUnconnectedNodeOnItIsNeverAtTheTip(t *testing.T) {
	f, expect := forgedSnapshot(t, 4102444800) // the year 2100

	t.Run("its newest block is dated in the future", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "data")
		for _, now := range []time.Time{time.Now(), {}} { // a zero time means the host's clock
			_, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target, Expect: expect, Now: now})
			if err == nil || !strings.Contains(err.Error(), "ahead of this host's clock") {
				t.Fatalf("the forged snapshot was extracted, or refused for another reason: %v", err)
			}
			mustNotLeaveTraces(t, target)
		}
		var out, errOut bytes.Buffer
		code := run([]string{"verify", "--manifest", f.manifest, "--archive", f.archive,
			"--chain-id", fmt.Sprint(expect.ChainID), "--genesis-hash", hex0x(expect.GenesisHash)}, &out, &errOut)
		if code == 0 || !strings.Contains(errOut.String(), "ahead of this host's clock") {
			t.Fatalf("verify accepted the forged snapshot (exit %d): %s", code, errOut.String())
		}
	})

	// The same forgery dated to look current gets through extract: nothing in a
	// snapshot proves it belongs to the network. What proves it is following the
	// network: a node on it cannot apply the network's next block and never hears
	// from a peer, and the wait for the tip does not end.
	t.Run("dated to look current, the node on it does not reach the tip", func(t *testing.T) {
		g, gexpect := forgedSnapshot(t, time.Now().Unix())
		target := filepath.Join(t.TempDir(), "data")
		_, id, err := extractSnapshot(extractOptions{ManifestPath: g.manifest, ArchivePath: g.archive, Target: target, Expect: gexpect, Now: time.Now()})
		if err != nil {
			t.Fatalf("extract refused a snapshot that only a peer can show to be forged: %v", err)
		}
		cdb, err := openChainDB(target)
		if err != nil {
			t.Fatal(err)
		}
		tip, _, err := cdb.blockAt(id.Height)
		cdb.close()
		if err != nil {
			t.Fatal(err)
		}
		snapshotHeight := fmt.Sprint(id.Height)
		for _, tc := range []struct {
			name  string
			peers int
			extra []string
		}{
			{"no peer, and nothing said about the snapshot", 0, nil},
			{"no peer", 0, []string{"--min-height", snapshotHeight}},
			{"a peer, that never lets it past the snapshot", 1, []string{"--min-height", snapshotHeight}},
		} {
			var out, errOut bytes.Buffer
			args := []string{"wait-synced", "--rpc", serveBlocks(t, []*types.Block{tip}, gexpect.ChainID, gexpect.GenesisHash, tc.peers),
				"--interval", "10ms", "--timeout", "600ms", "--stall-timeout", "10s"}
			code := run(append(args, tc.extra...), &out, &errOut)
			if code != waitTimeout {
				t.Fatalf("a node stuck on the forged tip (%s) was reported at the tip or not checked (exit %d):\n%s%s", tc.name, code, out.String(), errOut.String())
			}
		}
	})
}
