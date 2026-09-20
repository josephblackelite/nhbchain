package rpc

// The load test: 200 clients send heavy public queries as fast as they can
// while the node keeps producing blocks, on two CPUs (the size of a small
// validator). Block production must stay within a stated multiple of its
// idle latency, and the queries the node cannot afford must be refused promptly
// with the documented error rather than left to pile up.
//
// The store is made to look like one whose transaction-hash index is not
// complete, so that an unknown hash is a real 50,000-block scan and the pool is
// what stands between 200 such scans and the node's CPU, and the lending read
// (which used to replay the chain inside the state lock) is included as well.
//
// It uses only the server's public behaviour (ServeHTTP and the default
// configuration), so the same test can be pointed at an older tree to see what
// it does there.

import (
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	loadClients       = 200
	loadChainBlocks   = 60000
	loadWindow        = 3 * time.Second
	loadIdleBlocks    = 300
	loadMinBlocks     = 40 // blocks that must get produced during the load window
	loadLatencyMult   = 10 // loaded p95 may be this many times the idle p95 ...
	loadLatencyFloor  = 25 * time.Millisecond
	loadRejectedP95   = 500 * time.Millisecond
	loadTxPerBlock    = 4
	loadTransferValue = 32
)

type blockProducer struct {
	t      *testing.T
	fx     *costChain
	key    *crypto.PrivateKey
	to     []byte
	nonce  uint64
	blocks int
}

func newBlockProducer(t *testing.T, fx *costChain) *blockProducer {
	t.Helper()
	key := costSeededKey(777, 1)
	sender := costAddr(key)
	if err := fx.Node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(sender, &types.Account{BalanceNHB: big.NewInt(1_000_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("fund the sender: %v", err)
	}
	return &blockProducer{t: t, fx: fx, key: key, to: costAddr(costSeededKey(777, 2))}
}

// produce builds and commits count blocks, or until stop is closed, and returns
// how long each took (the transactions are signed before the clock starts).
func (p *blockProducer) produce(count int, stop <-chan struct{}) []time.Duration {
	restore := silenceStdout(p.t)
	defer restore()
	out := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		select {
		case <-stop:
			return out
		default:
		}
		txs := make([]*types.Transaction, 0, loadTxPerBlock)
		for j := 0; j < loadTxPerBlock; j++ {
			txs = append(txs, costSignedTx(p.t, p.key, types.TxTypeTransfer, p.nonce, p.to, big.NewInt(loadTransferValue), nil))
			p.nonce++
		}
		start := time.Now()
		block, err := p.fx.Node.CreateBlock(txs)
		if err != nil {
			p.t.Errorf("create block: %v", err)
			return out
		}
		if err := p.fx.Node.CommitBlock(block); err != nil {
			p.t.Errorf("commit block: %v", err)
			return out
		}
		out = append(out, time.Since(start))
		p.blocks++
		time.Sleep(time.Millisecond)
	}
	return out
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}

func summarize(d []time.Duration) (p50, p95, max time.Duration) {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return percentile(s, 0.5), percentile(s, 0.95), percentile(s, 1)
}

func TestHeavyQueryLoadDoesNotStarveBlockProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("the load test needs two CPUs to stand for a small validator")
	}
	prevProcs := runtime.GOMAXPROCS(2)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })

	fx := buildCostChain(t, costChainOpts{Blocks: loadChainBlocks, Seed: 5})
	// The transaction index of this store is not complete, so a hash that is in no
	// block is a scan of the last 50,000 (the case the pool exists for).
	batch := fx.DB.NewBatch()
	if err := batch.Delete([]byte("txHashIndexBackfillDone")); err != nil {
		t.Fatalf("drop the marker: %v", err)
	}
	if err := batch.Write(); err != nil {
		t.Fatalf("drop the marker: %v", err)
	}
	prevBatch := explorerActivityBatchSize
	explorerActivityBatchSize = 1 << 30
	t.Cleanup(func() { explorerActivityBatchSize = prevBatch })
	srv := newTestServer(t, fx.Node, nil, costServerConfig())
	warmCostServer(t, srv)
	// Bring the lending index (or, on an older tree, do the one full replay) up to date.
	if call := doRPC(t, srv, nil, "198.51.100.1:1", "lending_getMarket"); call.Code != http.StatusOK {
		t.Fatalf("lending_getMarket: HTTP %d %s", call.Code, call.Body)
	}
	producer := newBlockProducer(t, fx)

	// Idle: how long a block takes with nothing else going on.
	idle := producer.produce(loadIdleBlocks, nil)
	if len(idle) < loadIdleBlocks {
		t.Fatalf("idle production stopped after %d blocks", len(idle))
	}
	idle50, idle95, idleMax := summarize(idle)

	// Loaded: 200 clients, each with an address of its own, hammer the queries.
	type outcome struct {
		method   string
		code     int
		rpcCode  int
		retry    string
		dataOK   bool
		duration time.Duration
	}
	var mu sync.Mutex
	var outcomes []outcome
	stop := make(chan struct{})
	var started sync.WaitGroup
	var wg sync.WaitGroup
	var inflight atomic.Int64
	for c := 0; c < loadClients; c++ {
		c := c
		wg.Add(1)
		started.Add(1)
		go func() {
			defer wg.Done()
			started.Done()
			remote := fmt.Sprintf("10.%d.%d.%d:40000", 1+c/250, (c/50)%5, 1+c%50)
			rng := rand.New(rand.NewSource(int64(c)))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				var call rpcCall
				var method string
				inflight.Add(1)
				switch (c + i) % 6 {
				case 0:
					method = "nhb_getTransactionReceipt"
					call = doRPC(t, srv, nil, remote, method, fmt.Sprintf("0x%064x", uint64(c)<<32|uint64(i)+1))
				case 1:
					method = "nhb_getTransaction"
					call = doRPC(t, srv, nil, remote, method, fmt.Sprintf("0x%064x", uint64(c)<<32|uint64(i)+2))
				case 2:
					method = "nhb_searchExplorer"
					call = doRPC(t, srv, nil, remote, method, fmt.Sprintf("0x%064x", uint64(c)<<32|uint64(i)+3))
				case 3:
					method = "lending_getMarket"
					call = doRPC(t, srv, nil, remote, method)
				case 4:
					method = "nhb_txWindowStats"
					call = doRPC(t, srv, nil, remote, method, map[string]any{"lookbackSeconds": 1_000_000 + c*1000 + i})
				default:
					method = "nhb_getExplorerSnapshot"
					call = doRPC(t, srv, nil, remote, method, 1+(c*7+i)%400)
				}
				inflight.Add(-1)
				o := outcome{method: method, code: call.Code, rpcCode: call.errCode(), retry: call.Header.Get("Retry-After"), duration: call.Duration}
				if data, ok := errData(call); ok {
					o.dataOK = data["reason"] != nil && data["retryAfterMs"] != nil
				}
				mu.Lock()
				outcomes = append(outcomes, o)
				mu.Unlock()
				// Each client stays within what the per-address limit would let it send
				// (120 a minute per method, six methods, so about one call every 80ms):
				// a flood of trivial requests is the front door's business, not the
				// pool's, and this test is about heavy ones.
				time.Sleep(time.Duration(60+rng.Intn(40)) * time.Millisecond)
			}
		}()
	}
	started.Wait()
	loaded := producer.produce(1<<30, timerChan(loadWindow))
	close(stop)
	drainStart := time.Now()
	wg.Wait()
	drain := time.Since(drainStart)
	load50, load95, loadMax := summarize(loaded)

	mu.Lock()
	total, ok, refused, timedOut, other := len(outcomes), 0, 0, 0, 0
	var refusedDur []time.Duration
	badRefusal := 0
	for _, o := range outcomes {
		switch {
		case o.code == http.StatusOK && o.rpcCode == 0:
			ok++
		case o.code == http.StatusTooManyRequests && o.rpcCode == codeRateLimited:
			refused++
			refusedDur = append(refusedDur, o.duration)
			if o.retry == "" || !o.dataOK {
				badRefusal++
			}
		case o.code == http.StatusServiceUnavailable:
			timedOut++
		default:
			other++
		}
	}
	mu.Unlock()
	ref50, ref95, refMax := summarize(refusedDur)
	perMethod := map[string][]time.Duration{}
	mu.Lock()
	for _, o := range outcomes {
		perMethod[o.method+fmt.Sprintf(" [%d]", o.code)] = append(perMethod[o.method+fmt.Sprintf(" [%d]", o.code)], o.duration)
	}
	mu.Unlock()
	names := make([]string, 0, len(perMethod))
	for name := range perMethod {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p50, p95, max := summarize(perMethod[name])
		t.Logf("  %-34s %5d calls  p50 %-10s p95 %-10s max %s", name, len(perMethod[name]), p50, p95, max)
	}
	t.Logf("idle:   %d blocks, p50 %s p95 %s max %s", len(idle), idle50, idle95, idleMax)
	t.Logf("loaded: %d blocks in %s, p50 %s p95 %s max %s", len(loaded), loadWindow, load50, load95, loadMax)
	t.Logf("queries: %d answered, %d ok, %d refused (p50 %s p95 %s max %s), %d timed out, %d other; drain %s",
		total, ok, refused, ref50, ref95, refMax, timedOut, other, drain.Round(time.Millisecond))

	limit := time.Duration(loadLatencyMult) * idle95
	if limit < loadLatencyFloor {
		limit = loadLatencyFloor
	}
	if len(loaded) < loadMinBlocks {
		t.Fatalf("block production stalled under load: %d blocks in %s (want at least %d); p95 %s, max %s", len(loaded), loadWindow, loadMinBlocks, load95, loadMax)
	}
	if load95 > limit {
		t.Fatalf("block production p95 under load is %s; the limit is %s (idle p95 %s x %d, at least %s)", load95, limit, idle95, loadLatencyMult, loadLatencyFloor)
	}
	if refused == 0 {
		t.Fatalf("none of %d heavy queries was refused; the pool did not engage", total)
	}
	if badRefusal > 0 {
		t.Fatalf("%d refusals lacked a Retry-After header or a reason and retry hint in the error data", badRefusal)
	}
	if ref95 > loadRejectedP95 {
		t.Fatalf("a refused query took %s at p95 to be refused; it should be prompt (limit %s)", ref95, loadRejectedP95)
	}
	if other > 0 {
		t.Fatalf("%d queries ended with something other than a result, the documented refusal or the documented timeout", other)
	}

	// Nothing may be left holding a slot: a heavy query from a new client is served.
	if call := doRPC(t, srv, nil, "198.51.100.77:1", "lending_getMarket"); call.Code != http.StatusOK {
		t.Fatalf("after the load a heavy query was still refused: HTTP %d %s", call.Code, call.Body)
	}
}

func timerChan(d time.Duration) <-chan struct{} {
	c := make(chan struct{})
	go func() {
		time.Sleep(d)
		close(c)
	}()
	return c
}

func errData(c rpcCall) (map[string]interface{}, bool) {
	if c.Resp.Error == nil {
		return nil, false
	}
	data, ok := c.Resp.Error.Data.(map[string]interface{})
	return data, ok
}
