package rpc

// TestQueryCostTable measures the read methods whose cost grows with chain
// height, on a chain shaped like the live one, and prints them ranked by cost.
// It is a measurement, not an assertion, and it is slow, so it only runs when
// asked:
//
//	NHB_RPC_COST_TABLE=1 go test ./rpc -run TestQueryCostTable -v -count=1 -timeout 30m
//
// NHB_RPC_COST_BLOCKS sets the chain length (default: the live chain's 196,693
// blocks) and NHB_RPC_COST_MEM=1 keeps the store in memory instead of on disk.
// The measurement runs on one core (GOMAXPROCS=1) with nothing else running, so
// wall time is CPU time: the number that matters on a small validator host.

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

type costCase struct {
	method string
	label  string
	params func(fx *costChain, rep int) []any
	reps   int
}

type costRow struct {
	method, label string
	median, min   time.Duration
	max           time.Duration
	allocMB       float64
	status        int
	errCode       int
	bodyBytes     int
}

func envInt(name string, def int) int {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	return def
}

func costErrCode(body []byte) int {
	idx := strings.Index(string(body), `"code":`)
	if idx < 0 {
		return 0
	}
	rest := string(body[idx+len(`"code":`):])
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(rest[:end]))
	return v
}

// warmCostServer waits for the server's background loop to finish its first
// pass (activity index + first snapshot) so that it is not still running while
// methods are timed.
func warmCostServer(t testing.TB, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for srv.cachedExplorerSnapshot(explorerDefaultRecentBlocks) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("explorer snapshot loop never produced its first snapshot")
		}
		time.Sleep(200 * time.Millisecond)
	}
	for {
		srv.advanceExplorerActivityIndex()
		if _, _, complete := srv.currentExplorerActivityTotals(); complete {
			break
		}
	}
	time.Sleep(3 * time.Second)
}

func TestQueryCostTable(t *testing.T) {
	if os.Getenv("NHB_RPC_COST_TABLE") == "" {
		t.Skip("set NHB_RPC_COST_TABLE=1 to run the read-method cost table")
	}
	blocks := envInt("NHB_RPC_COST_BLOCKS", liveChainBlocks)

	// Let the first pass of the activity index cover the whole chain in one go
	// so it is not still running in the background while methods are timed.
	prevBatch := explorerActivityBatchSize
	explorerActivityBatchSize = 1 << 30
	t.Cleanup(func() { explorerActivityBatchSize = prevBatch })

	buildStart := time.Now()
	fx := buildCostChain(t, costChainOpts{Blocks: blocks, LevelDB: os.Getenv("NHB_RPC_COST_MEM") == "", Seed: 42})
	t.Logf("chain: %d blocks built in %s (height %d, %d user-facing, %d transactions total)",
		blocks, time.Since(buildStart).Round(time.Millisecond), fx.Height, len(fx.UserTx), len(fx.AllTx))

	srv := newTestServer(t, fx.Node, nil, costServerConfig())
	warmCostServer(t, srv)

	prevProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })
	runtime.GC()

	hex0x := func(h string) string { return "0x" + h }
	tip := int(fx.Height)
	cases := []costCase{
		{"nhb_getTransactionReceipt", "recent hash (in the index)", func(f *costChain, _ int) []any { return []any{hex0x(f.RecentTx)} }, 5},
		{"nhb_getTransactionReceipt", "hash beyond the scan window (in the index)", func(f *costChain, _ int) []any { return []any{hex0x(f.OldTx)} }, 5},
		{"nhb_getTransactionReceipt", "unknown hash", func(f *costChain, _ int) []any { return []any{hex0x(f.UnknownTx)} }, 3},
		{"nhb_getTransaction", "recent hash (in the index)", func(f *costChain, _ int) []any { return []any{hex0x(f.RecentTx)} }, 5},
		{"nhb_getTransaction", "unknown hash", func(f *costChain, _ int) []any { return []any{hex0x(f.UnknownTx)} }, 3},
		{"nhb_searchExplorer", "unknown 64-hex", func(f *costChain, _ int) []any { return []any{hex0x(f.UnknownTx)} }, 3},
		{"nhb_searchExplorer", "block hash", func(f *costChain, _ int) []any { return []any{f.RecentBlockHash} }, 3},
		{"nhb_searchExplorer", "block height", func(f *costChain, _ int) []any { return []any{strconv.Itoa(tip - 100)} }, 5},
		{"nhb_searchExplorer", "active address", func(f *costChain, _ int) []any { return []any{f.ActiveAddr} }, 3},
		{"nhb_searchExplorer", "dormant address", func(f *costChain, _ int) []any { return []any{f.DormantAddr} }, 3},
		{"nhb_getTransactionHistory", "active address, limit 50", func(f *costChain, _ int) []any { return []any{f.ActiveAddr, 50} }, 3},
		{"nhb_getTransactionHistory", "dormant address, limit 50", func(f *costChain, _ int) []any { return []any{f.DormantAddr, 50} }, 3},
		{"nhb_getTransactionHistory", "dormant address, limit 200", func(f *costChain, _ int) []any { return []any{f.DormantAddr, 200} }, 3},
		{"nhb_getAddressActivity", "active address", func(f *costChain, _ int) []any { return []any{f.ActiveAddr} }, 3},
		{"nhb_getAddressActivity", "dormant address", func(f *costChain, _ int) []any { return []any{f.DormantAddr} }, 3},
		{"nhb_getExplorerSnapshot", "default window 120 (served from the loop's cache)", func(f *costChain, _ int) []any { return []any{120} }, 5},
		{"nhb_getExplorerSnapshot", "window ~100 (a different window each call)", func(f *costChain, rep int) []any { return []any{100 + rep} }, 3},
		{"nhb_getExplorerSnapshot", "window ~400 (a different window each call)", func(f *costChain, rep int) []any { return []any{400 - rep} }, 3},
		{"nhb_getLatestBlocks", "count 20", func(f *costChain, _ int) []any { return []any{20} }, 5},
		{"nhb_getLatestTransactions", "count 50", func(f *costChain, _ int) []any { return []any{50} }, 5},
		{"nhb_txWindowStats", "lookback 1 hour (a new lookback each call)", func(f *costChain, rep int) []any { return []any{map[string]any{"lookbackSeconds": 3600 + rep}} }, 3},
		{"nhb_txWindowStats", "lookback 1 day (a new lookback each call)", func(f *costChain, rep int) []any { return []any{map[string]any{"lookbackSeconds": 86400 + rep}} }, 3},
		{"nhb_txWindowStats", "lookback 10^9 s = whole chain (a new lookback each call)", func(f *costChain, rep int) []any {
			return []any{map[string]any{"lookbackSeconds": 1_000_000_001 + rep}}
		}, 3},
		{"lending_getMarket", "no pool", nil, 3},
		{"lend_getPools", "no pool", nil, 3},
		{"lending_getUserAccount", "dormant address", func(f *costChain, _ int) []any { return []any{f.DormantAddr} }, 3},
		{"market_listOpenListings", "empty book", nil, 5},
		{"nhb_getNetworkStats", "", nil, 5},
		{"nhb_getBalance", "dormant address", func(f *costChain, _ int) []any { return []any{f.DormantAddr} }, 5},
		{"nhb_getValidatorSet", "", nil, 5},
		{"nhb_getTotalSupply", "", nil, 5},
	}

	remote := "203.0.113.50:40000"
	rows := make([]costRow, 0, len(cases))
	for _, c := range cases {
		durs := make([]time.Duration, 0, c.reps)
		var status, errCode, size int
		var ms0, ms1 runtime.MemStats
		runtime.ReadMemStats(&ms0)
		for i := 0; i < c.reps; i++ {
			var params []any
			if c.params != nil {
				params = c.params(fx, i)
			}
			code, body, d := costRPC(srv, remote, c.method, params...)
			durs = append(durs, d)
			status, errCode, size = code, costErrCode(body), len(body)
		}
		runtime.ReadMemStats(&ms1)
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		rows = append(rows, costRow{
			method: c.method, label: c.label,
			median: durs[len(durs)/2], min: durs[0], max: durs[len(durs)-1],
			allocMB: float64(ms1.TotalAlloc-ms0.TotalAlloc) / float64(c.reps) / (1 << 20),
			status:  status, errCode: errCode, bodyBytes: size,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].median > rows[j].median })

	var sb strings.Builder
	fmt.Fprintf(&sb, "\nRead-method cost on a %d-block chain (1 core, idle machine); ranked by median wall time\n", fx.Height)
	fmt.Fprintf(&sb, "%-4s %-27s %-52s %10s %10s %10s %9s %s\n", "rank", "method", "case", "median ms", "min ms", "max ms", "alloc MB", "http/code/bytes")
	for i, r := range rows {
		fmt.Fprintf(&sb, "%-4d %-27s %-52s %10.2f %10.2f %10.2f %9.2f %d/%d/%d\n",
			i+1, r.method, r.label,
			float64(r.median.Microseconds())/1000, float64(r.min.Microseconds())/1000, float64(r.max.Microseconds())/1000,
			r.allocMB, r.status, r.errCode, r.bodyBytes)
	}
	t.Log(sb.String())
	if out := strings.TrimSpace(os.Getenv("NHB_RPC_COST_OUT")); out != "" {
		_ = os.WriteFile(out, []byte(sb.String()), 0o644)
	}
}

// TestQueryCostStateLockStall measures how long everything else that needs the
// node's state lock is held up while one public lending read is in flight: the
// read replays the chain inside the lock. Same switch and settings as the table.
func TestQueryCostStateLockStall(t *testing.T) {
	if os.Getenv("NHB_RPC_COST_TABLE") == "" {
		t.Skip("set NHB_RPC_COST_TABLE=1 to run the state-lock stall measurement")
	}
	blocks := envInt("NHB_RPC_COST_BLOCKS", liveChainBlocks)
	prevBatch := explorerActivityBatchSize
	explorerActivityBatchSize = 1 << 30
	t.Cleanup(func() { explorerActivityBatchSize = prevBatch })
	fx := buildCostChain(t, costChainOpts{Blocks: blocks, LevelDB: os.Getenv("NHB_RPC_COST_MEM") == "", Seed: 42})
	srv := newTestServer(t, fx.Node, nil, costServerConfig())
	warmCostServer(t, srv)

	addr, err := crypto.DecodeAddress(fx.DormantAddr)
	if err != nil {
		t.Fatalf("decode probe address: %v", err)
	}
	probe := func() time.Duration {
		start := time.Now()
		if _, err := fx.Node.GetAccount(addr.Bytes()); err != nil {
			t.Fatalf("get account: %v", err)
		}
		return time.Since(start)
	}
	var idleMax time.Duration
	for i := 0; i < 2000; i++ {
		if d := probe(); d > idleMax {
			idleMax = d
		}
	}

	for _, method := range []string{"lending_getMarket", "lend_getPools"} {
		done := make(chan time.Duration, 1)
		go func() {
			_, _, d := costRPC(srv, "203.0.113.60:41000", method)
			done <- d
		}()
		var stallMax time.Duration
		var call time.Duration
		samples := 0
	poll:
		for {
			select {
			case call = <-done:
				break poll
			default:
				samples++
				if d := probe(); d > stallMax {
					stallMax = d
				}
			}
		}
		t.Logf("%s: the call took %s; the longest a state read (nhb_getBalance's lookup) waited meanwhile: %s (idle max %s, %d samples)",
			method, call.Round(time.Millisecond), stallMax.Round(time.Millisecond), idleMax.Round(time.Microsecond), samples)
	}
}
