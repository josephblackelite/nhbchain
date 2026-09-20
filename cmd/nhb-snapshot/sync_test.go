package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core/types"
)

// fakeNode answers the two RPC calls the follower's progress is read from.
type fakeNode struct {
	mu        sync.Mutex
	height    uint64
	step      uint64 // how much the height grows on each nhb_getLatestBlocks
	max       uint64
	timestamp func() int64 // the newest block's timestamp
	// blockTime, when set, is the timestamp of every block, the newest included,
	// so that two nodes holding the same height hold the same block.
	blockTime func(h uint64) int64
	// salt is part of every block, so that two nodes can hold different blocks
	// at one height (a node on another chain).
	salt  byte
	peers *int // connected peers net_info reports (2 when nil)
	// newestOnly makes the node answer nhb_getLatestBlocks with its newest block
	// alone, whatever it is asked for.
	newestOnly bool
	chainID    uint64
	genesis    string
	down       int32 // net_info fails while > 0
	// noBlocks makes nhb_getLatestBlocks fail while net_info answers: a node whose
	// RPC came up and then stopped answering what a follower asks.
	noBlocks bool
}

// sameChainTime is the block time of a chain that every fakeNode shares.
func sameChainTime(h uint64) int64 { return 1_700_000_000 + int64(h) }

func (n *fakeNode) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "net_info":
			if atomic.AddInt32(&n.down, -1) >= 0 {
				http.Error(w, "starting", http.StatusServiceUnavailable)
				return
			}
			peers := 2
			if n.peers != nil {
				peers = *n.peers
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"nodeId":"0xabc","chainId":%d,"genesisHash":%q,"peerCounts":{"total":%d}}}`, n.chainID, n.genesis, peers)
		case "nhb_getLatestBlocks":
			if n.noBlocks {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			count := 1
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params[0], &count)
			}
			if n.newestOnly {
				count = 1
			}
			n.mu.Lock()
			if n.height < n.max {
				n.height += n.step
				if n.height > n.max {
					n.height = n.max
				}
			}
			h := n.height
			n.mu.Unlock()
			var blocks []*types.Block
			for i := 0; i < count && uint64(i) <= h; i++ {
				height := h - uint64(i)
				var ts int64
				if n.blockTime != nil {
					ts = n.blockTime(height)
				} else {
					ts = n.timestamp() - int64(i)*4
				}
				blocks = append(blocks, types.NewBlock(&types.BlockHeader{Height: height, Timestamp: ts, PrevHash: []byte{1, n.salt}, StateRoot: []byte{2}}, nil))
			}
			out, _ := json.Marshal(blocks)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, out)
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	})
}

func (n *fakeNode) serve(t *testing.T) string {
	srv := httptest.NewServer(n.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func fastOptions(out *bytes.Buffer) waitOptions {
	return waitOptions{
		Interval:     5 * time.Millisecond,
		Timeout:      5 * time.Second,
		StallTimeout: 5 * time.Second,
		Stable:       2,
		Out:          out,
	}
}

func TestWaitSyncedAgainstAReferenceNode(t *testing.T) {
	local := &fakeNode{height: 100, step: 40, max: 1000, blockTime: sameChainTime, chainID: 7, genesis: "aa"}
	ref := &fakeNode{height: 1000, max: 1000, blockTime: sameChainTime, chainID: 7, genesis: "aa"}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC, opts.TipRPC = local.serve(t), ref.serve(t)
	chain := uint64(7)
	opts.ExpectChainID = &chain
	opts.MaxLagBlocks = 3

	res, err := waitSynced(context.Background(), opts)
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out.String())
	}
	if res.Height < 997 || res.TipRPC != 1000 || res.LagBlock > 3 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(out.String(), "blocks behind") {
		t.Fatalf("no progress was reported:\n%s", out.String())
	}
}

func TestWaitSyncedByBlockAgeWithoutAReference(t *testing.T) {
	// The newest block is old at first, then recent: the node caught up.
	var calls int32
	local := &fakeNode{height: 500, step: 1, max: 100000, chainID: 7, genesis: "aa",
		timestamp: func() int64 {
			if atomic.AddInt32(&calls, 1) < 6 {
				return time.Now().Unix() - 3600
			}
			return time.Now().Unix() - 2
		}}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC = local.serve(t)
	opts.MaxLagSeconds = 30
	res, err := waitSynced(context.Background(), opts)
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out.String())
	}
	if res.LagSecs > 30 || res.TipRPC != 0 {
		t.Fatalf("result %+v", res)
	}
	if atomic.LoadInt32(&calls) < 6 {
		t.Fatalf("returned while the newest block was still an hour old")
	}
}

func TestWaitSyncedWaitsForTheRPCToComeUp(t *testing.T) {
	local := &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", down: 3, timestamp: func() int64 { return time.Now().Unix() }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC = local.serve(t)
	if _, err := waitSynced(context.Background(), opts); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(out.String(), "waiting for the node's RPC") {
		t.Fatalf("no waiting was reported:\n%s", out.String())
	}
}

func TestWaitSyncedRefusesTheWrongChain(t *testing.T) {
	local := &fakeNode{height: 10, max: 10, chainID: 8, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC = local.serve(t)
	chain := uint64(7)
	opts.ExpectChainID = &chain
	_, err := waitSynced(context.Background(), opts)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitChain || !strings.Contains(err.Error(), "chain id 8") {
		t.Fatalf("got: %v", err)
	}

	opts = fastOptions(&out)
	opts.RPC = local.serve(t)
	local.chainID = 7
	opts.ExpectGenesis = bytes.Repeat([]byte{0xbb}, 32)
	_, err = waitSynced(context.Background(), opts)
	if !errors.As(err, &ee) || ee.code != exitChain || !strings.Contains(err.Error(), "genesis hash") {
		t.Fatalf("got: %v", err)
	}
}

func TestWaitSyncedRefusesAReferenceOnAnotherChain(t *testing.T) {
	local := &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() }}
	ref := &fakeNode{height: 10, max: 10, chainID: 9, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC, opts.TipRPC = local.serve(t), ref.serve(t)
	chain := uint64(7)
	opts.ExpectChainID = &chain
	_, err := waitSynced(context.Background(), opts)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitChain || !strings.Contains(err.Error(), "reference RPC") {
		t.Fatalf("got: %v", err)
	}
}

func TestWaitSyncedDetectsAStall(t *testing.T) {
	// The height never moves and the newest block is old.
	local := &fakeNode{height: 500, step: 0, max: 500, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() - 7200 }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC = local.serve(t)
	opts.StallTimeout = 60 * time.Millisecond
	_, err := waitSynced(context.Background(), opts)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitStalled || !strings.Contains(err.Error(), "not syncing") {
		t.Fatalf("got: %v", err)
	}
}

func TestWaitSyncedTimesOut(t *testing.T) {
	// Progress is slow: it moves, so it is not a stall, but it never arrives.
	local := &fakeNode{height: 0, step: 1, max: 1 << 40, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() - 7200 }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC = local.serve(t)
	opts.Timeout = 100 * time.Millisecond
	_, err := waitSynced(context.Background(), opts)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitTimeout {
		t.Fatalf("got: %v", err)
	}
}

// waitBounded runs wait-synced the way the deployment script runs it: the overall
// timeout is far above what the stage being tested is given, so that a wait that
// only ends at the overall timeout is told apart from one that ends on its own
// bound.
func waitBounded(t *testing.T, rpc string, extra ...string) (code int, output string, took time.Duration) {
	t.Helper()
	args := append([]string{"wait-synced", "--rpc", rpc, "--interval", "10ms", "--stall-timeout", "30s"}, extra...)
	var out, errOut bytes.Buffer
	started := time.Now()
	code = run(args, &out, &errOut)
	return code, out.String() + errOut.String(), time.Since(started)
}

// A service that is not running, or that crash-loops, never answers. The wait for
// the node's RPC must end on its own bound, with a message that says so, and not
// run for the whole of the overall timeout (two hours in the deployment script)
// before saying anything.
func TestWaitSyncedGivesUpOnARPCThatNeverComesUp(t *testing.T) {
	expect := func(t *testing.T, code int, out string, took time.Duration) {
		t.Helper()
		if code != waitNoRPC {
			t.Fatalf("exit %d, want %d:\n%s", code, waitNoRPC, out)
		}
		if !strings.Contains(out, "did not answer within 300ms") || !strings.Contains(out, "crash-looping") {
			t.Fatalf("the message does not say that the RPC never came up:\n%s", out)
		}
		if took > 10*time.Second {
			t.Fatalf("the wait ran for %s although the RPC was given 300ms and the overall timeout is 30s", took)
		}
	}
	bounds := []string{"--timeout", "30s", "--rpc-timeout", "300ms"}

	t.Run("nothing listens", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		code, out, took := waitBounded(t, url, bounds...)
		expect(t, code, out, took)
	})
	t.Run("connections are accepted and never answered", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var held []net.Conn
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				held = append(held, c)
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			_ = ln.Close()
			mu.Lock()
			defer mu.Unlock()
			for _, c := range held {
				_ = c.Close()
			}
		})
		code, out, took := waitBounded(t, "http://"+ln.Addr().String(), bounds...)
		expect(t, code, out, took)
	})
	t.Run("the node answers that it is still starting, for ever", func(t *testing.T) {
		starting := &fakeNode{chainID: 7, genesis: "aa", down: 1 << 30}
		code, out, took := waitBounded(t, starting.serve(t), bounds...)
		expect(t, code, out, took)
	})
	// A node that has shown no sign of life for the stall timeout has not advanced
	// either, so the stall timeout bounds this stage too when it is the shorter.
	t.Run("the stall timeout bounds it as well", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		code, out, took := waitBounded(t, url, "--timeout", "30s", "--rpc-timeout", "30s", "--stall-timeout", "300ms")
		expect(t, code, out, took)
	})
	t.Run("the overall timeout still ends the wait when it is the shorter one", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		code, out, took := waitBounded(t, url, "--timeout", "300ms", "--rpc-timeout", "30s")
		if code != waitTimeout || took > 10*time.Second {
			t.Fatalf("exit %d after %s, want %d:\n%s", code, took, waitTimeout, out)
		}
	})
	t.Run("a node that comes up in time is waited for", func(t *testing.T) {
		slow := &fakeNode{height: 10, max: 10, chainID: 7, genesis: "aa", down: 5, timestamp: nowUnix}
		code, out, _ := waitBounded(t, slow.serve(t), "--timeout", "30s", "--rpc-timeout", "20s")
		if code != waitOK {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
}

// The same holds after the RPC has answered once: a node that answers net_info
// but nothing a follower is measured by has not advanced either, and the stall
// timeout, not the overall timeout, ends the wait.
func TestWaitSyncedDoesNotWaitOutTheTimeoutForANodeThatStopsAnswering(t *testing.T) {
	broken := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: nowUnix, noBlocks: true}
	code, out, took := waitBounded(t, broken.serve(t), "--timeout", "8s", "--stall-timeout", "300ms")
	if code != waitStalled || !strings.Contains(out, "has not answered a request for its newest block") {
		t.Fatalf("exit %d, want %d:\n%s", code, waitStalled, out)
	}
	if took > 5*time.Second {
		t.Fatalf("the wait ran for %s although the stall timeout is 300ms and the overall timeout is 8s", took)
	}
}

func TestWaitSyncedNeverArrivesWhenTheReferenceStaysAhead(t *testing.T) {
	local := &fakeNode{height: 0, step: 1, max: 1 << 40, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() }}
	ref := &fakeNode{height: 1 << 30, max: 1 << 30, chainID: 7, genesis: "aa", timestamp: func() int64 { return time.Now().Unix() }}
	var out bytes.Buffer
	opts := fastOptions(&out)
	opts.RPC, opts.TipRPC = local.serve(t), ref.serve(t)
	opts.Timeout = 100 * time.Millisecond
	if _, err := waitSynced(context.Background(), opts); err == nil {
		t.Fatalf("a node far behind the reference was reported synced")
	}
}

func TestRunDispatchesAndReportsUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no arguments: exit %d", code)
	}
	if code := run([]string{"info"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "--data-dir is required") {
		t.Fatalf("info without a directory: exit %d: %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"verify", "--manifest", "m", "--archive", "a"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "not pinned") {
		t.Fatalf("verify without pins: exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"version"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "nhb-snapshot") {
		t.Fatalf("version: exit %d", code)
	}
}

func TestPinsCanComeFromTheEnvironment(t *testing.T) {
	f := newFixture(t)
	t.Setenv(envChainID, fmt.Sprintf("%d", f.chainID))
	t.Setenv(envGenesisHash, hex0x(f.genesis))
	var out, errOut bytes.Buffer
	code := run([]string{"verify", "--manifest", f.manifest, "--archive", f.archive}, &out, &errOut)
	if code != 0 {
		t.Fatalf("verify with pins from the environment: exit %d: %s", code, errOut.String())
	}
	t.Setenv(envChainID, fmt.Sprintf("%d", f.chainID+1))
	errOut.Reset()
	if code := run([]string{"verify", "--manifest", f.manifest, "--archive", f.archive}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "chain id") {
		t.Fatalf("verify with a wrong pinned chain id: exit %d: %s", code, errOut.String())
	}
}

func TestManifestShowField(t *testing.T) {
	f := newFixture(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"manifest", "show", "--manifest", f.manifest, "--field", "producer.binaryCommit"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != "0123456789abcdef" {
		t.Fatalf("exit %d: %q %s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"manifest", "show", "--manifest", f.manifest, "--field", "nonsense"}, &out, &errOut); code != 2 {
		t.Fatalf("an unknown field: exit %d", code)
	}
}

func TestManifestValidation(t *testing.T) {
	f := newFixture(t)
	good, err := os.ReadFile(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseManifest(good); err != nil {
		t.Fatalf("the packed manifest does not validate: %v", err)
	}
	mutate := func(edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, _ := json.Marshal(m)
		return out
	}
	archive := func(m map[string]any) map[string]any { return m["archive"].(map[string]any) }
	cases := map[string][]byte{
		"unknown field":                   mutate(func(m map[string]any) { m["extra"] = 1 }),
		"future version":                  mutate(func(m map[string]any) { m["manifestVersion"] = 2 }),
		"chain id not the genesis prefix": mutate(func(m map[string]any) { m["chainId"] = "1" }),
		"chain id not decimal":            mutate(func(m map[string]any) { m["chainId"] = "0x1" }),
		"hash not 32 bytes":               mutate(func(m map[string]any) { m["tipHash"] = "0x1234" }),
		"hash upper case":                 mutate(func(m map[string]any) { m["stateRoot"] = strings.ToUpper(m["stateRoot"].(string)) }),
		"bad time":                        mutate(func(m map[string]any) { m["createdAt"] = "yesterday" }),
		"archive with a path":             mutate(func(m map[string]any) { archive(m)["name"] = "../x.tar.gz" }),
		"archive not tar.gz":              mutate(func(m map[string]any) { archive(m)["name"] = "x.zip" }),
		"format":                          mutate(func(m map[string]any) { archive(m)["format"] = "zip" }),
		"sha256 short":                    mutate(func(m map[string]any) { archive(m)["sha256"] = "abcd" }),
		"no files":                        mutate(func(m map[string]any) { archive(m)["files"] = []any{} }),
		"file with a path":                mutate(func(m map[string]any) { archive(m)["files"].([]any)[0].(map[string]any)["name"] = "p2p/node_key.json" }),
		"sizes do not add up":             mutate(func(m map[string]any) { archive(m)["uncompressedSize"] = 1 }),
		"producer control chars":          mutate(func(m map[string]any) { m["producer"].(map[string]any)["binaryVersion"] = "a\nb" }),
		"trailing data":                   append(append([]byte(nil), good...), []byte(`{"x":1}`)...),
		"not json":                        []byte("not json"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManifest(raw); err == nil {
				t.Fatalf("the manifest was accepted")
			}
		})
	}
}
