package bft

// Block cadence on an idle chain, measured on two real engines.
//
// What decides how often an idle chain commits a block is not written down
// anywhere in the engine: nothing in it waits between one block and the next. A
// round for the next height starts the moment the last one commits, so a height
// takes as long as the messages, the block build and the commit take (a few tens of
// milliseconds between two validators in step), and the pace a chain keeps over
// minutes is that speed multiplied by the share of the time the pair spends making
// blocks rather than waiting out a round that failed: a round that fails lasts the
// whole commit timer (4 s by default), and a height whose validators are not in the
// same round can go through many of them. What keeps the pair out of failed rounds
// therefore decides the pace, several-fold: an engine that loses fewer rounds commits
// several times as many blocks a second with nothing else changed.
//
// The rig below is the part of the engine's surroundings that decides this, and
// nothing else: two engines (the real ones, with their real timers, signatures and
// message handling), each on a chain double that takes the time a build, a validation
// and a commit take, joined by links that deliver in order, one message at a time,
// after a one-way delay -- the way a peer's read loop hands a message to the engine --
// including the committed block each engine sends to its peer, which commits it
// through the sync path when it has not committed the height itself. A block that the
// chain already holds is committed already, and committing it again -- which is what the
// engine and the sync path both do when the peer's block reaches the node while the
// engine is committing the same one -- succeeds, as it does in the node (Node.commitBlock).
// The mempool is empty throughout. Timers can be scaled down (a report divides the 2 s,
// 2 s, 2 s and 4 s of the defaults by ten) while the costs and the delay stay what they
// are on a running chain, so the races between the messages and the round loop are the
// ones a running pair has.
//
// The report (TestCadenceReport, skipped unless NHB_CADENCE_REPORT is set) prints the
// numbers for whatever engine this file is compiled with; min_block_interval_test.go
// asserts what the engine must satisfy.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/p2p"
)

// cadenceCosts is what one node spends on the steps of a block.
type cadenceCosts struct {
	build    time.Duration // CreateBlock
	validate time.Duration // ValidateBlock
	commit   time.Duration // CommitBlock, and committing a block that arrived from the peer
}

// cadenceNoise is what a busy machine adds to what it does: now and then a step takes
// longer than it should (a garbage collection, another process, a CPU that is short).
// Each step of a node with noise pauses for a random time
// between min and max with probability prob. The generator is seeded, so a run's
// hiccups are the same however the goroutines happen to be scheduled.
type cadenceNoise struct {
	prob     float64
	min, max time.Duration
	mu       sync.Mutex
	rnd      *rand.Rand
}

func newCadenceNoise(prob float64, min, max time.Duration, seed int64) *cadenceNoise {
	if prob <= 0 {
		return nil
	}
	return &cadenceNoise{prob: prob, min: min, max: max, rnd: rand.New(rand.NewSource(seed))}
}

// pause sleeps for a hiccup, or not at all.
func (z *cadenceNoise) pause() {
	if z == nil {
		return
	}
	z.mu.Lock()
	hit := z.rnd.Float64() < z.prob
	span := z.max - z.min
	extra := z.min
	if span > 0 {
		extra += time.Duration(z.rnd.Int63n(int64(span)))
	}
	z.mu.Unlock()
	if hit {
		time.Sleep(extra)
	}
}

// cadenceTrace records what the two nodes do, for looking at a stalled height.
type cadenceTrace struct {
	origin time.Time
	mu     sync.Mutex
	events []cadenceEvent
}

type cadenceEvent struct {
	at   time.Duration
	node int
	what string
}

func (z *cadenceTrace) add(node int, format string, args ...any) {
	if z == nil {
		return
	}
	z.mu.Lock()
	z.events = append(z.events, cadenceEvent{at: time.Since(z.origin), node: node, what: fmt.Sprintf(format, args...)})
	z.mu.Unlock()
}

// cadenceCommit is one block a node committed.
type cadenceCommit struct {
	height  uint64
	at      time.Time
	round   int // round of the quorum certificate the block carries
	viaSync bool
}

// cadenceChain is one validator's node: a chain of empty blocks that takes real time
// to build, validate and commit them.
type cadenceChain struct {
	tv    *testValidators
	self  []byte
	costs cadenceCosts
	noise *cadenceNoise
	index int
	trace *cadenceTrace
	// clock stamps the blocks it builds: the wall clock, sped up by the scale of the run,
	// so that blocks built one round apart differ as they do on a running chain (a header
	// carries the second it was built in).
	clock func() int64
	// duplicateFails makes a second commit of a block the chain holds fail, which the
	// node's does not (see commit).
	duplicateFails bool

	syncMu   sync.Mutex // one block from the peer at a time, as blockSyncMu does
	commitMu sync.Mutex // one commit at a time, as the chain's own lock does

	mu     sync.Mutex
	height uint64
	tip    []byte
	blocks []*types.Block
	log    []cadenceCommit
	engine *Engine
}

func (n *cadenceChain) GetMempool() []*types.Transaction         { return nil }
func (n *cadenceChain) RequeueTransactions([]*types.Transaction) {}

func (n *cadenceChain) CreateBlock([]*types.Transaction) (*types.Block, error) {
	n.trace.add(n.index, "build")
	time.Sleep(n.costs.build)
	n.noise.pause()
	n.mu.Lock()
	defer n.mu.Unlock()
	header := &types.BlockHeader{
		Height:    n.height + 1,
		Timestamp: n.clock(),
		PrevHash:  append([]byte(nil), n.tip...),
		Validator: n.self,
		TxRoot:    testTxRoot(),
	}
	return types.NewBlock(header, nil), nil
}

func (n *cadenceChain) ValidateBlock(b *types.Block) error {
	if b != nil && b.Header != nil {
		n.trace.add(n.index, "validate h=%d", b.Header.Height)
	}
	time.Sleep(n.costs.validate)
	n.noise.pause()
	if b == nil || b.Header == nil {
		return errors.New("nil block")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if b.Header.Height != n.height+1 {
		return fmt.Errorf("block %d does not follow height %d", b.Header.Height, n.height)
	}
	if !bytes.Equal(b.Header.PrevHash, n.tip) {
		return errors.New("block does not follow the tip")
	}
	return nil
}

func (n *cadenceChain) CommitBlock(b *types.Block) error { return n.commit(b, false) }

// commit is the chain's own commit, whichever way the block came. The second of two
// commits of the same block (the engine's and the sync path's) does nothing and succeeds,
// as the node's does: it answers a block it already holds with no error, and the sync path
// then tells the engine about a block the engine has committed itself. A different block
// for a height the chain holds fails.
func (n *cadenceChain) commit(b *types.Block, viaSync bool) error {
	n.commitMu.Lock()
	defer n.commitMu.Unlock()
	n.mu.Lock()
	next := n.height + 1
	held := false
	if b != nil && b.Header != nil && b.Header.Height >= 1 && b.Header.Height < next && b.Header.Height <= uint64(len(n.blocks)) {
		have, _ := n.blocks[b.Header.Height-1].Header.Hash()
		got, _ := b.Header.Hash()
		held = bytes.Equal(have, got)
	}
	n.mu.Unlock()
	if held && !n.duplicateFails {
		return nil
	}
	if b == nil || b.Header == nil || b.Header.Height != next {
		return fmt.Errorf("block is not the next height (%d)", next)
	}
	time.Sleep(n.costs.commit)
	n.noise.pause()
	hash, err := b.Header.Hash()
	if err != nil {
		return err
	}
	round := -1
	if b.QuorumCert != nil {
		round = b.QuorumCert.Round
	}
	n.mu.Lock()
	n.height, n.tip = next, hash
	n.trace.add(n.index, "committed h=%d round=%d viaSync=%v", next, round, viaSync)
	n.blocks = append(n.blocks, b)
	n.log = append(n.log, cadenceCommit{height: next, at: time.Now(), round: round, viaSync: viaSync})
	n.mu.Unlock()
	return nil
}

func (n *cadenceChain) GetValidatorSet() map[string]*big.Int { return n.tv.set }
func (n *cadenceChain) GetAccount(addr []byte) (*types.Account, error) {
	weight := n.tv.set[string(addr)]
	if weight == nil {
		weight = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(weight)}, nil
}
func (n *cadenceChain) GetLastCommitHash() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]byte(nil), n.tip...)
}
func (n *cadenceChain) GetHeight() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.height
}

// syncBlock is what a node does with a block its peer committed and sent: commit it
// if it is the next height, and tell the engine it happened outside its own round.
func (n *cadenceChain) syncBlock(b *types.Block) {
	n.syncMu.Lock()
	defer n.syncMu.Unlock()
	if b == nil || b.Header == nil || b.Header.Height != n.GetHeight()+1 {
		return
	}
	if err := n.commit(b, true); err != nil {
		return
	}
	if n.engine != nil {
		n.engine.NotifyExternalCommit()
	}
}

func (n *cadenceChain) commits() []cadenceCommit {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]cadenceCommit(nil), n.log...)
}

// blocksFrom returns the committed blocks from height from on.
func (n *cadenceChain) blocksFrom(from uint64) []*types.Block {
	n.mu.Lock()
	defer n.mu.Unlock()
	if from == 0 {
		from = 1
	}
	if from > uint64(len(n.blocks)) {
		return nil
	}
	return append([]*types.Block(nil), n.blocks[from-1:]...)
}

// cadenceLink carries what one engine broadcasts to its peer: in order, one message
// at a time, each after the one-way delay.
type cadenceLink struct {
	delay   time.Duration
	from    int // index of the sending node
	trace   *cadenceTrace
	to      *Engine
	toChain *cadenceChain
	back    *cadenceLink // what the receiving node answers over
	queue   chan cadenceFrame
	// dropPrecommits loses every precommit the link carries, so that the receiving node
	// never commits a height through its own round and always takes the block from its peer.
	dropPrecommits bool
}

type cadenceFrame struct {
	msg *p2p.Message
	at  time.Time
}

func newCadenceLink(delay time.Duration, from int, trace *cadenceTrace) *cadenceLink {
	return &cadenceLink{delay: delay, from: from, trace: trace, queue: make(chan cadenceFrame, 1<<15)}
}

// Broadcast satisfies p2p.Broadcaster for the sending engine.
func (l *cadenceLink) Broadcast(msg *p2p.Message) error {
	frame := cadenceFrame{msg: &p2p.Message{Type: msg.Type, Payload: append([]byte(nil), msg.Payload...)}, at: time.Now().Add(l.delay)}
	select {
	case l.queue <- frame:
	default:
	}
	return nil
}

func (l *cadenceLink) run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case f := <-l.queue:
			if d := time.Until(f.at); d > 0 {
				time.Sleep(d)
			}
			l.toChain.noise.pause()
			l.dispatch(f.msg)
		}
	}
}

// dispatch is what a node does with a message its peer sent (core.Node.ProcessNetworkMessage),
// on the goroutine of the connection.
func (l *cadenceLink) dispatch(msg *p2p.Message) {
	to := 1 - l.from
	switch msg.Type {
	case p2p.MsgTypeProposal:
		var sp SignedProposal
		if json.Unmarshal(msg.Payload, &sp) == nil {
			l.trace.add(to, "recv proposal h=%d r=%d", sp.Proposal.Block.Header.Height, sp.Proposal.Round)
			_ = l.to.HandleProposal(&sp)
		}
	case p2p.MsgTypeVote:
		var sv SignedVote
		if json.Unmarshal(msg.Payload, &sv) == nil {
			if l.dropPrecommits && sv.Vote != nil && sv.Vote.Type == Precommit {
				return
			}
			l.trace.add(to, "recv %s h=%d r=%d nil=%v", sv.Vote.Type, sv.Vote.Height, sv.Vote.Round, len(sv.Vote.BlockHash) == 0)
			_ = l.to.HandleVote(&sv)
		}
	case p2p.MsgTypeBlock:
		var b types.Block
		if json.Unmarshal(msg.Payload, &b) == nil {
			l.trace.add(to, "recv block h=%d", b.Header.Height)
			l.toChain.syncBlock(&b)
		}
	case p2p.MsgTypeStatus:
		var st p2p.StatusPayload
		if json.Unmarshal(msg.Payload, &st) == nil && st.Height > l.toChain.GetHeight() {
			if req, err := p2p.NewGetBlocksMessage(l.toChain.GetHeight() + 1); err == nil {
				_ = l.back.Broadcast(req)
			}
		}
	case p2p.MsgTypeGetBlocks:
		var req p2p.GetBlocksPayload
		if json.Unmarshal(msg.Payload, &req) == nil {
			for _, b := range l.toChain.blocksFrom(req.From) {
				if reply, err := p2p.NewBlockMessage(b); err == nil {
					_ = l.back.Broadcast(reply)
				}
			}
		}
	}
}

// cadenceModel is what a run is made of. The costs and the delay are those of a running
// pair (see the file comment); scale divides the round timers.
type cadenceModel struct {
	scale       float64
	delay       time.Duration
	costs       [2]cadenceCosts
	minInterval time.Duration
	// noise[i] is the hiccup probability of node i, and hiccup the range of one.
	noise  [2]float64
	hiccup [2]time.Duration
	seed   int64
	// commitTimeout is the commit timeout of a chain with the default timers, before the
	// scale divides it; zero is the default of 4 s.
	commitTimeout time.Duration
	// oneBehind starts validator 1 one round behind validator 0, which is where a pair
	// is when one of them has been restarted and its peer has gone on.
	oneBehind bool
	// syncOnly[i] loses every precommit sent to node i, so that it takes each block from
	// its peer and never commits a height through its own round.
	syncOnly [2]bool
	// duplicateFails makes a second commit of a block a chain holds fail on both nodes.
	duplicateFails bool
	// sampleStarts watches the round each engine is in, to find out which round each of
	// them started every height in (cadenceRun.starts).
	sampleStarts bool
	// options are applied to both engines, after the timers and the interval.
	options []Option
}

// liveCadenceModel is a pair as a running chain has it: a block is built and validated
// in about 10 ms, committed in about 5, a message crosses in about a millisecond, and
// the second validator is the slower machine.
func liveCadenceModel(scale float64) cadenceModel {
	return cadenceModel{
		scale: scale,
		delay: time.Millisecond,
		costs: [2]cadenceCosts{
			{build: 5 * time.Millisecond, validate: 5 * time.Millisecond, commit: 5 * time.Millisecond},
			{build: 8 * time.Millisecond, validate: 8 * time.Millisecond, commit: 8 * time.Millisecond},
		},
	}
}

func (m cadenceModel) timers() TimeoutConfig {
	s := m.scale
	if s <= 0 {
		s = 1
	}
	div := func(d time.Duration) time.Duration { return time.Duration(float64(d) / s) }
	commit := m.commitTimeout
	if commit <= 0 {
		commit = 4 * time.Second
	}
	return TimeoutConfig{Proposal: div(2 * time.Second), Prevote: div(2 * time.Second), Precommit: div(2 * time.Second), Commit: div(commit)}
}

// cadenceRun is what one run measured, at validator 0.
type cadenceRun struct {
	model     cadenceModel
	elapsed   time.Duration
	heights   int
	intervals []time.Duration // between one commit and the next, warm-up left out
	rounds    []int           // round of the certificate of each height after the warm-up
	viaSync   [2]int          // commits of each node that came from the peer's block
	final     [2]uint64
	trace     *cadenceTrace
	commitsAt []time.Duration // node 0's commit times, on the trace's clock
	heightsOf []uint64        // the height each interval ends at
	synced    map[uint64]bool // the heights that either node committed through the sync path
	// starts[i] is the round node i's engine started each height in, when the run watched
	// for it (cadenceModel.sampleStarts).
	starts [2]map[uint64]int
}

const cadenceWarmup = 3 // heights left out of the numbers

// runCadence runs two engines until node 0 has committed target heights or limit passes.
func runCadence(t *testing.T, m cadenceModel, target int, limit time.Duration) cadenceRun {
	t.Helper()
	tv := newTestValidators(t, 2)
	origin := time.Now()
	scale := m.scale
	if scale <= 0 {
		scale = 1
	}
	clock := func() int64 { return 1_700_000_000 + int64(time.Since(origin).Seconds()*scale) }
	chains := [2]*cadenceChain{
		{tv: tv, self: tv.addrs[0], costs: m.costs[0], clock: clock, duplicateFails: m.duplicateFails, noise: newCadenceNoise(m.noise[0], m.hiccup[0]/4, m.hiccup[0], m.seed+1)},
		{tv: tv, self: tv.addrs[1], costs: m.costs[1], clock: clock, duplicateFails: m.duplicateFails, noise: newCadenceNoise(m.noise[1], m.hiccup[1]/4, m.hiccup[1], m.seed+2)},
	}
	var trace *cadenceTrace
	if os.Getenv("NHB_CADENCE_TRACE") != "" {
		trace = &cadenceTrace{origin: origin}
	}
	chains[0].index, chains[1].index = 0, 1
	chains[0].trace, chains[1].trace = trace, trace
	links := [2]*cadenceLink{newCadenceLink(m.delay, 0, trace), newCadenceLink(m.delay, 1, trace)} // links[i] carries what engine i sends
	options := func() []Option {
		opts := []Option{WithTimeouts(m.timers())}
		if m.minInterval > 0 {
			opts = append(opts, WithMinBlockInterval(m.minInterval))
		}
		return append(opts, m.options...)
	}
	engines := [2]*Engine{
		NewEngine(chains[0], tv.keys[0], links[0], options()...),
		NewEngine(chains[1], tv.keys[1], links[1], options()...),
	}
	chains[0].engine, chains[1].engine = engines[0], engines[1]
	links[0].to, links[0].toChain, links[0].back = engines[1], chains[1], links[1]
	links[1].to, links[1].toChain, links[1].back = engines[0], chains[0], links[0]
	links[1].dropPrecommits, links[0].dropPrecommits = m.syncOnly[0], m.syncOnly[1] // links[1-j] carries what node j receives
	if m.oneBehind {
		engines[0].mu.Lock()
		engines[0].currentState.Round++
		engines[0].mu.Unlock()
	}

	stop := make(chan struct{})
	var stopped atomic.Bool
	var wg sync.WaitGroup
	starts := [2]map[uint64]int{{}, {}}
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(2)
		go func() { defer wg.Done(); links[i].run(stop) }()
		go func() {
			defer wg.Done()
			for !stopped.Load() {
				engines[i].runRound()
			}
		}()
		if m.sampleStarts {
			wg.Add(1)
			go func() { defer wg.Done(); sampleStartRounds(engines[i], &stopped, starts[i]) }()
		}
	}

	started := time.Now()
	deadline := time.After(limit)
	tick := time.NewTicker(2 * time.Millisecond)
wait:
	for {
		select {
		case <-tick.C:
			if chains[0].GetHeight() >= uint64(target) {
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	tick.Stop()
	elapsed := time.Since(started)
	stopped.Store(true)
	close(stop)
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	for done := false; !done; {
		for _, e := range engines {
			endRound(e)
		}
		select {
		case <-finished:
			done = true
		case <-time.After(5 * time.Millisecond):
		}
	}

	run := cadenceRun{model: m, elapsed: elapsed, trace: trace, starts: starts}
	log := chains[0].commits()
	run.heights = len(log)
	for i, c := range log {
		if trace != nil {
			run.commitsAt = append(run.commitsAt, c.at.Sub(origin))
		}
		if i < cadenceWarmup {
			continue
		}
		run.intervals = append(run.intervals, c.at.Sub(log[i-1].at))
		run.rounds = append(run.rounds, c.round)
		run.heightsOf = append(run.heightsOf, c.height)
	}
	run.synced = map[uint64]bool{}
	for _, chain := range chains {
		for _, c := range chain.commits() {
			if c.viaSync {
				run.synced[c.height] = true
			}
		}
	}
	for i, chain := range chains {
		for _, c := range chain.commits() {
			if c.viaSync {
				run.viaSync[i]++
			}
		}
		run.final[i] = chain.GetHeight()
	}
	return run
}

// sampleStartRounds watches the round an engine is in and records, for each height, the
// first round it stayed in for a couple of milliseconds: the round it started the height in
// (the state commit leaves for a moment, round 0 of the height it has just moved to, is
// gone before that).
func sampleStartRounds(e *Engine, stopped *atomic.Bool, into map[uint64]int) {
	var height uint64
	round := -1
	var since time.Time
	for !stopped.Load() {
		h, r, _ := e.Status()
		now := time.Now()
		if h != height || r != round {
			height, round, since = h, r, now
		} else if now.Sub(since) >= 2*time.Millisecond {
			if _, seen := into[h]; !seen {
				into[h] = r
			}
		}
		time.Sleep(200 * time.Microsecond)
	}
}

// span is the time the measured heights took.
func (r cadenceRun) span() time.Duration {
	var total time.Duration
	for _, d := range r.intervals {
		total += d
	}
	return total
}

// perSecond is the pace of the measured heights, in blocks a second of the run's own
// (scaled) clock.
func (r cadenceRun) perSecond() float64 {
	span := r.span()
	if span <= 0 || len(r.intervals) == 0 {
		return 0
	}
	return float64(len(r.intervals)) / span.Seconds()
}

func (r cadenceRun) quantile(q float64) time.Duration {
	if len(r.intervals) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), r.intervals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(q * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// stallLimit is how long a height may take before it counts as stalled: its minimum
// interval and half a commit timer, which is more than the messages of a round need
// and less than a round that failed takes.
func (r cadenceRun) stallLimit() time.Duration {
	return r.model.minInterval + r.model.timers().Commit/2
}

// stalled counts the heights that took longer than stallLimit: one or more rounds that
// failed and were waited out.
func (r cadenceRun) stalled() int {
	n := 0
	for _, d := range r.intervals {
		if d > r.stallLimit() {
			n++
		}
	}
	return n
}

// afterSync splits the heights by whether the height before was committed through the
// sync path by either node, and counts how many of each stalled: before the two started a
// height in the same round whichever way it ended (see startNewRound), the stalls began
// there.
func (r cadenceRun) afterSync() (stalledAfter, after, stalledOther, other int) {
	for i, d := range r.intervals {
		stalled := d > r.stallLimit()
		if r.synced[r.heightsOf[i]-1] {
			after++
			if stalled {
				stalledAfter++
			}
			continue
		}
		other++
		if stalled {
			stalledOther++
		}
	}
	return
}

// roundBuckets counts the heights by the round they were decided in.
func (r cadenceRun) roundBuckets() map[string]int {
	out := map[string]int{}
	for _, round := range r.rounds {
		switch {
		case round <= 1:
			out["1"]++
		case round == 2:
			out["2"]++
		case round <= 4:
			out["3-4"]++
		case round <= 9:
			out["5-9"]++
		default:
			out["10+"]++
		}
	}
	return out
}

// liveEquivalentDurations is the time each measured height would have taken on a chain
// with the default timers. A run scales the round timers and the minimum interval down by
// the scale of the run, so what they cost is multiplied back; the time a block takes when
// nothing fails is not scaled (it is what it is), and is kept as it is: the time of a
// height beyond its minimum interval, without a failed round, is the median of those, and
// a height that stalled costs that plus its failed rounds, scaled.
func (r cadenceRun) liveEquivalentDurations() []time.Duration {
	if len(r.intervals) == 0 {
		return nil
	}
	scale := r.model.scale
	if scale <= 0 {
		scale = 1
	}
	interval := r.model.minInterval
	beyond := func(d time.Duration) time.Duration { return max(d-interval, 0) }
	var fast []time.Duration
	for _, d := range r.intervals {
		if d <= r.stallLimit() {
			fast = append(fast, beyond(d))
		}
	}
	sort.Slice(fast, func(i, j int) bool { return fast[i] < fast[j] })
	var typical time.Duration
	if len(fast) > 0 {
		typical = fast[len(fast)/2]
	}
	live := make([]time.Duration, 0, len(r.intervals))
	for _, d := range r.intervals {
		switch {
		case d <= r.stallLimit():
			live = append(live, beyond(d)+time.Duration(float64(interval)*scale))
		default:
			live = append(live, typical+time.Duration(float64(beyond(d)-typical)*scale)+time.Duration(float64(interval)*scale))
		}
	}
	return live
}

// liveEquivalentPerSecond is the pace converted to a chain with the default timers.
func (r cadenceRun) liveEquivalentPerSecond() float64 {
	total := 0.0
	for _, d := range r.liveEquivalentDurations() {
		total += d.Seconds()
	}
	if total <= 0 {
		return 0
	}
	return float64(len(r.intervals)) / total
}

// liveEquivalentMeanMax is the mean and the longest time of a height on a chain with the
// default timers.
func (r cadenceRun) liveEquivalentMeanMax() (mean, longest time.Duration) {
	live := r.liveEquivalentDurations()
	var total time.Duration
	for _, d := range live {
		total += d
		longest = max(longest, d)
	}
	if len(live) > 0 {
		mean = total / time.Duration(len(live))
	}
	return
}

func (r cadenceRun) String() string {
	b := r.roundBuckets()
	keys := []string{"1", "2", "3-4", "5-9", "10+"}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, b[k]))
	}
	stalledAfter, after, stalledOther, other := r.afterSync()
	differ, both := r.startMismatch()
	liveMean, liveMax := r.liveEquivalentMeanMax()
	return fmt.Sprintf("heights=%d in %v  %.2f blocks/s (run clock) %.2f blocks/s (live-equivalent, mean %v max %v a height)  interval mean=%v p50=%v p90=%v p99=%v max=%v  stalled=%d (%.1f%%)  rounds{%s}  later-round=%d (%.1f%%)  start-rounds-differ=%d/%d  via-sync=%v  stalled-after-a-synced-height=%d/%d stalled-otherwise=%d/%d",
		len(r.intervals), r.span().Round(time.Millisecond), r.perSecond(), r.liveEquivalentPerSecond(), liveMean.Round(time.Millisecond), liveMax.Round(time.Millisecond),
		(r.span() / time.Duration(max(1, len(r.intervals)))).Round(100*time.Microsecond),
		r.quantile(.5).Round(100*time.Microsecond), r.quantile(.9).Round(100*time.Microsecond), r.quantile(.99).Round(100*time.Microsecond), r.quantile(1).Round(time.Millisecond),
		r.stalled(), 100*float64(r.stalled())/float64(max(1, len(r.intervals))), strings.Join(parts, " "),
		r.laterRound(), 100*float64(r.laterRound())/float64(max(1, len(r.intervals))), differ, both, r.viaSync,
		stalledAfter, after, stalledOther, other)
}

// laterRound counts the heights decided in a round after the first one a height starts in
// (round 1: see startNewRound).
func (r cadenceRun) laterRound() int {
	n := 0
	for _, round := range r.rounds {
		if round > 1 {
			n++
		}
	}
	return n
}

// startMismatch counts the measured heights that the two validators started in different
// rounds, out of the ones both were seen to start (cadenceModel.sampleStarts).
func (r cadenceRun) startMismatch() (differ, both int) {
	for _, h := range r.heightsOf {
		a, seenA := r.starts[0][h]
		b, seenB := r.starts[1][h]
		if !seenA || !seenB {
			continue
		}
		both++
		if a != b {
			differ++
		}
	}
	return
}

// dumpStalls prints what the two nodes did during the first few heights that stalled.
func (r cadenceRun) dumpStalls(limitHeights int) {
	if r.trace == nil {
		return
	}
	threshold := r.model.timers().Commit/2 + r.model.minInterval
	shown := 0
	for i := cadenceWarmup; i < len(r.commitsAt) && shown < limitHeights; i++ {
		if r.commitsAt[i]-r.commitsAt[i-1] <= threshold {
			continue
		}
		fmt.Fprintf(os.Stderr, "\nTRACE|height %d took %v\n", i+1, (r.commitsAt[i] - r.commitsAt[i-1]).Round(time.Millisecond))
		r.trace.mu.Lock()
		for _, ev := range r.trace.events {
			if ev.at >= r.commitsAt[i-1]-2*time.Millisecond && ev.at <= r.commitsAt[i]+time.Millisecond {
				fmt.Fprintf(os.Stderr, "TRACE|%9.1fms node%d %s\n", float64(ev.at-r.commitsAt[i-1])/float64(time.Millisecond), ev.node, ev.what)
			}
		}
		r.trace.mu.Unlock()
		shown++
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envDurations(name string, def []time.Duration) []time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	var out []time.Duration
	for _, part := range strings.Split(raw, ",") {
		if d, err := time.ParseDuration(strings.TrimSpace(part)); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// TestCadenceReport prints the pace of an idle pair for the engine this file is built
// with. It measures and asserts nothing. NHB_CADENCE_REPORT enables it;
// NHB_CADENCE_SCALE (default 10) divides the round timers, NHB_CADENCE_HEIGHTS
// (default 400) is how many heights a trial runs, NHB_CADENCE_TRIALS (default 3) how
// many trials per setting, NHB_CADENCE_MIN the minimum intervals to try (default 0),
// NHB_CADENCE_NOISE_PERMILLE and NHB_CADENCE_HICCUP_MS the hiccups of the slower
// validator (the other has a quarter of the chance and half the length), and
// NHB_CADENCE_TRACE the events of the first stalled heights. Intervals are the ones a
// chain with the default timers would use: the run divides them by the scale.
// NHB_CADENCE_FAST_MS and NHB_CADENCE_SLOW_MS are what a build, a validation and a commit
// cost at the first and at the second validator (default 5 and 8).
// NHB_CADENCE_COMMIT is the commit timeout of that chain (default 4s; an interval of more
// than half of it is lowered to half), NHB_CADENCE_BEHIND starts the second validator one
// round behind the first, and NHB_CADENCE_DUPLICATE_FAILS makes a second commit of a block
// a node holds fail, which the node's does not.
func TestCadenceReport(t *testing.T) {
	if os.Getenv("NHB_CADENCE_REPORT") == "" {
		t.Skip("set NHB_CADENCE_REPORT to print the cadence of an idle pair")
	}
	scale := float64(envInt("NHB_CADENCE_SCALE", 10))
	heights := envInt("NHB_CADENCE_HEIGHTS", 400)
	trials := envInt("NHB_CADENCE_TRIALS", 3)
	noise := float64(envInt("NHB_CADENCE_NOISE_PERMILLE", 0)) / 1000
	hiccup := time.Duration(envInt("NHB_CADENCE_HICCUP_MS", 40)) * time.Millisecond
	for _, live := range envDurations("NHB_CADENCE_MIN", []time.Duration{0}) {
		for trial := 1; trial <= trials; trial++ {
			m := liveCadenceModel(scale)
			for i, name := range []string{"NHB_CADENCE_FAST_MS", "NHB_CADENCE_SLOW_MS"} {
				if ms := envInt(name, 0); ms > 0 {
					cost := time.Duration(ms) * time.Millisecond
					m.costs[i] = cadenceCosts{build: cost, validate: cost, commit: cost}
				}
			}
			m.minInterval = time.Duration(float64(live) / scale)
			m.noise, m.hiccup, m.seed = [2]float64{noise / 4, noise}, [2]time.Duration{hiccup / 2, hiccup}, int64(trial)
			if d := envDurations("NHB_CADENCE_COMMIT", nil); len(d) > 0 {
				m.commitTimeout = d[0]
			}
			m.oneBehind = os.Getenv("NHB_CADENCE_BEHIND") != ""
			m.duplicateFails = os.Getenv("NHB_CADENCE_DUPLICATE_FAILS") != ""
			m.sampleStarts = true
			run := runCadence(t, m, heights+cadenceWarmup, 6*time.Hour)
			fmt.Fprintf(os.Stderr, "\nCADENCE|scale=%g|live-min-interval=%v|trial=%d|%s\n", scale, live, trial, run)
			run.dumpStalls(envInt("NHB_CADENCE_TRACE_STALLS", 2))
		}
	}
}
