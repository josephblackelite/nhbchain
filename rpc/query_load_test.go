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
	loadClients     = 200
	loadChainBlocks = 60000
	loadWindow      = 3 * time.Second
	loadIdleBlocks  = 300
	loadMinBlocks   = 40 // blocks that must get produced during the load window
	loadLatencyMult = 10 // loaded p95 may be this many times the idle p95 ...
	// ... or this long, whichever is more: a block that commits in a fifth of a
	// second is not being starved (the load it is measured against stalls it for
	// minutes), and a shorter limit is inside the noise of a busy machine.
	loadLatencyFloor = 200 * time.Millisecond
	loadRejectedP95  = time.Second
	// loadAttempts is how many measurements are taken before timings that are a
	// little over the limits fail the test.
	loadAttempts      = 3
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

// loadOutcome is what one client saw of one query.
type loadOutcome struct {
	method   string
	code     int
	rpcCode  int
	retry    string
	dataOK   bool
	duration time.Duration
}

// loadRun is what one measurement of the node under load found.
type loadRun struct {
	idle, loaded []time.Duration
	idle95       time.Duration
	load95       time.Duration
	limit        time.Duration // what the loaded p95 may be

	total, ok, refused, timedOut, other int
	badRefusal                          int
	ref95                               time.Duration
}

// measureLoad produces blocks with nothing else going on, then produces them
// for loadWindow while 200 clients, each with an address of its own, hammer the
// heavy queries, and reports both.
func measureLoad(t *testing.T, srv *Server, producer *blockProducer) *loadRun {
	t.Helper()
	// Idle: how long a block takes with nothing else going on.
	idle := producer.produce(loadIdleBlocks, nil)
	if len(idle) < loadIdleBlocks {
		t.Fatalf("idle production stopped after %d blocks", len(idle))
	}
	idle50, idle95, idleMax := summarize(idle)

	var mu sync.Mutex
	var outcomes []loadOutcome
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
				o := loadOutcome{method: method, code: call.Code, rpcCode: call.errCode(), retry: call.Header.Get("Retry-After"), duration: call.Duration}
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

	run := &loadRun{idle: idle, loaded: loaded, idle95: idle95, load95: load95}
	mu.Lock()
	defer mu.Unlock()
	run.total = len(outcomes)
	var refusedDur []time.Duration
	perMethod := map[string][]time.Duration{}
	for _, o := range outcomes {
		key := fmt.Sprintf("%s [%d]", o.method, o.code)
		perMethod[key] = append(perMethod[key], o.duration)
		switch {
		case o.code == http.StatusOK && o.rpcCode == 0:
			run.ok++
		case o.code == http.StatusTooManyRequests && o.rpcCode == codeRateLimited:
			run.refused++
			refusedDur = append(refusedDur, o.duration)
			if o.retry == "" || !o.dataOK {
				run.badRefusal++
			}
		case o.code == http.StatusServiceUnavailable:
			run.timedOut++
		default:
			run.other++
		}
	}
	var ref50, refMax time.Duration
	ref50, run.ref95, refMax = summarize(refusedDur)
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
		run.total, run.ok, run.refused, ref50, run.ref95, refMax, run.timedOut, run.other, drain.Round(time.Millisecond))

	run.limit = time.Duration(loadLatencyMult) * idle95
	if run.limit < loadLatencyFloor {
		run.limit = loadLatencyFloor
	}
	return run
}

// timingProblem says what is wrong with the measured timings, if anything, and
// whether it is too big to be noise (a machine that is busy with other work
// makes a measurement worse by a small multiple, not by a hundredfold).
func (r *loadRun) timingProblem() (problem string, gross bool) {
	if len(r.loaded) < loadMinBlocks {
		return fmt.Sprintf("block production stalled under load: %d blocks in %s (want at least %d); p95 %s", len(r.loaded), loadWindow, loadMinBlocks, r.load95),
			len(r.loaded) < loadMinBlocks/4 || r.load95 > 20*r.limit
	}
	if r.load95 > r.limit {
		return fmt.Sprintf("block production p95 under load is %s; the limit is %s (idle p95 %s x %d, at least %s)", r.load95, r.limit, r.idle95, loadLatencyMult, loadLatencyFloor),
			r.load95 > 20*r.limit
	}
	if r.ref95 > loadRejectedP95 {
		return fmt.Sprintf("a refused query took %s at p95 to be refused; it should be prompt (limit %s)", r.ref95, loadRejectedP95), false
	}
	return "", false
}

// TestHeavyQueryLoadDoesNotStarveBlockProduction measures how block production
// fares under the load. The timings of a machine that is busy with other work
// (other tests in the same run) are noisy, so a measurement whose timings miss
// the limits by a little is taken again, up to loadAttempts times, and the test
// passes when one of them is within them. A measurement that misses by a wide
// margin, or that shows a refusal without its hint, a query that ended in
// something the pool does not document, or no refusal at all, fails at once.
// The tree this test was written against stalls block production for minutes
// (one block in the whole window), so what the test rules out is nowhere near
// the limits.
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

	for attempt := 1; ; attempt++ {
		run := measureLoad(t, srv, producer)
		// What holds however busy the machine is.
		if run.badRefusal > 0 {
			t.Fatalf("%d refusals lacked a Retry-After header or a reason and retry hint in the error data", run.badRefusal)
		}
		if run.other > 0 {
			t.Fatalf("%d queries ended with something other than a result, the documented refusal or the documented timeout", run.other)
		}
		problem, gross := run.timingProblem()
		if gross {
			t.Fatalf("%s (attempt %d of %d)", problem, attempt, loadAttempts)
		}
		if run.refused == 0 {
			t.Fatalf("none of %d heavy queries was refused; the pool did not engage", run.total)
		}
		if problem == "" {
			break
		}
		if attempt == loadAttempts {
			t.Fatalf("%s (attempt %d of %d)", problem, attempt, loadAttempts)
		}
		t.Logf("attempt %d of %d: %s; measuring again", attempt, loadAttempts, problem)
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
