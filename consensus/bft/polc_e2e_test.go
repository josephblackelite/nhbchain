package bft

// TestTwoValidatorNetworkConvergesUnderAsymmetricDelay is an end-to-end
// simulation of two REAL Engine instances exchanging REAL p2p messages
// over REAL timers, deliberately reproducing the timing condition that
// caused the actual production incident this fix replaced: validator B's
// round-1 prevote is delayed in reaching validator A long enough that A's
// own round timer advances past round 1 before it arrives (exactly what
// HandleVote's real "drop any vote whose round is older than mine" gate
// does to it in production). Meanwhile A's round-1 vote reaches B
// promptly, so B (having both votes) locks on round 1 for real.
//
// This is deliberately NOT a unit test of one mechanism in isolation --
// unlike the rest of this file, it runs runRound() in goroutines against
// real timers and real message-passing, the same shape of test that would
// have caught the actual deadlock before it ever reached production. If
// this test hangs or times out, that is exactly the failure mode being
// guarded against.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/p2p"
)

// simNode is a minimal, self-consistent NodeInterface for the two-engine
// simulation: each simulated validator has its own local chain height and
// committed-block list, exactly like two real, separate processes would.
type simNode struct {
	mu           sync.Mutex
	validatorSet map[string]*big.Int
	height       uint64
	committed    []*types.Block
	self         []byte
	seq          int
}

func (n *simNode) GetMempool() []*types.Transaction             { return nil }
func (n *simNode) RequeueTransactions(_ []*types.Transaction)   {}
func (n *simNode) CreateBlock(_ []*types.Transaction) (*types.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seq++
	header := &types.BlockHeader{Height: n.height + 1, Validator: n.self, PrevHash: []byte(fmt.Sprintf("seq-%d", n.seq))}
	return types.NewBlock(header, nil), nil
}
func (n *simNode) ValidateBlock(b *types.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if b == nil || b.Header == nil {
		return fmt.Errorf("nil block")
	}
	if b.Header.Height != n.height+1 {
		return fmt.Errorf("height mismatch: got %d want %d", b.Header.Height, n.height+1)
	}
	return nil
}
func (n *simNode) CommitBlock(b *types.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if b == nil || b.Header == nil {
		return fmt.Errorf("nil block")
	}
	if b.Header.Height != n.height+1 {
		return fmt.Errorf("out of order commit: got %d want %d", b.Header.Height, n.height+1)
	}
	n.committed = append(n.committed, b)
	n.height = b.Header.Height
	return nil
}
func (n *simNode) GetValidatorSet() map[string]*big.Int { return n.validatorSet }
func (n *simNode) GetAccount(addr []byte) (*types.Account, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	w := n.validatorSet[string(addr)]
	if w == nil {
		w = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(w)}, nil
}
func (n *simNode) GetLastCommitHash() []byte { return nil }
func (n *simNode) GetHeight() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.height
}
func (n *simNode) committedHashes(t *testing.T) [][]byte {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([][]byte, 0, len(n.committed))
	for _, b := range n.committed {
		h, err := b.Header.Hash()
		if err != nil {
			t.Fatalf("hash committed block: %v", err)
		}
		out = append(out, h)
	}
	return out
}

// simBroadcaster delivers every message this engine sends to the paired
// peer engine, asynchronously (matching real network delivery), with an
// optional per-message delay hook used to simulate the exact asymmetric
// timing that caused the real incident.
type simBroadcaster struct {
	peer  *Engine
	delay func(msgType byte) time.Duration
}

func (b *simBroadcaster) Broadcast(msg *p2p.Message) error {
	d := time.Duration(0)
	if b.delay != nil {
		d = b.delay(msg.Type)
	}
	peer := b.peer
	payload := append([]byte(nil), msg.Payload...)
	msgType := msg.Type
	go func() {
		if d > 0 {
			time.Sleep(d)
		}
		switch msgType {
		case p2p.MsgTypeProposal:
			var sp SignedProposal
			if err := json.Unmarshal(payload, &sp); err == nil {
				peer.HandleProposal(&sp)
			}
		case p2p.MsgTypeVote:
			var sv SignedVote
			if err := json.Unmarshal(payload, &sv); err == nil {
				peer.HandleVote(&sv)
			}
		}
	}()
	return nil
}

func TestTwoValidatorNetworkConvergesUnderAsymmetricDelay(t *testing.T) {
	f := newTwoValidatorFixture(t)

	nodeA := &simNode{validatorSet: f.validator, self: f.addrs[0]}
	nodeB := &simNode{validatorSet: f.validator, self: f.addrs[1]}

	timeouts := TimeoutConfig{
		Proposal:  30 * time.Millisecond,
		Prevote:   30 * time.Millisecond,
		Precommit: 30 * time.Millisecond,
		Commit:    30 * time.Millisecond,
	}

	// B's round-1 prevote reaching A is delayed long enough that A's own
	// round timer has already advanced well past round 1 by the time it
	// arrives -- exactly the condition that caused the real deadlock.
	// Every other message flows promptly in both directions.
	const roundOneDelayToA = 400 * time.Millisecond
	broadcasterA := &simBroadcaster{}
	broadcasterB := &simBroadcaster{
		delay: func(msgType byte) time.Duration {
			if msgType == p2p.MsgTypeVote {
				return roundOneDelayToA
			}
			return 0
		},
	}

	engineA := NewEngine(nodeA, f.keys[0], broadcasterA, WithTimeouts(timeouts))
	engineB := NewEngine(nodeB, f.keys[1], broadcasterB, WithTimeouts(timeouts))
	broadcasterA.peer = engineB
	broadcasterB.peer = engineA

	// Only delay B's very first vote broadcast (its round-1 prevote) --
	// everything from B afterward (including its eventual re-proposal
	// carrying the cryptographic proof) must flow promptly, or this test
	// would just be testing "everything is slow", not the real scenario.
	var delayOnce sync.Once
	broadcasterB.delay = func(msgType byte) time.Duration {
		if msgType != p2p.MsgTypeVote {
			return 0
		}
		used := false
		delayOnce.Do(func() { used = true })
		if used {
			return roundOneDelayToA
		}
		return 0
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 40; i++ {
			engineA.runRound()
			if nodeA.GetHeight() >= 1 {
				break
			}
		}
		close(done)
	}()
	doneB := make(chan struct{})
	go func() {
		for i := 0; i < 40; i++ {
			engineB.runRound()
			if nodeB.GetHeight() >= 1 {
				break
			}
		}
		close(doneB)
	}()

	timeout := time.After(10 * time.Second)
	for done != nil || doneB != nil {
		select {
		case <-done:
			done = nil
		case <-doneB:
			doneB = nil
		case <-timeout:
			t.Fatalf("DEADLOCK REGRESSION: two-validator network failed to commit height 1 within 10s -- this is exactly the production incident this fix must prevent (A height=%d, B height=%d)", nodeA.GetHeight(), nodeB.GetHeight())
		}
	}

	if nodeA.GetHeight() < 1 || nodeB.GetHeight() < 1 {
		t.Fatalf("expected both validators to commit height 1, got A=%d B=%d", nodeA.GetHeight(), nodeB.GetHeight())
	}

	hashesA := nodeA.committedHashes(t)
	hashesB := nodeB.committedHashes(t)
	if len(hashesA) == 0 || len(hashesB) == 0 {
		t.Fatalf("expected both validators to have at least one committed block")
	}
	if !bytes.Equal(hashesA[0], hashesB[0]) {
		t.Fatalf("SAFETY REGRESSION: validators committed DIFFERENT blocks at height 1 (A=%x B=%x) -- a real fork", hashesA[0], hashesB[0])
	}
}
