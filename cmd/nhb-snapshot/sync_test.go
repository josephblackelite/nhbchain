package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	timestamp func() int64
	chainID   uint64
	genesis   string
	down      int32 // net_info fails while > 0
}

func (n *fakeNode) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "net_info":
			if atomic.AddInt32(&n.down, -1) >= 0 {
				http.Error(w, "starting", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"nodeId":"0xabc","chainId":%d,"genesisHash":%q,"peerCounts":{"total":2}}}`, n.chainID, n.genesis)
		case "nhb_getLatestBlocks":
			n.mu.Lock()
			if n.height < n.max {
				n.height += n.step
				if n.height > n.max {
					n.height = n.max
				}
			}
			h := n.height
			n.mu.Unlock()
			block := types.NewBlock(&types.BlockHeader{Height: h, Timestamp: n.timestamp(), PrevHash: []byte{1}, StateRoot: []byte{2}}, nil)
			out, _ := json.Marshal([]*types.Block{block})
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
	local := &fakeNode{height: 100, step: 40, max: 1000, timestamp: func() int64 { return time.Now().Unix() }, chainID: 7, genesis: "aa"}
	ref := &fakeNode{height: 1000, max: 1000, timestamp: func() int64 { return time.Now().Unix() }, chainID: 7, genesis: "aa"}
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
