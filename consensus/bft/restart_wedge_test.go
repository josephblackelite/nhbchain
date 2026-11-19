package bft

// Restarts of a validator set in which every running validator is needed, on real engines.
//
// Three validators of four run (the fourth is down), or five of seven: every quorum needs all of
// the running ones, so a block commits only in a round that all of them are in. Restarted at
// different moments they used to end up in different rounds -- typically two level with each other
// and the third some rounds ahead of them -- and stay there for good: the height stopped and the
// round numbers went on up. A round lasts the commit timeout at every validator, so validators
// that are k rounds apart stay k rounds apart; a validator sends nothing in a round in which it
// has no proposal to vote on, so the ones behind hear little of where the one ahead is; and a
// validator moved to a later round only when the validators seen there held more than a third of
// the power and, with it, a quorum (the first rule of supportedRoundLocked before a third was enough),
// which with every other validator needed
// means all of them: one validator alone ahead was never followed. Two validators never showed it,
// because for two one peer ahead is enough, and neither did a set with a quorum to spare. Locks are
// not what keeps the validators apart: the ones in a wedge often hold one, and it also happens
// with none.
//
// The rig is n engines -- the real ones, with their real timers, signatures, vote records, lock
// snapshots and weak-jump budgets -- on the chain doubles of the cadence rig, joined by links that
// deliver in order after a one-way delay, the last few of the set never started (they stay in the
// set, are the proposers of some rounds, and those rounds fail). A schedule drawn from a seed
// restarts one, two or all of the running validators at random moments: each is killed (nothing it
// sends afterwards is delivered), left down for a while, and started again on the same chain, vote
// record, lock snapshot and weak-jump budget, and it listens for bootRounds rounds before it runs
// its own (Engine.Start waits 30 seconds for its peers, 7.5 rounds of 4 s). A run is wedged when
// every running validator runs and no block comes for stallAfter rounds. Every run also checks that
// no two chains hold different blocks and that no validator signed two conflicting votes.
//
// The timers are a twenty-fifth of the defaults (a round that does not commit lasts 160 ms), the
// costs and the delay a millisecond; TestWedgeReport prints the wedge rate for any size of set.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
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

// wedgeModel is what a run of the mesh is made of.
type wedgeModel struct {
	validators int // size of the validator set
	silent     int // how many of them never run (the last ones of the set): they stay in the set
	scale      float64
	delay      time.Duration
	costs      cadenceCosts
	bootRounds int // rounds a restarted validator listens before it starts its own
	restarts   int
	minGap     float64 // rounds between two restarts, at least
	maxGap     float64 // and at most
	maxDown    float64 // rounds a validator stays down before it is started again, at most
	stagger    float64 // rounds between the restarts of the validators that restart together, at most
	stallAfter float64 // rounds without a block, with every running validator running, that count as a wedge
	// minInterval is the minimum block interval of the engines, scaled down like the timers (zero: none).
	minInterval time.Duration
	trace       bool
}

func defaultWedgeModel(validators, silent int) wedgeModel {
	return wedgeModel{
		validators: validators,
		silent:     silent,
		scale:      25,
		delay:      time.Millisecond,
		costs:      cadenceCosts{build: time.Millisecond, validate: time.Millisecond, commit: time.Millisecond},
		bootRounds: 8,
		restarts:   12,
		minGap:     1,
		maxGap:     8,
		maxDown:    3,
		stagger:    1,
		stallAfter: 80,
	}
}

func (m wedgeModel) timers() TimeoutConfig {
	div := func(d time.Duration) time.Duration { return time.Duration(float64(d) / m.scale) }
	return TimeoutConfig{Proposal: div(2 * time.Second), Prevote: div(2 * time.Second), Precommit: div(2 * time.Second), Commit: div(4 * time.Second)}
}

// round is how long a round that does not commit lasts.
func (m wedgeModel) round() time.Duration { return m.timers().Commit }

func (m wedgeModel) rounds(n float64) time.Duration { return time.Duration(n * float64(m.round())) }

// wedgeFrame is a message on a link.
type wedgeFrame struct {
	msg *p2p.Message
	at  time.Time
}

// wedgeNode is one incarnation of a validator's process: an engine, and the loop that runs it.
type wedgeNode struct {
	idx     int
	engine  *Engine
	port    *wedgePort
	dead    atomic.Bool
	quit    chan struct{}
	done    chan struct{}
	started atomic.Bool // true once its own rounds run (the boot wait is over)
}

// wedgePort is the broadcaster of one incarnation: it delivers nothing once the process is dead.
type wedgePort struct {
	rig  *wedgeRig
	idx  int
	dead atomic.Bool
	mu   sync.Mutex
	msgs []*p2p.Message
}

func (p *wedgePort) Broadcast(msg *p2p.Message) error {
	if p.dead.Load() {
		return nil
	}
	p.mu.Lock()
	p.msgs = append(p.msgs, msg)
	p.mu.Unlock()
	p.rig.send(p.idx, -1, msg)
	return nil
}

// wedgeRig is n validators joined by per-pair links that deliver in order, one message at a
// time, after a one-way delay; the last silent of them never run.
type wedgeRig struct {
	m      wedgeModel
	tv     *testValidators
	origin time.Time
	chains []*cadenceChain
	dirs   []string
	links  [][]chan wedgeFrame // links[from][to]

	lockedKills atomic.Int64 // restarts of a validator that held a lock

	mu     sync.Mutex
	nodes  []*wedgeNode
	sent   [][]*p2p.Message // what every incarnation of a validator broadcast
	events []string
	stop   chan struct{}
	wg     sync.WaitGroup
}

func newWedgeRig(t *testing.T, m wedgeModel) *wedgeRig {
	t.Helper()
	tv := newTestValidators(t, m.validators)
	r := &wedgeRig{m: m, tv: tv, origin: time.Now(), stop: make(chan struct{})}
	clock := func() int64 { return 1_700_000_000 + int64(time.Since(r.origin).Seconds()*m.scale) }
	for i := 0; i < m.validators; i++ {
		r.chains = append(r.chains, &cadenceChain{tv: tv, self: tv.addrs[i], costs: m.costs, clock: clock, index: i})
		r.dirs = append(r.dirs, t.TempDir())
	}
	r.nodes = make([]*wedgeNode, m.validators)
	r.sent = make([][]*p2p.Message, m.validators)
	r.links = make([][]chan wedgeFrame, m.validators)
	for from := range r.links {
		r.links[from] = make([]chan wedgeFrame, m.validators)
		for to := range r.links[from] {
			if from == to {
				continue
			}
			r.links[from][to] = make(chan wedgeFrame, 1<<14)
			r.wg.Add(1)
			go r.runLink(from, to)
		}
	}
	return r
}

func (r *wedgeRig) running() int { return r.m.validators - r.m.silent }

func (r *wedgeRig) logf(format string, args ...any) {
	line := fmt.Sprintf("%8.3fs ", time.Since(r.origin).Seconds()) + fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.events = append(r.events, line)
	r.mu.Unlock()
}

// send queues msg from validator from to validator to, or to every other one when to is negative.
func (r *wedgeRig) send(from, to int, msg *p2p.Message) {
	frame := wedgeFrame{msg: &p2p.Message{Type: msg.Type, Payload: append([]byte(nil), msg.Payload...)}, at: time.Now().Add(r.m.delay)}
	for j := range r.links[from] {
		if j == from || (to >= 0 && j != to) {
			continue
		}
		select {
		case r.links[from][j] <- frame:
		default:
		}
	}
}

func (r *wedgeRig) runLink(from, to int) {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			return
		case f := <-r.links[from][to]:
			if d := time.Until(f.at); d > 0 {
				time.Sleep(d)
			}
			r.deliver(from, to, f.msg)
		}
	}
}

func (r *wedgeRig) node(idx int) *wedgeNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := r.nodes[idx]; n != nil && !n.dead.Load() {
		return n
	}
	return nil
}

// deliver is what a node does with a message a peer sent (core.Node.ProcessNetworkMessage).
func (r *wedgeRig) deliver(from, to int, msg *p2p.Message) {
	node := r.node(to)
	if node == nil {
		return
	}
	chain := r.chains[to]
	switch msg.Type {
	case p2p.MsgTypeProposal:
		var sp SignedProposal
		if json.Unmarshal(msg.Payload, &sp) == nil {
			_ = node.engine.HandleProposal(&sp)
		}
	case p2p.MsgTypeVote:
		var sv SignedVote
		if json.Unmarshal(msg.Payload, &sv) == nil {
			_ = node.engine.HandleVote(&sv)
		}
	case p2p.MsgTypeBlock:
		var b types.Block
		if json.Unmarshal(msg.Payload, &b) == nil {
			chain.syncMu.Lock()
			if b.Header != nil && b.Header.Height == chain.GetHeight()+1 {
				if chain.commit(&b, true) == nil {
					node.engine.NotifyExternalCommit()
				}
			}
			chain.syncMu.Unlock()
		}
	case p2p.MsgTypeGetStatus:
		if reply, err := p2p.NewStatusMessage(chain.GetHeight()); err == nil {
			r.send(to, from, reply)
		}
	case p2p.MsgTypeStatus:
		var st p2p.StatusPayload
		if json.Unmarshal(msg.Payload, &st) == nil && st.Height > chain.GetHeight() {
			if req, err := p2p.NewGetBlocksMessage(chain.GetHeight() + 1); err == nil {
				r.send(to, from, req)
			}
		}
	case p2p.MsgTypeGetBlocks:
		var req p2p.GetBlocksPayload
		if json.Unmarshal(msg.Payload, &req) == nil {
			for _, b := range chain.blocksFrom(req.From) {
				if reply, err := p2p.NewBlockMessage(b); err == nil {
					r.send(to, from, reply)
				}
			}
		}
	}
}

func (r *wedgeRig) signPath(idx int) string { return filepath.Join(r.dirs[idx], "bft_sign_state.json") }
func (r *wedgeRig) lockPath(idx int) string { return filepath.Join(r.dirs[idx], "bft_lock.json") }
func (r *wedgeRig) weakJumpPath(idx int) string {
	return filepath.Join(r.dirs[idx], "bft_weak_jump.json")
}

// start makes a new process of validator idx: an engine built the way a restarted one is (on
// the chain the node already has, with the vote record, the lock snapshot and the weak-round-jump
// budget the last one left), which listens for boot and then runs its rounds (Engine.Start).
// first, when it is not zero, is the round its first round is (a fresh engine starts in round 1).
func (r *wedgeRig) start(idx int, boot time.Duration, first int) {
	port := &wedgePort{rig: r, idx: idx}
	opts := []Option{
		WithTimeouts(r.m.timers()),
		WithSignStatePath(r.signPath(idx)),
		WithLockSnapshotPath(r.lockPath(idx)),
		WithWeakJumpSnapshotPath(r.weakJumpPath(idx)),
	}
	if r.m.minInterval > 0 {
		opts = append(opts, WithMinBlockInterval(r.m.minInterval))
	}
	engine := NewEngine(r.chains[idx], r.tv.keys[idx], port, opts...)
	if first > 0 {
		engine.mu.Lock()
		engine.currentState.Round = first - 1
		engine.mu.Unlock()
	}
	n := &wedgeNode{idx: idx, engine: engine, port: port, quit: make(chan struct{}), done: make(chan struct{})}
	r.mu.Lock()
	r.nodes[idx] = n
	r.mu.Unlock()
	h, _, locked := engine.Status()
	floor, signed := engine.signedRoundFloor(h)
	r.logf("v%d starts at height %d (chain %d), booting %v; vote record floor=%d(%v) lock=%d", idx, h, r.chains[idx].GetHeight(), boot.Round(time.Millisecond), floor, signed, locked)
	go func() {
		defer close(n.done)
		select {
		case <-time.After(boot):
		case <-n.quit:
			return
		}
		n.started.Store(true)
		engine.requestStatus()
		for !n.dead.Load() {
			engine.runRound()
		}
	}()
}

// kill ends the process of validator idx: nothing more leaves it, its rounds stop.
func (r *wedgeRig) kill(idx int) {
	r.mu.Lock()
	n := r.nodes[idx]
	r.mu.Unlock()
	if n == nil {
		return
	}
	h, round, locked := n.engine.Status()
	if locked >= 0 {
		r.lockedKills.Add(1)
	}
	r.logf("v%d killed at height %d round %d lock=%d", idx, h, round, locked)
	n.port.dead.Store(true)
	n.dead.Store(true)
	close(n.quit)
	for done := false; !done; {
		endRound(n.engine)
		select {
		case <-n.done:
			done = true
		case <-time.After(2 * time.Millisecond):
		}
	}
	r.mu.Lock()
	r.sent[idx] = append(r.sent[idx], n.port.msgs...)
	r.nodes[idx] = nil
	r.mu.Unlock()
	time.Sleep(3 * time.Millisecond) // what was still being handled when it died
}

// close ends every process and every link of the rig.
func (r *wedgeRig) close() {
	close(r.stop)
	for i := range r.nodes {
		r.kill(i)
	}
	// Every link goroutine returns once stop is closed.
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (r *wedgeRig) height() uint64 {
	var best uint64
	for _, c := range r.chains {
		if h := c.GetHeight(); h > best {
			best = h
		}
	}
	return best
}

// waitForHeight fails the test unless a chain of the rig reaches height within rounds rounds.
func (r *wedgeRig) waitForHeight(t *testing.T, height uint64, rounds float64, what string) {
	t.Helper()
	deadline := time.After(r.m.rounds(rounds))
	for r.height() < height {
		select {
		case <-deadline:
			t.Fatalf("LIVENESS: %s: no block %d in %.0f rounds (the chains are at height %d):\n%s", what, height, rounds, r.height(), r.diagnose())
		case <-time.After(r.m.round() / 8):
		}
	}
}

// allRunning reports whether every validator that is meant to run is running its rounds.
func (r *wedgeRig) allRunning() bool {
	for i := 0; i < r.running(); i++ {
		n := r.node(i)
		if n == nil || !n.started.Load() {
			return false
		}
	}
	return true
}

// wedgeRestart is one restart in a schedule.
type wedgeRestart struct {
	at   time.Duration
	who  int
	down time.Duration
}

// wedgeSchedule draws the restarts of a run from seed: at random moments, one, two or all of the
// running validators, each of them killed a little after the other and down for a while.
func wedgeSchedule(m wedgeModel, seed int64) []wedgeRestart {
	rnd := rand.New(rand.NewSource(seed))
	running := m.validators - m.silent
	var out []wedgeRestart
	at := m.rounds(2)
	for i := 0; i < m.restarts; i++ {
		at += m.rounds(m.minGap + rnd.Float64()*(m.maxGap-m.minGap))
		count := 1 + rnd.Intn(3)
		if count > running {
			count = running
		}
		if count == 3 && running > 3 {
			count = running // "all of them"
		}
		for _, who := range rnd.Perm(running)[:count] {
			out = append(out, wedgeRestart{
				at:   at + m.rounds(rnd.Float64()*m.stagger),
				who:  who,
				down: m.rounds(rnd.Float64() * m.maxDown),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// wedgeReport is what a run found.
type wedgeReport struct {
	seed           int64
	wedged         bool
	at             time.Duration
	height         uint64
	longest        time.Duration // longest stretch without a block with every running validator running
	forkAt         uint64
	doubles        int // pairs of conflicting votes of one validator that a slashing report could be made of
	lockedRestarts int // restarts of a validator that held a lock
	shape          string
	diagnosis      string
}

// diagnose says where every running validator is: its height, round, lock, the round it last
// signed in, and where it has seen the others.
func (r *wedgeRig) diagnose() string {
	text, _ := r.describe()
	return text
}

// describe is diagnose with the shape of the state as well: "k ahead" when k validators are in a
// later round than all the others, "chains differ" when the validators do not hold the same height
// of chain, and "other" when they are all in one round.
func (r *wedgeRig) describe() (string, string) {
	var b strings.Builder
	var rounds []int
	chainsDiffer := false
	for i := 0; i < r.running(); i++ {
		n := r.node(i)
		if n == nil {
			fmt.Fprintf(&b, "  v%d: down\n", i)
			continue
		}
		e := n.engine
		h, round, locked := e.Status()
		floor, signed := e.signedRoundFloor(h)
		e.mu.RLock()
		var claims []string
		for key, c := range e.roundClaims {
			for j := range r.tv.addrs {
				if string(r.tv.addrs[j]) == key {
					claims = append(claims, fmt.Sprintf("v%d:h%d/r%d", j, c.height, c.round))
				}
			}
		}
		sort.Strings(claims)
		validRound := e.validRound
		e.mu.RUnlock()
		rounds = append(rounds, round)
		if r.chains[i].GetHeight() != r.chains[0].GetHeight() {
			chainsDiffer = true
		}
		fmt.Fprintf(&b, "  v%d: height %d round %d lockedRound %d validRound %d signedFloor %d(%v) chain %d claims %v\n", i, h, round, locked, validRound, floor, signed, r.chains[i].GetHeight(), claims)
	}
	shape := "other"
	sort.Sort(sort.Reverse(sort.IntSlice(rounds)))
	top := 0
	for _, round := range rounds {
		if round == rounds[0] {
			top++
		}
	}
	switch {
	case chainsDiffer:
		shape = "chains differ"
	case top < len(rounds):
		shape = fmt.Sprintf("%d ahead", top)
	}
	return b.String(), shape
}

// conflictingVotes counts the pairs of conflicting votes that any validator of the rig signed,
// in any of its processes: what a slashing report is made of.
func (r *wedgeRig) conflictingVotes() int {
	doubles := 0
	for i := 0; i < r.running(); i++ {
		r.mu.Lock()
		msgs := append([]*p2p.Message(nil), r.sent[i]...)
		if n := r.nodes[i]; n != nil {
			n.port.mu.Lock()
			msgs = append(msgs, n.port.msgs...)
			n.port.mu.Unlock()
		}
		r.mu.Unlock()
		doubles += slashableProofs(votesBy(msgs, r.tv.addrs[i]), r.tv.addrs[i])
	}
	return doubles
}

// forkHeight returns the first height at which two chains hold different blocks, or 0.
func (r *wedgeRig) forkHeight() uint64 {
	for h := uint64(1); ; h++ {
		var first []byte
		seen := false
		for _, c := range r.chains {
			blocks := c.blocksFrom(h)
			if len(blocks) == 0 {
				continue
			}
			hash, _ := blocks[0].Header.Hash()
			if !seen {
				first, seen = hash, true
			} else if !bytes.Equal(first, hash) {
				return h
			}
		}
		if !seen {
			return 0
		}
	}
}

// requireSafe fails the test when two chains of the rig hold different blocks or a validator
// signed conflicting votes.
func (r *wedgeRig) requireSafe(t *testing.T) {
	t.Helper()
	if h := r.forkHeight(); h != 0 {
		t.Fatalf("SAFETY: two chains hold different blocks at height %d", h)
	}
	if n := r.conflictingVotes(); n != 0 {
		t.Fatalf("SAFETY: a validator signed conflicting votes (%d slashing proofs can be made)", n)
	}
}

// runWedge runs the mesh through the schedule of seed and reports whether it wedged.
func runWedge(t *testing.T, m wedgeModel, seed int64) wedgeReport {
	t.Helper()
	r := newWedgeRig(t, m)
	rep := wedgeReport{seed: seed}
	defer r.close()

	for i := 0; i < r.running(); i++ {
		r.start(i, 0, 0)
	}
	schedule := wedgeSchedule(m, seed)
	var end time.Duration
	if len(schedule) > 0 {
		// After the last restart, long enough for a wedge to be seen and for a run that is not
		// wedged to show it.
		end = schedule[len(schedule)-1].at + m.rounds(m.maxDown+float64(m.bootRounds)) + m.rounds(m.stallAfter+20)
	}
	type pending struct {
		at  time.Duration
		who int
		up  bool
	}
	var todo []pending
	for _, ev := range schedule {
		todo = append(todo, pending{at: ev.at, who: ev.who}, pending{at: ev.at + ev.down, who: ev.who, up: true})
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].at < todo[j].at })

	lastHeight := r.height()
	lastAdvance := time.Now()
	var clearSince time.Time // when every running validator was last seen running its rounds
	tick := time.NewTicker(m.round() / 8)
	defer tick.Stop()
	for {
		<-tick.C
		elapsed := time.Since(r.origin)
		for len(todo) > 0 && todo[0].at <= elapsed {
			ev := todo[0]
			todo = todo[1:]
			if ev.up {
				// A validator restarted again before it came back is started once.
				if r.node(ev.who) == nil {
					r.start(ev.who, m.rounds(float64(m.bootRounds)), 0)
				}
			} else {
				r.kill(ev.who)
			}
		}
		if h := r.height(); h > lastHeight {
			lastHeight, lastAdvance = h, time.Now()
		}
		if r.allRunning() {
			if clearSince.IsZero() {
				clearSince = time.Now()
			}
			// A stretch of no block counts from the last block or from the moment every running
			// validator was running, whichever is later.
			from := lastAdvance
			if clearSince.After(from) {
				from = clearSince
			}
			stalled := time.Since(from)
			if stalled > rep.longest {
				rep.longest = stalled
			}
			if stalled > m.rounds(m.stallAfter) {
				rep.wedged, rep.at, rep.height = true, elapsed, lastHeight
				rep.diagnosis, rep.shape = r.describe()
				break
			}
		} else {
			clearSince = time.Time{}
		}
		if len(todo) == 0 && elapsed > end {
			break
		}
	}
	rep.height = r.height()
	rep.forkAt = r.forkHeight()
	rep.lockedRestarts = int(r.lockedKills.Load())
	if m.trace || rep.wedged {
		r.mu.Lock()
		rep.diagnosis += strings.Join(r.events, "\n") + "\n"
		r.mu.Unlock()
	}
	rep.doubles = r.conflictingVotes()
	return rep
}

// runWedgeSeeds runs the schedules of seeds from, from+1, ... from+seeds-1, parallel at a time.
func runWedgeSeeds(t *testing.T, m wedgeModel, from, seeds, parallel int) []wedgeReport {
	t.Helper()
	reports := make([]wedgeReport, seeds)
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := 0; i < seeds; i++ {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			reports[i] = runWedge(t, m, int64(from+i))
		}()
	}
	wg.Wait()
	return reports
}

// Every validator that runs is needed for a quorum -- three of four, five of seven -- and the
// ones that are behind start in a round some rounds before the ones ahead, all rounds of the same
// length: there is never a round that all of them are in, so nothing can commit, unless one that
// is behind moves to the round of one that is ahead. The round timers are the same everywhere and
// nothing is sent in a round in which nothing is proposed, so nothing else brings them together:
// on an engine that follows only a validator that holds a third of the power or more
// (supportedRoundLocked) the height does not commit however long they run. No lock is involved.
func TestASetThatNeedsEveryRunningValidatorCommitsWhenTheyStartInDifferentRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, tc := range []struct {
		name          string
		validators    int
		silent        int
		firstRounds   []int
		allowedRounds float64
	}{
		{"three of four, one three rounds ahead", 4, 1, []int{4, 4, 7}, 60},
		{"five of seven, one four rounds ahead", 7, 2, []int{4, 4, 4, 4, 8}, 80},
		{"five of seven, two ahead by different amounts", 7, 2, []int{4, 4, 4, 7, 9}, 80},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := defaultWedgeModel(tc.validators, tc.silent)
			r := newWedgeRig(t, m)
			defer r.close()
			for i, first := range tc.firstRounds {
				r.start(i, 0, first)
			}
			started := time.Now()
			r.waitForHeight(t, 3, tc.allowedRounds, fmt.Sprintf("validators started in rounds %v", tc.firstRounds))
			t.Logf("three blocks after %v (%.1f rounds) for validators started in rounds %v", time.Since(started).Round(time.Millisecond), float64(time.Since(started))/float64(m.round()), tc.firstRounds)
			r.requireSafe(t)
		})
	}
}

// The restart that makes it happen on a running chain: all but one of the running validators are
// restarted together (an upgrade, a shared crash) while the one that is not keeps running. It goes
// through the rounds alone, each a whole round timeout, and the restarted validators, which start
// in the first rounds of the height, come back to find it rounds ahead of them. They are level with
// each other and see only it in a later round, so on the engine that follows a validator only when
// it holds a third of the power or more they never leave their rounds for its. They must rejoin
// it and commit again.
func TestRestartedValidatorsRejoinTheOneThatKeptRunningWhenEveryRunningValidatorIsNeeded(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, tc := range []struct {
		name       string
		validators int
		silent     int
	}{
		{"three of four, two restarted", 4, 1},
		{"five of seven, four restarted", 7, 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := defaultWedgeModel(tc.validators, tc.silent)
			r := newWedgeRig(t, m)
			defer r.close()
			for i := 0; i < r.running(); i++ {
				r.start(i, 0, 0)
			}
			r.waitForHeight(t, 3, 60, "before the restart")
			for i := 1; i < r.running(); i++ {
				r.kill(i)
			}
			// The one that kept running goes on alone, a round at a time, and the others boot.
			time.Sleep(m.rounds(12))
			before := r.height()
			for i := 1; i < r.running(); i++ {
				r.start(i, m.rounds(float64(m.bootRounds)), 0)
			}
			r.waitForHeight(t, before+3, 80, "after the restart of all but one of the running validators")
			r.requireSafe(t)
		})
	}
}

// wedgePolka is a block of height 1, built by validator 1, and the prevotes of every running
// validator for it in round: the polka a validator locks on it with.
func wedgePolka(t *testing.T, r *wedgeRig, round int) (*types.Block, []byte, []*SignedVote) {
	t.Helper()
	header := &types.BlockHeader{Height: 1, Timestamp: 1_700_000_000, Validator: r.tv.addrs[1], TxRoot: testTxRoot()}
	block := types.NewBlock(header, nil)
	hash := headerHash(t, block)
	var votes []*SignedVote
	for i := 0; i < r.running(); i++ {
		votes = append(votes, signVoteAs(t, r.tv.keys[i], r.tv.addrs[i], Prevote, hash, round, 1))
	}
	return block, hash, votes
}

// A lock does not keep a set apart, or make it commit anything but the locked block. Three of the
// four validators run and all of them prevoted block X in round 2 of height 1, so it has a polka;
// two of them saw it, locked, precommitted and were restarted, the third did not see it, or lost
// what it saw, and comes back with nothing but the record of its vote. They start in different
// rounds -- the pair locked on X in round 6, the one that is not in round 10, or the other way
// round -- and must commit X: the locked ones refuse anything else, and the one that is not
// locked learns X and the polka, or proposes it when its turn comes, and is accepted.
func TestALockedSetCommitsTheLockedBlockWhenItsValidatorsStartInDifferentRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, tc := range []struct {
		name        string
		locked      []int // the validators locked on X in round 2
		firstRounds []int
	}{
		{"the pair locked, the one ahead not", []int{1, 2}, []int{10, 6, 6}},
		{"the one ahead locked, the pair not", []int{0}, []int{10, 6, 6}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := defaultWedgeModel(4, 1)
			r := newWedgeRig(t, m)
			defer r.close()
			const polkaRound = 2
			block, hash, votes := wedgePolka(t, r, polkaRound)
			for i := 0; i < r.running(); i++ {
				record := signState{Height: 1, Round: polkaRound, Prevote: &signedValue{BlockHash: hash}}
				for _, l := range tc.locked {
					if l == i {
						record.Precommit = &signedValue{BlockHash: hash}
						if err := writeLockSnapshot(r.lockPath(i), lockSnapshot{Height: 1, LockedRound: polkaRound, LockedBlockHash: hash, LockedBlock: block, PolkaVotes: votes}); err != nil {
							t.Fatalf("write the lock of validator %d: %v", i, err)
						}
					}
				}
				if err := writeSignState(r.signPath(i), record); err != nil {
					t.Fatalf("write the vote record of validator %d: %v", i, err)
				}
			}
			for i, first := range tc.firstRounds {
				r.start(i, 0, first)
				if _, _, lock := r.node(i).engine.Status(); (lock == polkaRound) != containsInt(tc.locked, i) {
					t.Fatalf("test setup: validator %d has lock %d after its start, locked validators are %v", i, lock, tc.locked)
				}
			}
			r.waitForHeight(t, 1, 80, fmt.Sprintf("validators started in rounds %v", tc.firstRounds))
			r.requireSafe(t)
			for i := 0; i < r.running(); i++ {
				if blocks := r.chains[i].blocksFrom(1); len(blocks) > 0 {
					if got, _ := blocks[0].Header.Hash(); !bytes.Equal(got, hash) {
						t.Fatalf("validator %d committed block %x at height 1, want the locked block %x", i, got, hash)
					}
				}
			}
		})
	}
}

func containsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// Restarts at random moments, of one, two or all of the running validators, wedge no set in which
// every running validator is needed: the height goes on being decided, no two chains hold
// different blocks, and no validator signs two conflicting votes. On the engine that follows only a
// validator that holds a third of the power or more, most of the runs stop at a height for good.
func TestRestartsDoNotWedgeASetInWhichEveryRunningValidatorIsNeeded(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, tc := range []struct {
		name       string
		validators int
		silent     int
		seeds      int
		restarts   int
	}{
		{"three of four", 4, 1, 4, 6},
		{"five of seven", 7, 2, 2, 6},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := defaultWedgeModel(tc.validators, tc.silent)
			m.restarts = tc.restarts
			m.stallAfter = 50
			for _, rep := range runWedgeSeeds(t, m, 1, tc.seeds, tc.seeds) {
				if rep.wedged {
					// A machine that is busy can make the rounds too short for the messages,
					// whatever the engine does: a wedge is one that comes back.
					rep = runWedge(t, m, rep.seed)
				}
				if rep.wedged {
					t.Errorf("LIVENESS: seed %d: no block for %.0f rounds after %v, at height %d:\n%s", rep.seed, m.stallAfter, rep.at.Round(time.Millisecond), rep.height, rep.diagnosis)
				}
				if rep.forkAt != 0 {
					t.Errorf("SAFETY: seed %d: two chains hold different blocks at height %d", rep.seed, rep.forkAt)
				}
				if rep.doubles != 0 {
					t.Errorf("SAFETY: seed %d: a validator signed conflicting votes (%d slashing proofs can be made)", rep.seed, rep.doubles)
				}
			}
		})
	}
}

// TestWedgeReport prints the wedge rate of the mesh for the engine this file is built with.
// NHB_WEDGE_REPORT enables it: NHB_WEDGE_VALIDATORS (default 4), NHB_WEDGE_SILENT (default 1, 0 is allowed),
// NHB_WEDGE_SEEDS (default 12), NHB_WEDGE_FROM (first seed, default 1), NHB_WEDGE_RESTARTS
// (default 12), NHB_WEDGE_SCALE (the divisor of the round timers, default 25), NHB_WEDGE_MIN_INTERVAL
// (the minimum block interval of a chain with the default timers, divided by the scale like them;
// default none), NHB_WEDGE_PARALLEL (runs at once, default 4), and NHB_WEDGE_TRACE (the restarts of
// every run, not only of the wedged ones). A run that wedged is also one to run again alone: on a
// machine that is busy the rounds can be too short for the messages, whatever the engine.
func TestWedgeReport(t *testing.T) {
	if os.Getenv("NHB_WEDGE_REPORT") == "" {
		t.Skip("set NHB_WEDGE_REPORT to measure how often restarts wedge a validator set")
	}
	silent := 1
	if n, err := strconv.Atoi(os.Getenv("NHB_WEDGE_SILENT")); err == nil && n >= 0 {
		silent = n
	}
	m := defaultWedgeModel(envInt("NHB_WEDGE_VALIDATORS", 4), silent)
	m.restarts = envInt("NHB_WEDGE_RESTARTS", 12)
	m.scale = float64(envInt("NHB_WEDGE_SCALE", 25))
	if d := envDurations("NHB_WEDGE_MIN_INTERVAL", nil); len(d) > 0 {
		m.minInterval = time.Duration(float64(d[0]) / m.scale)
	}
	m.trace = os.Getenv("NHB_WEDGE_TRACE") != ""
	seeds := envInt("NHB_WEDGE_SEEDS", 12)
	reports := runWedgeSeeds(t, m, envInt("NHB_WEDGE_FROM", 1), seeds, envInt("NHB_WEDGE_PARALLEL", 4))
	wedged, locked := 0, 0
	shapes := map[string]int{}
	for _, rep := range reports {
		status := "ok"
		if rep.wedged {
			wedged++
			shapes[rep.shape]++
			status = fmt.Sprintf("WEDGED at %v (height %d, %s)", rep.at.Round(time.Millisecond), rep.height, rep.shape)
		}
		if rep.lockedRestarts > 0 {
			locked++
		}
		if rep.forkAt != 0 {
			status += fmt.Sprintf(" FORK at height %d", rep.forkAt)
		}
		if rep.doubles != 0 {
			status += fmt.Sprintf(" DOUBLE-SIGN x%d", rep.doubles)
		}
		fmt.Fprintf(os.Stderr, "WEDGE|n=%d silent=%d seed=%d|%s|height=%d longest-stall=%v locked-restarts=%d\n", m.validators, m.silent, rep.seed, status, rep.height, rep.longest.Round(time.Millisecond), rep.lockedRestarts)
		if rep.diagnosis != "" && (rep.wedged || m.trace) {
			fmt.Fprintf(os.Stderr, "WEDGE|seed=%d|diagnosis:\n%s\n", rep.seed, rep.diagnosis)
		}
	}
	fmt.Fprintf(os.Stderr, "WEDGE|n=%d silent=%d|%d of %d seeds wedged %v (%d restarts each); %d runs restarted a validator that held a lock\n", m.validators, m.silent, wedged, seeds, shapes, m.restarts, locked)
}
