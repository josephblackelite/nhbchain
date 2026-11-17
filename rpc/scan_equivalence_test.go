package rpc

// Differential tests: the explorer scans now answer from the transaction-hash
// index and from per-block summaries instead of reading every block, and must
// return exactly what the scans of release/hardening-r1 returned. The reference
// copies of those scans (legacy_scan_reference_test.go) and the current code run
// over the same randomly built chains, including chains shorter and longer than
// the scan bound, and their results are compared as JSON.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func withBackfillLimit(tb testing.TB, limit int) {
	tb.Helper()
	prev := explorerHistoricalBackfillLimit
	explorerHistoricalBackfillLimit = limit
	tb.Cleanup(func() { explorerHistoricalBackfillLimit = prev })
}

// normalizeSnapshot blanks the fields that legitimately differ between two
// builds of the same snapshot (they read the clock and the mempool).
func normalizeSnapshot(s *ExplorerSnapshotResult) *ExplorerSnapshotResult {
	if s == nil {
		return nil
	}
	c := *s
	c.UpdatedAt = ""
	c.CurrentTime = 0
	return &c
}

func TestAddressActivityMatchesReferenceScan(t *testing.T) {
	for _, tc := range []struct {
		seed   int64
		blocks int
		limit  int // explorerHistoricalBackfillLimit
	}{
		{1, 150, 50000},
		{2, 150, 40}, // the scan bound falls inside the chain
		{3, 150, 7},
		{4, 60, 50000},
		{5, 1, 50000},
		{6, 300, 100},
	} {
		tc := tc
		t.Run(fmt.Sprintf("seed%d_blocks%d_limit%d", tc.seed, tc.blocks, tc.limit), func(t *testing.T) {
			withBackfillLimit(t, tc.limit)
			rc := buildRandomChain(t, tc.seed, tc.blocks, false)
			srv := newTestServer(t, rc.Node, nil, costServerConfig())
			ctx := context.Background()
			nonEmpty, admin := 0, 0
			for _, addr := range rc.allAddresses() {
				for _, limit := range []int{1, 2, 5, 50, 200} {
					want, wantErr := srv.legacyBuildAddressActivity(addr, limit)
					if wantErr == nil && len(want.Transactions) > 0 {
						nonEmpty++
						if addr == crypto.MustNewAddress(crypto.NHBPrefix, rc.Admin).String() {
							admin++
						}
					}
					// Twice: the first call fills the summary cache, the second reads it.
					for pass := 0; pass < 2; pass++ {
						got, gotErr := srv.buildAddressActivity(ctx, addr, limit)
						if (wantErr == nil) != (gotErr == nil) {
							t.Fatalf("%s limit %d pass %d: error mismatch: want %v got %v", addr, limit, pass, wantErr, gotErr)
						}
						if a, b := mustJSON(t, want), mustJSON(t, got); a != b {
							t.Fatalf("%s limit %d pass %d: history differs\nwant %s\n got %s", addr, limit, pass, a, b)
						}
					}
				}
			}
			if tc.blocks > 20 && (nonEmpty == 0 || admin == 0) {
				t.Fatalf("the chain gave the comparison nothing to compare (%d non-empty histories, %d for the admin wallet)", nonEmpty, admin)
			}
			// A malformed address fails the same way.
			_, wantErr := srv.legacyBuildAddressActivity("not-an-address", 5)
			_, gotErr := srv.buildAddressActivity(ctx, "not-an-address", 5)
			if wantErr == nil || gotErr == nil || wantErr.Error() != gotErr.Error() {
				t.Fatalf("bad address: want %v got %v", wantErr, gotErr)
			}
		})
	}
}

func TestExplorerSnapshotMatchesReferenceScan(t *testing.T) {
	for _, tc := range []struct {
		seed   int64
		blocks int
		limit  int
	}{
		{11, 150, 50000},
		{12, 150, 30},
		{13, 150, 8},
		{14, 400, 50000},
		{15, 25, 50000},
		{16, 3, 50000},
	} {
		tc := tc
		t.Run(fmt.Sprintf("seed%d_blocks%d_limit%d", tc.seed, tc.blocks, tc.limit), func(t *testing.T) {
			withBackfillLimit(t, tc.limit)
			rc := buildRandomChain(t, tc.seed, tc.blocks, false)
			srv := newTestServer(t, rc.Node, nil, costServerConfig())
			settleActivityIndex(srv)
			ctx := context.Background()
			withTxs, lookedBack := 0, 0
			for _, window := range []int{1, 2, 5, 30, 120, 400} {
				want, wantErr := srv.legacyBuildExplorerSnapshot(window)
				if wantErr == nil && len(want.LatestTransactions) > 0 {
					withTxs++
					// A transaction older than the window can only have come from the look-back.
					for _, tx := range want.LatestTransactions {
						if tx.BlockNumber+uint64(window) <= want.LatestHeight {
							lookedBack++
							break
						}
					}
				}
				for pass := 0; pass < 2; pass++ {
					got, gotErr := srv.buildExplorerSnapshot(ctx, window)
					if (wantErr == nil) != (gotErr == nil) {
						t.Fatalf("window %d pass %d: error mismatch: want %v got %v", window, pass, wantErr, gotErr)
					}
					if a, b := mustJSON(t, normalizeSnapshot(want)), mustJSON(t, normalizeSnapshot(got)); a != b {
						t.Fatalf("window %d pass %d: snapshot differs\nwant %s\n got %s", window, pass, a, b)
					}
				}
			}
			if tc.blocks >= 25 && tc.limit >= 30 && (withTxs == 0 || lookedBack == 0) {
				t.Fatalf("the chain gave the comparison nothing to compare (%d snapshots with transactions, %d that needed the look-back)", withTxs, lookedBack)
			}
		})
	}
}

func TestTxWindowStatsMatchesReferenceScan(t *testing.T) {
	for _, tc := range []struct {
		seed   int64
		blocks int
	}{{21, 150}, {22, 2}, {23, 1}, {24, 400}} {
		tc := tc
		t.Run(fmt.Sprintf("seed%d_blocks%d", tc.seed, tc.blocks), func(t *testing.T) {
			rc := buildRandomChain(t, tc.seed, tc.blocks, false)
			srv := newTestServer(t, rc.Node, nil, costServerConfig())
			ctx := context.Background()
			for _, now := range []int64{rc.LastTS - 40, rc.LastTS, rc.LastTS + 9, rc.LastTS + 500, rc.LastTS + 10_000_000} {
				for _, lookback := range []int64{1, 2, 3, 10, 59, 60, 200, 1000, 100_000, 1_000_000_000, 4_000_000_000_000_000_000} {
					want, wantErr := srv.legacyComputeTxWindowStats(lookback, now)
					for pass := 0; pass < 2; pass++ {
						got, gotErr := srv.computeTxWindowStatsAt(ctx, lookback, now)
						if (wantErr == nil) != (gotErr == nil) {
							t.Fatalf("now %d lookback %d: error mismatch: want %v got %v", now, lookback, wantErr, gotErr)
						}
						if a, b := mustJSON(t, want), mustJSON(t, got); a != b {
							t.Fatalf("now %d lookback %d pass %d: stats differ\nwant %s\n got %s", now, lookback, pass, a, b)
						}
					}
				}
			}
		})
	}
}

// legacyLatestTransactions is the loop of nhb_getLatestTransactions as it was.
func legacyLatestTransactions(chain *core.Blockchain, count int) []*types.Transaction {
	if count <= 0 {
		count = 20
	} else if count > 50 {
		count = 50
	}
	latestHeight := chain.GetHeight()
	var txs []*types.Transaction
	for i := uint64(0); i <= latestHeight && len(txs) < count; i++ {
		height := latestHeight - i
		block, err := chain.GetBlockByHeight(height)
		if err != nil {
			break
		}
		txs = append(txs, block.Transactions...)
	}
	if len(txs) > count {
		txs = txs[:count]
	}
	return txs
}

func TestLatestTransactionsMatchesReferenceScan(t *testing.T) {
	for _, tc := range []struct {
		seed   int64
		blocks int
	}{{31, 150}, {32, 5}, {33, 400}} {
		rc := buildRandomChain(t, tc.seed, tc.blocks, false)
		srv := newTestServer(t, rc.Node, nil, costServerConfig())
		for _, count := range []int{-3, 0, 1, 7, 20, 50, 51, 500} {
			for pass := 0; pass < 2; pass++ {
				code, body, _ := costRPC(srv, "203.0.113.9:1", "nhb_getLatestTransactions", count)
				if code != 200 {
					t.Fatalf("status %d: %s", code, body)
				}
				var resp struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				want := mustJSON(t, legacyLatestTransactions(rc.Node.Chain(), count))
				if strings.TrimSpace(string(resp.Result)) != want {
					t.Fatalf("seed %d count %d pass %d: latest transactions differ\nwant %.300s\n got %.300s", tc.seed, count, pass, want, resp.Result)
				}
			}
		}
	}
}

// TestFindTransactionMatchesReferenceScan covers hits and misses of every kind:
// a hash the index knows, hashes in every spelling the lookup accepts, hashes it
// does not (unknown, malformed, the wrong length), on an index that is complete,
// on one whose completeness marker is missing (the lookup must fall back to the
// scan and still find everything the scan could find), and on one with a wrong
// entry.
func TestFindTransactionMatchesReferenceScan(t *testing.T) {
	type mutate func(t *testing.T, rc *randChain)
	setups := map[string]mutate{
		"complete index": func(t *testing.T, rc *randChain) {},
		"marker missing, entries deleted": func(t *testing.T, rc *randChain) {
			deleteIndex(t, rc, true)
		},
		"marker present, one entry pointing at the wrong block": func(t *testing.T, rc *randChain) {
			hx := rc.Hashes[0]
			wrong := make([]byte, 8)
			wrong[7] = byte(rc.HashAt[hx]%7) + 1
			raw, _ := hex.DecodeString(hx)
			if err := rc.DB.Put(append([]byte("txhash:"), raw...), wrong); err != nil {
				t.Fatalf("corrupt entry: %v", err)
			}
		},
	}
	for name, setup := range setups {
		name, setup := name, setup
		for _, limit := range []int{50000, 25} {
			limit := limit
			t.Run(fmt.Sprintf("%s/limit%d", name, limit), func(t *testing.T) {
				withBackfillLimit(t, limit)
				rc := buildRandomChain(t, 41, 200, false)
				setup(t, rc)
				srv := newTestServer(t, rc.Node, nil, costServerConfig())
				inputs := []string{"", "  ", "0x", "zz", "0x" + strings.Repeat("ab", 31), strings.Repeat("ab", 33), strings.Repeat("0", 64)}
				for _, hx := range rc.Hashes {
					inputs = append(inputs, hx, "0x"+hx, strings.ToUpper(hx), "0x"+strings.ToUpper(hx), "  0x"+hx+"  ", "0X"+hx, hx[:63], hx+"0")
				}
				ctx := context.Background()
				for _, in := range inputs {
					wantTx, wantHash, wantBlock, wantHeight, wantErr := srv.legacyFindTransaction(in)
					gotTx, gotHash, gotBlock, gotHeight, gotErr := srv.findTransaction(ctx, in)
					if (wantErr == nil) != (gotErr == nil) {
						t.Fatalf("%q: error mismatch: want %v got %v", in, wantErr, gotErr)
					}
					if wantHash != gotHash || wantHeight != gotHeight || !bytes.Equal(wantBlock, gotBlock) {
						t.Fatalf("%q: want (%s, %d, %x) got (%s, %d, %x)", in, wantHash, wantHeight, wantBlock, gotHash, gotHeight, gotBlock)
					}
					if (wantTx == nil) != (gotTx == nil) || (wantTx != nil && mustJSON(t, wantTx) != mustJSON(t, gotTx)) {
						t.Fatalf("%q: transaction differs", in)
					}
				}
			})
		}
	}
}

// TestSnapshotRefreshesMatchReferenceAsTheChainGrows is the loop's situation:
// one server rebuilds the snapshot after every new block, keeping what it has
// learned about the older blocks, while the chain keeps getting blocks with
// every kind of transaction. After every step the snapshot must be the one the
// reference scan builds from scratch, for windows of every size, with the
// look-back bound both far away and inside the chain.
func TestSnapshotRefreshesMatchReferenceAsTheChainGrows(t *testing.T) {
	for _, limit := range []int{50000, 60, 9} {
		limit := limit
		t.Run(fmt.Sprintf("limit%d", limit), func(t *testing.T) {
			withBackfillLimit(t, limit)
			rc := buildRandomChain(t, 111, 30, false)
			srv := quietServer(t, rc.Node, costServerConfig())
			ctx := context.Background()
			sawEarlyExit, sawLookBack, steps := false, false, 0
			for step := 0; step < 90; step++ {
				settleActivityIndex(srv)
				for _, window := range []int{1, 3, 20, 120} {
					want, wantErr := srv.legacyBuildExplorerSnapshot(window)
					got, gotErr := srv.buildExplorerSnapshot(ctx, window)
					if (wantErr == nil) != (gotErr == nil) {
						t.Fatalf("step %d window %d: error mismatch: want %v got %v", step, window, wantErr, gotErr)
					}
					if a, b := mustJSON(t, normalizeSnapshot(want)), mustJSON(t, normalizeSnapshot(got)); a != b {
						t.Fatalf("step %d window %d: snapshot differs\nwant %s\n got %s", step, window, a, b)
					}
					if len(want.LatestTransactions) >= explorerDefaultLatestTxCount && len(want.ActiveAddresses) >= explorerActiveAddressLimit {
						sawEarlyExit = true
					}
					for _, tx := range want.LatestTransactions {
						if tx.BlockNumber+uint64(window) <= want.LatestHeight {
							sawLookBack = true
						}
					}
					steps++
				}
				// The chain grows by one to three blocks, of every kind.
				rc.Grow(1 + step%3)
			}
			if limit == 50000 && (!sawEarlyExit || !sawLookBack) {
				t.Fatalf("the run never reached the look-back's early exit (%v) or never needed the look-back (%v); it proves nothing", sawEarlyExit, sawLookBack)
			}
			if steps == 0 {
				t.Fatalf("no snapshots were compared")
			}
		})
	}
}

func TestRecentTPSMatchesReference(t *testing.T) {
	for _, tc := range []struct {
		seed   int64
		blocks int
	}{{121, 1}, {122, 4}, {123, 10}, {124, 11}, {125, 200}} {
		rc := buildRandomChain(t, tc.seed, tc.blocks, false)
		srv := quietServer(t, rc.Node, costServerConfig())
		for step := 0; step < 15; step++ {
			want := estimateRecentTPS(rc.Node)
			got, err := srv.recentTPS(context.Background())
			if err != nil || got != want {
				t.Fatalf("seed %d height %d: want %v got %v (%v)", tc.seed, rc.Height, want, got, err)
			}
			rc.Grow(1 + step%2)
		}
	}
	// The same through the RPC method.
	rc := buildRandomChain(t, 126, 60, false)
	srv := quietServer(t, rc.Node, costServerConfig())
	call := doRPC(t, srv, nil, "203.0.113.5:1", "nhb_getNetworkStats")
	if call.Code != 200 || call.Resp.Error != nil {
		t.Fatalf("nhb_getNetworkStats: HTTP %d %s", call.Code, call.Body)
	}
	var result struct {
		Result struct {
			TPS float64 `json:"tps"`
		} `json:"result"`
	}
	decodeInto(t, call.Body, &result)
	if want := estimateRecentTPS(rc.Node); result.Result.TPS != want {
		t.Fatalf("nhb_getNetworkStats tps: want %v got %v", want, result.Result.TPS)
	}
}
