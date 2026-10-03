package bft

// Validators that are rounds apart must come back together.
//
// A round that does not commit lasts one commit timeout, the same for everyone, so
// two groups of validators that are a round or more apart stay that far apart
// unless the round-sync rule (round_sync.go, supportedRoundLocked) moves the group
// that is behind: what the group ahead says is heard by the group behind, but what
// the group behind says is for a round the group ahead has left, and is dropped.
// When neither group holds a quorum of the voting power on its own -- three equal
// validators with one of them ahead, five with two ahead, six split three and three
// -- block production stops until the group behind is moved.
//
// The first test drives real engines over real signed messages and the real round
// start; the second runs them over real (scaled) timers and a mesh that delivers
// every message to every peer, the way the network does, and waits for a block.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/p2p"
)

// splitCase is a set of validators of which the ones listed in ahead are in a later
// round than the others.
type splitCase struct {
	name   string
	powers []int64
	ahead  []int
}

func (c splitCase) isAhead(idx int) bool {
	for _, a := range c.ahead {
		if a == idx {
			return true
		}
	}
	return false
}

// splitCases are the shapes of split that matter: the group ahead holds exactly a
// third of the power, or more than a third but not enough to make a quorum with one
// of the others, and the group behind cannot make a quorum without it.
var splitCases = []splitCase{
	{"three equal, one ahead", []int64{1, 1, 1}, []int{2}},
	{"two staked two to one, the smaller ahead", []int64{2, 1}, []int{1}},
	{"two equal, one ahead", []int64{1, 1}, []int{1}},
	{"five equal, two ahead", []int64{1, 1, 1, 1, 1}, []int{3, 4}},
	{"six equal, two ahead", []int64{1, 1, 1, 1, 1, 1}, []int{4, 5}},
	{"six equal, three ahead", []int64{1, 1, 1, 1, 1, 1}, []int{3, 4, 5}},
	{"seven equal, three ahead", []int64{1, 1, 1, 1, 1, 1, 1}, []int{4, 5, 6}},
}

// splitEngines builds one engine for each validator of tv, none of them running.
func splitEngines(t *testing.T, tv *testValidators, opts ...Option) ([]*Engine, []*signGuardNode, []*fanoutBroadcaster) {
	t.Helper()
	engines := make([]*Engine, len(tv.keys))
	nodes := make([]*signGuardNode, len(tv.keys))
	brs := make([]*fanoutBroadcaster, len(tv.keys))
	for i := range tv.keys {
		nodes[i] = &signGuardNode{validatorSet: tv.set, self: tv.addrs[i]}
		brs[i] = &fanoutBroadcaster{}
		engines[i] = NewEngine(nodes[i], tv.keys[i], brs[i], opts...)
	}
	return engines, nodes, brs
}

// Every validator sends a vote for the round it is in to every other, as it does
// each round, and a validator that is told of enough of the power in a later round
// leaves its own at once (the wake-up the round loop acts on). Whatever the gap
// between the groups, after that exchange every validator is in the same round, and
// stays in step through the rounds after it. A group that holds less than a third of
// the power moves nobody.
func TestSplitGroupsOfValidatorsMeetInOneRound(t *testing.T) {
	for _, gap := range []int{1, 2, 5, 40} {
		for _, tc := range splitCases {
			gap, tc := gap, tc
			t.Run(fmt.Sprintf("%s, %d rounds apart", tc.name, gap), func(t *testing.T) {
				tv := newWeightedValidators(t, tc.powers...)
				engines, _, _ := splitEngines(t, tv)
				const behindRound = 3
				for i, e := range engines {
					round := behindRound
					if tc.isAhead(i) {
						round = behindRound + gap
					}
					e.mu.Lock()
					e.currentState = State{Height: 1, Round: round}
					e.mu.Unlock()
				}

				exchange := func() {
					for from := range engines {
						round := roundOf(engines[from])
						for to := range engines {
							if to == from {
								continue
							}
							vote := signVoteAs(t, tv.keys[from], tv.addrs[from], Prevote, sha256Sum([]byte("some block")), round, 1)
							// An error here is a full queue for the current round, which
							// this test does not read.
							_ = engines[to].HandleVote(vote)
						}
					}
					for _, e := range engines {
						select {
						case <-e.roundSkipCh:
							e.startNewRound() // what the round loop does when it is woken
						default:
						}
						drainVotes(e)
					}
				}
				inOneRound := func() (int, bool) {
					first := roundOf(engines[0])
					for _, e := range engines[1:] {
						if roundOf(e) != first {
							return 0, false
						}
					}
					return first, true
				}

				exchange()
				round, together := inOneRound()
				if !together {
					rounds := make([]int, len(engines))
					for i, e := range engines {
						rounds[i] = roundOf(e)
					}
					t.Fatalf("LIVENESS: after hearing each other the validators are in rounds %v, not one", rounds)
				}
				if round != behindRound+gap {
					t.Fatalf("the validators met in round %d, want the round of the group that was ahead, %d", round, behindRound+gap)
				}

				// A round that does not commit ends for everyone at once; they go on
				// together, and nothing splits them again.
				for i := 0; i < 3; i++ {
					for _, e := range engines {
						e.startNewRound()
					}
					exchange()
					got, together := inOneRound()
					if !together || got != round+i+1 {
						t.Fatalf("after %d more rounds the validators are not in step: want all in round %d", i+1, round+i+1)
					}
				}
			})
		}
	}
}

// A group that holds less than a third of the power is not followed, whichever round
// it says it is in: the validators that hold the rest stay in their own rounds.
func TestAGroupBelowAThirdOfThePowerMovesNobody(t *testing.T) {
	cases := []splitCase{
		{"one of four ahead", []int64{1, 1, 1, 1}, []int{3}},
		{"two of seven ahead", []int64{1, 1, 1, 1, 1, 1, 1}, []int{5, 6}},
		{"one of six ahead", []int64{1, 1, 1, 1, 1, 1}, []int{5}},
		{"the small validator of two staked ten to four", []int64{10, 4}, []int{1}},
		{"a little less than a third of the stake", []int64{6, 2, 1, 1}, []int{2, 3}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tv := newWeightedValidators(t, tc.powers...)
			engines, _, _ := splitEngines(t, tv)
			const behindRound, gap = 3, 40
			for i, e := range engines {
				round := behindRound
				if tc.isAhead(i) {
					round = behindRound + gap
				}
				e.mu.Lock()
				e.currentState = State{Height: 1, Round: round}
				e.mu.Unlock()
			}
			for from := range engines {
				for to := range engines {
					if to == from {
						continue
					}
					vote := signVoteAs(t, tv.keys[from], tv.addrs[from], Prevote, sha256Sum([]byte("some block")), roundOf(engines[from]), 1)
					_ = engines[to].HandleVote(vote)
				}
			}
			for i, e := range engines {
				if tc.isAhead(i) {
					continue
				}
				if len(e.roundSkipCh) != 0 {
					t.Fatalf("validator %d was woken to leave its round by validators holding less than a third of the power", i)
				}
				e.startNewRound()
				if got := roundOf(e); got != behindRound+1 {
					t.Fatalf("validator %d is in round %d, want %d: it followed validators holding less than a third of the power", i, got, behindRound+1)
				}
			}
		})
	}
}

func drainVotes(e *Engine) {
	for {
		select {
		case <-e.voteCh:
		default:
			return
		}
	}
}

// fanoutBroadcaster delivers what an engine sends to every engine of the test but
// that one, one goroutine per message like a real network, and records it.
type fanoutBroadcaster struct {
	mu    sync.Mutex
	peers []fanoutPeer
	msgs  []*p2p.Message
}

type fanoutPeer struct {
	engine *Engine
	node   *signGuardNode // where a committed block sent to the peer is applied
}

func (b *fanoutBroadcaster) Broadcast(msg *p2p.Message) error {
	b.mu.Lock()
	b.msgs = append(b.msgs, msg)
	peers := append([]fanoutPeer(nil), b.peers...)
	b.mu.Unlock()
	payload := append([]byte(nil), msg.Payload...)
	kind := msg.Type
	for _, peer := range peers {
		peer := peer
		go func() {
			switch kind {
			case p2p.MsgTypeProposal:
				var sp SignedProposal
				if json.Unmarshal(payload, &sp) == nil {
					_ = peer.engine.HandleProposal(&sp)
				}
			case p2p.MsgTypeVote:
				var sv SignedVote
				if json.Unmarshal(payload, &sv) == nil {
					_ = peer.engine.HandleVote(&sv)
				}
			case p2p.MsgTypeBlock:
				// A committed block is how a validator that was not in the round that
				// decided it learns of the height (see meshBroadcaster).
				var block types.Block
				if json.Unmarshal(payload, &block) == nil {
					peer.node.applySynced(&block)
					peer.engine.NotifyExternalCommit()
				}
			}
		}()
	}
	return nil
}

func (b *fanoutBroadcaster) messages() []*p2p.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*p2p.Message(nil), b.msgs...)
}

// connectAll makes every engine send to every other.
func connectAll(engines []*Engine, nodes []*signGuardNode, brs []*fanoutBroadcaster) {
	for i, br := range brs {
		br.mu.Lock()
		for j := range engines {
			if j != i {
				br.peers = append(br.peers, fanoutPeer{engine: engines[j], node: nodes[j]})
			}
		}
		br.mu.Unlock()
	}
}

// splitRun is one set of running engines over a mesh, none of which has committed.
type splitRun struct {
	t       *testing.T
	tv      *testValidators
	engines []*Engine
	nodes   []*signGuardNode
	brs     []*fanoutBroadcaster
	stop    atomic.Bool
	done    []chan struct{}
}

// startSplit builds the engines (validators in the group ahead start `gap` rounds
// after the others) and starts their rounds at the same moment.
func startSplit(t *testing.T, tc splitCase, gap int) *splitRun {
	t.Helper()
	tv := newWeightedValidators(t, tc.powers...)
	engines, nodes, brs := splitEngines(t, tv, e2eOptions()...)
	connectAll(engines, nodes, brs)
	r := &splitRun{t: t, tv: tv, engines: engines, nodes: nodes, brs: brs}
	for i, e := range engines {
		if tc.isAhead(i) {
			e.mu.Lock()
			e.currentState = State{Height: 1, Round: gap}
			e.mu.Unlock()
		}
	}
	for i, e := range engines {
		r.done = append(r.done, runLoop(e, nodes[i], &r.stop))
	}
	return r
}

// wait blocks until every node has committed height 1, or the deadline, and stops
// the round loops.
func (r *splitRun) wait(within time.Duration) bool {
	deadline := time.After(within)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	ok := false
loop:
	for {
		select {
		case <-tick.C:
			all := true
			for _, n := range r.nodes {
				if n.GetHeight() < 1 {
					all = false
					break
				}
			}
			if all {
				ok = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	r.stop.Store(true)
	for _, e := range r.engines {
		endRound(e)
	}
	for _, done := range r.done {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return ok
}

// check asserts what must hold whatever the timing: every validator committed the
// same block, and none signed two conflicting votes.
func (r *splitRun) check() {
	r.t.Helper()
	first := r.nodes[0].committedHash(r.t)
	for i, n := range r.nodes[1:] {
		if got := n.committedHash(r.t); !bytes.Equal(first, got) {
			r.t.Fatalf("SAFETY: validators 0 and %d committed different blocks at height 1: %x and %x", i+1, first, got)
		}
	}
	for i, br := range r.brs {
		if votes := votesBy(br.messages(), r.tv.addrs[i]); len(votes) > 0 {
			requireNoSlashableProof(r.t, votes, r.tv.addrs[i])
		}
	}
}

// commitRound is the round the block of height 1 was committed in, or -1.
func (r *splitRun) commitRound() int {
	for _, n := range r.nodes {
		n.mu.Lock()
		round := -1
		if len(n.committed) > 0 && n.committed[0].QuorumCert != nil {
			round = n.committed[0].QuorumCert.Round
		}
		n.mu.Unlock()
		if round >= 0 {
			return round
		}
	}
	return -1
}

// Real engines, real (scaled) timers and a mesh that delivers every message to every
// peer: the validators of a group that is rounds ahead of the rest, in a set in which
// neither group makes a quorum alone, commit a block within a few rounds of starting.
// Before the group behind followed it they never did: the two groups kept the
// distance they started with, round after round.
func TestSplitGroupsOfValidatorsCommitABlock(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	for _, gap := range []int{5, 40} {
		for _, tc := range splitCases {
			gap, tc := gap, tc
			t.Run(fmt.Sprintf("%s, %d rounds apart", tc.name, gap), func(t *testing.T) {
				r := startSplit(t, tc, gap)
				started := time.Now()
				if !r.wait(5 * time.Second) {
					heights := make([]uint64, len(r.nodes))
					rounds := make([]int, len(r.nodes))
					for i := range r.nodes {
						heights[i], rounds[i] = r.nodes[i].GetHeight(), roundOf(r.engines[i])
					}
					t.Fatalf("LIVENESS: no block in %v (%d rounds of this test): heights %v, rounds %v",
						time.Since(started), int(time.Since(started)/e2eRound), heights, rounds)
				}
				t.Logf("committed in round %d after %v", r.commitRound(), time.Since(started))
				r.check()
			})
		}
	}
}

// A group below a third of the power that is far ahead does not drag the others after
// it: the validators that hold the rest commit the block in rounds of their own, and
// the group ahead takes the block from them.
func TestAGroupBelowAThirdFarAheadDoesNotDelayTheRest(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	tc := splitCase{"two of seven ahead", []int64{1, 1, 1, 1, 1, 1, 1}, []int{5, 6}}
	const gap = 40
	r := startSplit(t, tc, gap)
	if !r.wait(5 * time.Second) {
		t.Fatalf("LIVENESS: no block: validators holding five sevenths of the power were not able to decide without the other two")
	}
	if round := r.commitRound(); round < 0 || round >= gap {
		t.Fatalf("the block was committed in round %d: the validators behind were taken to round %d or later by a group holding two sevenths of the power", round, gap)
	}
	r.check()
}
