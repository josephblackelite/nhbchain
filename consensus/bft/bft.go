package bft

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"nhbchain/core/engagement"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// State holds the current height and round of the consensus.
type State struct {
	Height uint64
	Round  int
}

// polkaRecord captures the single block that reached a prevote Polka
// (>=2/3 voting power prevoting the same non-nil block) during one round of
// the current height, along with the actual signed prevotes that
// constitute that Polka -- so this validator, if it becomes proposer
// again later, can attach them as portable, independently-verifiable
// proof (see Proposal.ValidRoundProof and Engine.verifyPolkaProofLocked).
type polkaRecord struct {
	block     *types.Block
	blockHash []byte
	votes     []*SignedVote
}

// Engine is the core BFT consensus state machine.
type Engine struct {
	mu           sync.RWMutex
	privKey      *crypto.PrivateKey
	validatorSet map[string]*big.Int
	broadcaster  p2p.Broadcaster
	node         NodeInterface

	currentState     State
	activeProposal   *SignedProposal
	receivedVotes    map[VoteType]map[string]*SignedVote
	receivedPower    map[VoteType]*big.Int
	totalVotingPower *big.Int
	committedBlocks  map[uint64]bool
	bufferedProposal map[uint64]map[int][]*SignedProposal
	bufferedVotes    map[uint64]map[int][]*SignedVote

	// --- NHB-AUDIT-C1: Proof-of-Lock-Change state (Tendermint-style safety) ---
	//
	// Without this, startNewRound() below wiped every round's vote/proposal
	// state unconditionally, including a precommit this validator had
	// already broadcast for real -- so a validator that simply timed out
	// waiting for a slow quorum on one block was free to precommit a
	// DIFFERENT block in the next round with no memory of the first. Under
	// ordinary network delay (no attacker required), two different
	// quorums could each independently commit a different block at the
	// same height -- a live fork. These fields are this validator's LOCK:
	// once >=2/3 voting power prevotes for a block in some round (a
	// "Polka"), lockedBlock/lockedRound remember it for the rest of this
	// HEIGHT (never cleared by a round timeout, only by advancing to a new
	// height -- see resetLockStateLocked), and prevote()/lockCompliesLocked
	// refuse to prevote a conflicting block afterward unless a later round
	// carries CRYPTOGRAPHIC PROOF (never a bare claim, never a "did I
	// personally witness that round's gossip" check -- see
	// verifyPolkaProofLocked) that >=2/3 power already moved past that
	// lock. validBlock/validRound track the single most recent Polka seen
	// (which may be newer than the lock) so this validator, when it
	// becomes proposer, re-proposes that same value instead of
	// manufacturing a fresh competing one out of its mempool.
	//
	// NHB-AUDIT-C2: lockedBlockHash (not a *types.Block) is deliberately
	// the storage form of the lock -- every real use of the lock
	// (lockCompliesLocked's two bytes.Equal calls) only ever needs the
	// HASH, never the block object, so the hash alone is sufficient to
	// enforce safety. That is what makes it possible to persist just this
	// field to lockSnapshotPath (see lock_snapshot.go) on every write and
	// restore it on restart: a crash between "this validator locked" and
	// "this height committed" would otherwise leave the in-memory lock
	// gone after restart, silently re-permitting a prevote for a
	// conflicting block that a live quorum may already be precommitting
	// around -- undetectable in normal operation (needs a crash at exactly
	// the wrong instant) and only surfacing as a fork under bad luck or an
	// adversary timing a crash deliberately. validBlock/validRound, and the
	// polkaHistory entry for lockedRound (the actual signed prevotes behind
	// the lock), are ALSO persisted and restored on a matching-height
	// restart -- see lock_snapshot.go's LockedBlock/PolkaVotes fields and
	// NewEngine's restore step. An earlier version of this engine persisted
	// only the lock itself and treated the rest as a liveness-only
	// optimization safe to lose on restart; that was wrong, because losing
	// it everywhere at once (e.g. every validator that observed the polka
	// also crashes -- this chain's real topology has exactly two
	// validators) leaves every restored validator holding a prohibition
	// with no possible compliant re-proposal ever again, including the
	// proposer rejecting its own proposal: a permanent liveness deadlock,
	// not merely a lost optimization. See
	// lock_snapshot_deadlock_test.go's
	// TestRestoredLockWithPolkaProofRecoversAndCommits for the regression
	// test.
	lockedBlockHash []byte
	lockedRound     int
	validBlock      *types.Block
	validRound      int
	// lockSnapshotPath, when set via WithLockSnapshotPath, is where the
	// lock (lockedRound/lockedBlockHash) is durably mirrored on every
	// change; see lock_snapshot.go.
	lockSnapshotPath string
	// polkaHistory records, for each round within the CURRENT height, the
	// block and the actual signed prevotes that constituted that round's
	// Polka (if any) -- this validator's own bookkeeping, consulted only
	// when THIS validator becomes proposer again and needs to attach
	// ValidRoundProof to a re-proposal. Never consulted by the RECEIVING
	// side of lockCompliesLocked -- that side verifies the attached proof
	// cryptographically instead, which is what makes this safe even when
	// this validator's own round advanced past vr before independently
	// confirming a peer's polka at that round. Cleared only alongside the
	// lock fields above, on height advance. The entry for lockedRound is
	// the one exception to "in-memory only": it is also what NewEngine
	// restores from lockSnapshotPath after a crash/restart (see
	// lock_snapshot.go), so a validator that crashed after locking still
	// has, once restarted, exactly the one entry it actually needs to
	// satisfy its own restored lock in propose().
	polkaHistory map[int]polkaRecord

	proposalCh chan *SignedProposal
	voteCh     chan *SignedVote
	// externalCommitCh is signaled when a block for the current height was
	// committed OUTSIDE this engine's own round (e.g. synced from a peer
	// that already reached quorum, via core.Node.commitSyncedBlock). The
	// engine otherwise only reconciles e.currentState.Height against the
	// node's real chain height at the top of startNewRound(), which can
	// lag by up to a full commitTimeout after an external commit lands --
	// during that window this node may keep racing its own round for a
	// height the network has already finalized, needlessly stalling
	// (never a safety issue -- it can't manufacture quorum alone -- but a
	// real, observed liveness bug). Signaling this channel makes runRound
	// abandon the stale round immediately instead of waiting out the timer.
	externalCommitCh chan struct{}

	proposalTimeout  time.Duration
	prevoteTimeout   time.Duration
	precommitTimeout time.Duration
	commitTimeout    time.Duration

	prevoteSent   bool
	precommitSent bool
	lastCatchUpAt time.Time
}

// TimeoutConfig captures the per-phase round timers used by the engine.
type TimeoutConfig struct {
	Proposal  time.Duration
	Prevote   time.Duration
	Precommit time.Duration
	Commit    time.Duration
}

// Option mutates the engine during construction.
type Option func(*Engine)

var (
	defaultProposalTimeout  = 2 * time.Second
	defaultPrevoteTimeout   = 2 * time.Second
	defaultPrecommitTimeout = 2 * time.Second
	defaultCommitTimeout    = 4 * time.Second
)

const (
	defaultProposalQueueSize = 16
	defaultVoteQueueSize     = 128
	voteQueueMultiplier      = 2
)

func calculateVoteQueueCapacity(validatorSet map[string]*big.Int) int {
	validatorCount := len(validatorSet)
	if validatorCount == 0 {
		return defaultVoteQueueSize
	}

	scaled := validatorCount * voteQueueMultiplier
	if scaled < defaultVoteQueueSize {
		return defaultVoteQueueSize
	}
	return scaled
}

func defaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		Proposal:  defaultProposalTimeout,
		Prevote:   defaultPrevoteTimeout,
		Precommit: defaultPrecommitTimeout,
		Commit:    defaultCommitTimeout,
	}
}

// WithTimeouts overrides the engine round timers when provided durations are positive.
func WithTimeouts(cfg TimeoutConfig) Option {
	return func(e *Engine) {
		if e == nil {
			return
		}
		if cfg.Proposal > 0 {
			e.proposalTimeout = cfg.Proposal
		}
		if cfg.Prevote > 0 {
			e.prevoteTimeout = cfg.Prevote
		}
		if cfg.Precommit > 0 {
			e.precommitTimeout = cfg.Precommit
		}
		if cfg.Commit > 0 {
			e.commitTimeout = cfg.Commit
		}
	}
}

// WithLockSnapshotPath enables crash-safe persistence of the
// Proof-of-Lock-Change lock (see the Engine struct's lockedBlockHash doc
// comment). NewEngine will attempt to restore the lock from this path if
// present, and the engine keeps it up to date on every lock change/clear
// (see lock_snapshot.go). A blank path (the default when this option is
// never applied) leaves persistence off, matching prior behavior exactly.
func WithLockSnapshotPath(path string) Option {
	return func(e *Engine) {
		if e == nil {
			return
		}
		e.lockSnapshotPath = path
	}
}

func NewEngine(node NodeInterface, key *crypto.PrivateKey, broadcaster p2p.Broadcaster, opts ...Option) *Engine {
	validatorSet := node.GetValidatorSet()
	totalPower := big.NewInt(0)
	for _, weight := range validatorSet {
		if weight != nil {
			totalPower.Add(totalPower, weight)
		}
	}
	nodeHeight := node.GetHeight()
	engine := &Engine{
		node:          node,
		privKey:       key,
		validatorSet:  validatorSet,
		broadcaster:   broadcaster,
		currentState:  State{Height: nodeHeight + 1, Round: 0},
		receivedVotes: make(map[VoteType]map[string]*SignedVote),
		receivedPower: map[VoteType]*big.Int{
			Prevote:   big.NewInt(0),
			Precommit: big.NewInt(0),
		},
		totalVotingPower: totalPower,
		committedBlocks:  make(map[uint64]bool),
		bufferedProposal: make(map[uint64]map[int][]*SignedProposal),
		bufferedVotes:    make(map[uint64]map[int][]*SignedVote),
		lockedRound:      -1,
		validRound:       -1,
		polkaHistory:     make(map[int]polkaRecord),
		proposalCh:       make(chan *SignedProposal, defaultProposalQueueSize),
		voteCh:           make(chan *SignedVote, calculateVoteQueueCapacity(validatorSet)),
		externalCommitCh: make(chan struct{}, 1),
		proposalTimeout:  defaultProposalTimeout,
		prevoteTimeout:   defaultPrevoteTimeout,
		precommitTimeout: defaultPrecommitTimeout,
		commitTimeout:    defaultCommitTimeout,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(engine)
		}
	}

	// NHB-AUDIT-C2: restore the lock from disk, if this engine was
	// configured with a snapshot path and one exists. Only ever applied
	// when snap.Height == engine.currentState.Height -- i.e. the snapshot
	// describes the SAME height this engine is about to contest. A
	// snapshot for an earlier height means that height already committed
	// (durably, since currentState.Height is derived from the node's own
	// real GetHeight() above) and the old lock is stale and MUST NOT be
	// reapplied; a snapshot for a later height should not be possible
	// (this engine has never run at that height yet) and is equally
	// discarded rather than trusted. Either mismatch is treated exactly
	// like "no snapshot" -- restoring nothing is always safe, since an
	// unlocked validator still can never single-handedly cause a fork.
	if snap, err := readLockSnapshot(engine.lockSnapshotPath); err != nil {
		fmt.Printf("failed to read lock snapshot: %v\n", err)
	} else if snap != nil && snap.Height == engine.currentState.Height && snap.LockedRound >= 0 && len(snap.LockedBlockHash) > 0 {
		engine.lockedRound = snap.LockedRound
		engine.lockedBlockHash = snap.LockedBlockHash

		// NHB-AUDIT-C2-followup: the assignment above is the PROHIBITION
		// (never prevote a conflicting block) and is always restored when
		// present, exactly as before this change. What follows is the
		// PERMISSION -- the actual locked block and the signed prevotes
		// that constituted its Polka -- restored into the exact fields
		// propose() already reads (e.validBlock/e.validRound/
		// e.polkaHistory[revalidRound]) so a validator that becomes
		// proposer again after a restart can re-propose the locked value
		// with a valid ValidRoundProof, precisely as an un-crashed
		// validator already does. It is only trusted after being
		// independently re-verified here -- never merely because it is
		// present in the file -- so a corrupt or partial snapshot can
		// never fabricate a lock or a proof that never genuinely happened;
		// on any verification failure this falls back to restoring the
		// prohibition alone, exactly like the pre-fix behavior.
		if snap.LockedBlock != nil && snap.LockedBlock.Header != nil && len(snap.PolkaVotes) > 0 {
			if blockHash, hashErr := snap.LockedBlock.Header.Hash(); hashErr == nil && bytes.Equal(blockHash, snap.LockedBlockHash) {
				if engine.verifyPolkaProofLocked(snap.PolkaVotes, snap.LockedRound, snap.LockedBlockHash, snap.Height) {
					engine.validBlock = snap.LockedBlock
					engine.validRound = snap.LockedRound
					engine.polkaHistory[snap.LockedRound] = polkaRecord{
						block:     snap.LockedBlock,
						blockHash: append([]byte(nil), snap.LockedBlockHash...),
						votes:     snap.PolkaVotes,
					}
				} else {
					fmt.Println("lock snapshot: persisted polka proof failed cryptographic verification; restoring lock without a re-propose value")
				}
			} else {
				fmt.Println("lock snapshot: persisted block hash does not match locked hash; restoring lock without a re-propose value")
			}
		}
	}

	return engine
}

func (e *Engine) Start() {
	fmt.Println("BFT Consensus Engine Started.")
	fmt.Println("BFT waiting for peer connections before starting rounds.")
	time.Sleep(30 * time.Second)
	e.requestStatus()
	for {
		e.runRound()
	}
}

func (e *Engine) runRound() {
	e.startNewRound()

	e.mu.RLock()
	height := e.currentState.Height
	round := e.currentState.Round
	e.mu.RUnlock()
	e.replayBufferedMessages(height, round)

	proposer := e.selectProposer(round)
	myAddr := e.privKey.PubKey().Address().Bytes()
	if bytes.Equal(proposer, myAddr) {
		if err := e.propose(); err != nil {
			fmt.Printf("failed to propose block: %v\n", err)
		} else {
			e.prevote()
		}
	}

	proposalTimer := time.NewTimer(e.proposalTimeout)
	prevoteTimer := time.NewTimer(e.prevoteTimeout)
	precommitTimer := time.NewTimer(e.precommitTimeout)
	commitTimer := time.NewTimer(e.commitTimeout)
	defer func() {
		stopTimer(proposalTimer)
		stopTimer(prevoteTimer)
		stopTimer(precommitTimer)
		stopTimer(commitTimer)
	}()

	for {
		select {
		case <-proposalTimer.C:
			e.prevote()
		case <-prevoteTimer.C:
			e.precommit()
		case <-precommitTimer.C:
			if e.commit() {
				return
			}
		case <-commitTimer.C:
			return
		case <-e.externalCommitCh:
			// A block for this height (or later) was just committed via
			// the peer-sync path rather than this engine's own round.
			// Abandon this round now -- the next startNewRound() will
			// pick up the real height via syncHeightWithNodeLocked()
			// instead of continuing to race for a decided height.
			return
		case sp := <-e.proposalCh:
			if sp == nil || sp.Proposal == nil || sp.Proposal.Block == nil || sp.Proposal.Block.Header == nil {
				continue
			}
			if sp.Proposal.Block.Header.Height != height || sp.Proposal.Round != round {
				continue
			}
			if err := e.node.ValidateBlock(sp.Proposal.Block); err != nil {
				fmt.Printf("rejected invalid proposal for height %d: %v\n", sp.Proposal.Block.Header.Height, err)
				e.mu.Lock()
				e.broadcastPrevoteNilLocked(fmt.Sprintf("invalid proposal: %v", err))
				e.mu.Unlock()
				continue
			}
			if e.acceptProposal(sp) {
				fmt.Printf("Received block proposal for height %d from %x\n", sp.Proposal.Block.Header.Height, sp.Proposer)
				e.prevote()
			}
		case sv := <-e.voteCh:
			if sv == nil || sv.Vote == nil {
				continue
			}
			if sv.Vote.Height != height || sv.Vote.Round != round {
				continue
			}
			added, reachedPrevote, reachedPrecommit := e.addVoteIfRelevant(sv)
			if added {
				fmt.Printf("Received %s vote for block %x from %x\n", sv.Vote.Type, sv.Vote.BlockHash, sv.Validator)
			}
			if reachedPrevote {
				e.precommit()
			}
			if reachedPrecommit && e.commit() {
				return
			}
		}

		e.mu.RLock()
		committed := e.committedBlocks[height]
		e.mu.RUnlock()
		if committed {
			return
		}
	}
}

func (e *Engine) requeueActiveProposal() {
	if e == nil || e.node == nil {
		return
	}
	e.mu.RLock()
	var txs []*types.Transaction
	if e.activeProposal != nil && e.activeProposal.Proposal != nil && e.activeProposal.Proposal.Block != nil && len(e.activeProposal.Proposal.Block.Transactions) > 0 {
		txs = append([]*types.Transaction(nil), e.activeProposal.Proposal.Block.Transactions...)
	}
	e.mu.RUnlock()
	if len(txs) == 0 {
		return
	}
	e.node.RequeueTransactions(txs)
}

func (e *Engine) HandleProposal(p *SignedProposal) error {
	if err := e.verifySignedProposal(p); err != nil {
		return err
	}
	if p == nil || p.Proposal == nil || p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return fmt.Errorf("invalid proposal payload")
	}
	if _, ok := e.validatorSet[string(p.Proposer)]; !ok {
		return fmt.Errorf("proposal from non-validator %x", p.Proposer)
	}

	e.mu.RLock()
	height := e.currentState.Height
	round := e.currentState.Round
	e.mu.RUnlock()

	if p.Proposal.Block.Header.Height != height || p.Proposal.Round < round {
		if p.Proposal.Block.Header.Height > height {
			e.requestCatchUp(height)
		}
		return nil
	}
	if p.Proposal.Round > round {
		e.mu.Lock()
		e.bufferProposalLocked(p)
		e.mu.Unlock()
		return nil
	}

	select {
	case e.proposalCh <- p:
		return nil
	default:
		return fmt.Errorf("proposal queue full")
	}
}

func (e *Engine) HandleVote(v *SignedVote) error {
	if err := e.verifySignedVote(v); err != nil {
		return err
	}
	if v == nil || v.Vote == nil {
		return fmt.Errorf("invalid vote payload")
	}
	if _, ok := e.validatorSet[string(v.Validator)]; !ok {
		return fmt.Errorf("vote from non-validator %x", v.Validator)
	}

	e.mu.RLock()
	height := e.currentState.Height
	round := e.currentState.Round
	e.mu.RUnlock()

	if v.Vote.Height != height || v.Vote.Round < round {
		if v.Vote.Height > height {
			e.requestCatchUp(height)
		}
		return nil
	}
	if v.Vote.Round > round {
		e.mu.Lock()
		e.bufferVoteLocked(v)
		e.mu.Unlock()
		return nil
	}

	e.voteCh <- v
	return nil
}

func (e *Engine) propose() error {
	e.mu.RLock()
	round := e.currentState.Round
	revalidBlock := e.validBlock
	revalidRound := e.validRound
	var revalidProof []*SignedVote
	if revalidBlock != nil {
		if rec, ok := e.polkaHistory[revalidRound]; ok {
			revalidProof = rec.votes
		}
	}
	e.mu.RUnlock()

	// NHB-AUDIT-C1: if this validator itself already observed a Polka for
	// some block (e.validBlock), it MUST re-propose that exact value when
	// it becomes proposer again, attaching the actual signed prevotes as
	// proof, rather than building a fresh one from mempool -- proposing
	// anything else here would just force other (correctly) locked
	// validators to prevote nil, stalling the round for no reason. This is
	// the liveness half of Proof-of-Lock-Change: the safety half
	// (lockCompliesLocked, in prevote()) is what actually prevents a
	// fork; this half is what lets the network still make progress once a
	// value has a Polka behind it instead of stalling forever.
	validRound := -1
	var block *types.Block
	var err error
	if revalidBlock != nil {
		block = revalidBlock
		validRound = revalidRound
	} else {
		txs := e.node.GetMempool()
		if len(txs) == 0 {
			fmt.Println("PROPOSE: Mempool empty, creating empty block proposal.")
			block, err = e.node.CreateBlock(nil)
		} else {
			block, err = e.node.CreateBlock(txs)
		}
		if err != nil {
			return fmt.Errorf("failed to build block: %w", err)
		}
	}
	if block == nil || block.Header == nil {
		return fmt.Errorf("proposed block missing header")
	}
	if err := e.node.ValidateBlock(block); err != nil {
		return fmt.Errorf("local block validation failed: %w", err)
	}

	proposal := &Proposal{Block: block, Round: round, ValidRound: validRound, ValidRoundProof: revalidProof}
	proposalHash := sha256.Sum256(proposal.bytes())
	sig, err := ethcrypto.Sign(proposalHash[:], e.privKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to sign proposal: %w", err)
	}

	signedProposal := &SignedProposal{
		Proposal:  proposal,
		Proposer:  e.privKey.PubKey().Address().Bytes(),
		Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig},
	}

	e.acceptProposal(signedProposal)

	payload, err := json.Marshal(signedProposal)
	if err != nil {
		return fmt.Errorf("failed to marshal proposal: %w", err)
	}
	msg := &p2p.Message{Type: p2p.MsgTypeProposal, Payload: payload}
	e.broadcaster.Broadcast(msg)
	fmt.Println("PROPOSE: Broadcasting our new block proposal.")
	return nil
}

func (e *Engine) prevote() {
	e.mu.Lock()
	if e.prevoteSent || e.activeProposal == nil || e.activeProposal.Proposal == nil || e.activeProposal.Proposal.Block == nil || e.activeProposal.Proposal.Block.Header == nil {
		e.mu.Unlock()
		return
	}
	blockHash, err := e.activeProposal.Proposal.Block.Header.Hash()
	if err != nil {
		e.mu.Unlock()
		fmt.Printf("failed to hash block for prevote: %v\n", err)
		return
	}
	round := e.currentState.Round
	height := e.currentState.Height

	// NHB-AUDIT-C1: this is the safety half of Proof-of-Lock-Change. Refuse
	// to prevote a block that conflicts with an earlier lock unless the
	// proposal carries cryptographic proof (>=2/3 signed prevotes) that
	// enough voting power already moved on -- never merely because the
	// proposer's message claims it, and never by asking whether this
	// validator personally witnessed that round's gossip live (an earlier
	// version of this fix relied on the latter and could deadlock
	// permanently when validators' round counters drifted out of sync;
	// see the Engine struct's lockedBlockHash doc comment). This is what makes
	// it impossible for two different quorums to each commit a different
	// block at this height, no matter how round timeouts and message
	// delays play out.
	if !e.lockCompliesLocked(e.activeProposal.Proposal, blockHash, height) {
		e.prevoteSent = true
		reason := fmt.Sprintf("proposal for round %d (validRound=%d) conflicts with lock at round %d",
			round, e.activeProposal.Proposal.ValidRound, e.lockedRound)
		e.broadcastPrevoteNilLocked(reason)
		e.mu.Unlock()
		fmt.Printf("PREVOTE NIL: refusing to prevote a locked-conflicting proposal: %s\n", reason)
		return
	}
	e.prevoteSent = true
	e.mu.Unlock()

	vote, err := e.createVote(Prevote, blockHash, round, height)
	if err != nil {
		fmt.Printf("failed to create prevote: %v\n", err)
		e.mu.Lock()
		e.prevoteSent = false
		e.mu.Unlock()
		return
	}

	added, reachedPrevote, reachedPrecommit := e.addVoteIfRelevant(vote)
	if added {
		fmt.Printf("PREVOTE: Recorded our prevote for block %x\n", blockHash)
	}

	e.broadcastVote(vote)
	fmt.Println("PREVOTE: Broadcasting our prevote.")

	if reachedPrevote {
		e.precommit()
	}
	if reachedPrecommit {
		e.commit()
	}
}

// lockCompliesLocked reports whether prevoting for blockHash (the hash of
// proposal.Block) is safe given this validator's current lock state. Must
// be called with e.mu held. This directly implements Tendermint's
// Proof-of-Lock-Change rule:
//
//   - proposal.ValidRound < 0 (a fresh value, not a re-proposal): allowed
//     only if this validator is unlocked, or the proposed block IS the
//     locked block.
//   - proposal.ValidRound >= 0 (proposer claims a Polka at that round):
//     allowed only if (a) the claimed round is strictly earlier than this
//     proposal's own round, (b) proposal.ValidRoundProof cryptographically
//     verifies as a real >=2/3 Polka for this EXACT block at that round
//     (see verifyPolkaProofLocked -- the proposer's claim alone is never
//     trusted, but neither is this validator's own possibly-incomplete
//     memory of that round), and (c) this validator's lock (if any) is
//     from that round or earlier, or already matches this block.
func (e *Engine) lockCompliesLocked(proposal *Proposal, blockHash []byte, height uint64) bool {
	if proposal == nil {
		return false
	}
	vr := proposal.ValidRound
	if vr < 0 {
		if e.lockedRound < 0 {
			return true
		}
		return bytes.Equal(e.lockedBlockHashLocked(), blockHash)
	}
	if vr >= proposal.Round {
		return false
	}
	if !e.verifyPolkaProofLocked(proposal.ValidRoundProof, vr, blockHash, height) {
		return false
	}
	if e.lockedRound < 0 || e.lockedRound <= vr {
		return true
	}
	return bytes.Equal(e.lockedBlockHashLocked(), blockHash)
}

// verifyPolkaProofLocked cryptographically verifies that proof contains
// enough distinct, validly-signed Prevote signatures for
// (height, round=vr, blockHash) to constitute a real Polka (>=2/3 voting
// power) against the validator set active for the current height. This is
// a pure, stateless check -- it never asks whether this validator itself
// witnessed round vr's gossip in real time, which is exactly what makes it
// safe even when this validator's own round counter has already advanced
// past vr (see the Engine struct's lockedBlockHash doc comment for why that
// distinction matters). Must be called with e.mu held.
func (e *Engine) verifyPolkaProofLocked(proof []*SignedVote, vr int, blockHash []byte, height uint64) bool {
	if len(proof) == 0 || e.totalVotingPower == nil || e.totalVotingPower.Sign() <= 0 {
		return false
	}
	seen := make(map[string]struct{}, len(proof))
	signedPower := big.NewInt(0)
	for _, sv := range proof {
		if sv == nil || sv.Vote == nil {
			continue
		}
		if sv.Vote.Type != Prevote || sv.Vote.Round != vr || sv.Vote.Height != height || !bytes.Equal(sv.Vote.BlockHash, blockHash) {
			// Signature doesn't cover the exact claim being made -- reject
			// rather than trust a vote for a different height/round/block.
			continue
		}
		key := string(sv.Validator)
		if _, dup := seen[key]; dup {
			continue
		}
		weight, isValidator := e.validatorSet[key]
		if !isValidator || weight == nil || weight.Sign() <= 0 {
			continue
		}
		if err := e.verifySignedVote(sv); err != nil {
			continue
		}
		seen[key] = struct{}{}
		signedPower.Add(signedPower, weight)
	}
	threshold := new(big.Int).Mul(e.totalVotingPower, big.NewInt(2))
	threshold.Add(threshold, big.NewInt(2))
	threshold.Div(threshold, big.NewInt(3))
	return signedPower.Cmp(threshold) >= 0
}

// lockedBlockHashLocked returns the hash of the currently locked block, or
// nil if unlocked. Must be called with e.mu held. A plain field getter --
// see the Engine struct's lockedBlockHash doc comment for why the hash
// (rather than the block object) is what this engine actually stores.
func (e *Engine) lockedBlockHashLocked() []byte {
	return e.lockedBlockHash
}

func (e *Engine) precommit() {
	e.mu.Lock()
	if e.precommitSent || e.activeProposal == nil || e.activeProposal.Proposal == nil || e.activeProposal.Proposal.Block == nil || e.activeProposal.Proposal.Block.Header == nil {
		e.mu.Unlock()
		return
	}
	if !e.hasTwoThirdsPowerLocked(Prevote) {
		e.mu.Unlock()
		return
	}
	blockHash, err := e.activeProposal.Proposal.Block.Header.Hash()
	if err != nil {
		e.mu.Unlock()
		fmt.Printf("failed to hash block for precommit: %v\n", err)
		return
	}
	round := e.currentState.Round
	height := e.currentState.Height
	e.precommitSent = true
	e.mu.Unlock()

	vote, err := e.createVote(Precommit, blockHash, round, height)
	if err != nil {
		fmt.Printf("failed to create precommit: %v\n", err)
		e.mu.Lock()
		e.precommitSent = false
		e.mu.Unlock()
		return
	}

	added, _, reachedPrecommit := e.addVoteIfRelevant(vote)
	if added {
		fmt.Printf("PRECOMMIT: Recorded our precommit for block %x\n", blockHash)
	}

	e.broadcastVote(vote)
	fmt.Println("PRECOMMIT: Broadcasting our precommit.")

	if reachedPrecommit {
		e.commit()
	}
}

func (e *Engine) commit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.activeProposal == nil || e.activeProposal.Proposal == nil || e.activeProposal.Proposal.Block == nil || e.activeProposal.Proposal.Block.Header == nil {
		return false
	}
	if e.committedBlocks[e.currentState.Height] {
		return true
	}
	if !e.hasTwoThirdsPowerLocked(Precommit) {
		return false
	}

	// Try to commit the block; on failure, broadcast prevote(nil) and reset.
	block := e.activeProposal.Proposal.Block
	block.QuorumCert = e.buildQuorumCertLocked(block, e.activeProposal.Proposal.Round)
	fmt.Printf("COMMIT: Attempting to commit block %d.\n", block.Header.Height)
	if err := e.node.CommitBlock(block); err != nil {
		fmt.Printf("failed to commit block: %v\n", err)
		e.broadcastPrevoteNilLocked(fmt.Sprintf("execution failure: %v", err)) // assumes lock is held
		e.resetProposalStateLocked()                                          // reset for next round; lock/valid state deliberately untouched, see resetLockStateLocked
		return false
	}
	fmt.Printf("COMMIT: Successfully committed block %d.\n", block.Header.Height)

	e.committedBlocks[e.currentState.Height] = true
	e.currentState.Height++
	e.currentState.Round = 0
	e.resetLockStateLocked()
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.validatorSet = e.node.GetValidatorSet()
	e.recalculateVotingPowerLocked()
	e.syncHeightWithNodeLocked()
	e.broadcastCommittedBlock(block)
	e.broadcastStatus()
	return true
}

// buildQuorumCertLocked packages the precommit signatures that already
// contributed to this commit's 2/3+ quorum (e.receivedVotes[Precommit],
// still populated at this point -- see commit()'s hasTwoThirdsPowerLocked
// check just above) into a types.QuorumCert attached to the block before
// it is committed and gossiped onward. This reuses the exact signatures
// each validator already produced during the live round; no extra signing
// happens here. NHB-TRIAGE-C1: without this, a block committed by a real
// BFT quorum carried no portable proof of that fact, so any node receiving
// it later via ordinary P2P sync (not the live round) had no way to verify
// quorum was ever reached -- see core/node.go's QuorumCert verification on
// the synced-block path. Must be called with e.mu held (same requirement
// as hasTwoThirdsPowerLocked, whose result gates this call).
func (e *Engine) buildQuorumCertLocked(block *types.Block, round int) *types.QuorumCert {
	if block == nil || block.Header == nil {
		return nil
	}
	headerHash, err := block.Header.Hash()
	if err != nil {
		fmt.Printf("buildQuorumCert: hash header: %v\n", err)
		return nil
	}
	votes := e.receivedVotes[Precommit]
	if len(votes) == 0 {
		return nil
	}
	sigs := make([]types.QuorumSignature, 0, len(votes))
	for _, sv := range votes {
		if sv == nil || sv.Signature == nil || sv.Validator == nil {
			continue
		}
		if sv.Signature.Scheme != SignatureSchemeSecp256k1 {
			// Only secp256k1 votes are ever produced by createVote today
			// (see consensus/bft/types.go) -- skip anything else rather
			// than attaching a signature the verifier's recover-based
			// check can't handle.
			continue
		}
		sigs = append(sigs, types.QuorumSignature{
			Validator: append([]byte(nil), sv.Validator...),
			Signature: append([]byte(nil), sv.Signature.Signature...),
		})
	}
	if len(sigs) == 0 {
		return nil
	}
	return &types.QuorumCert{
		Height:     block.Header.Height,
		Round:      round,
		BlockHash:  headerHash,
		Signatures: sigs,
	}
}

func (e *Engine) requestStatus() {
	if e == nil || e.broadcaster == nil {
		return
	}
	msg, err := p2p.NewGetStatusMessage()
	if err != nil {
		fmt.Printf("failed to build status request: %v\n", err)
		return
	}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast status request: %v\n", err)
	}
}

// NotifyExternalCommit tells the engine a block was just committed to the
// chain through some path other than this engine's own round (e.g. a synced
// block adopted from a peer that already reached quorum). It's safe to call
// from any goroutine, at any height, at any time -- it never blocks and it
// makes no claim about which height changed, since the engine re-derives the
// real height itself from e.node.GetHeight() on its next round start. This
// only needs to happen when the node is genuinely behind (an in-flight round
// still targeting an already-finalized height); calling it spuriously just
// causes one harmless extra round restart.
func (e *Engine) NotifyExternalCommit() {
	if e == nil {
		return
	}
	select {
	case e.externalCommitCh <- struct{}{}:
	default:
	}
}

func (e *Engine) requestCatchUp(currentHeight uint64) {
	if e == nil || e.broadcaster == nil {
		return
	}
	e.mu.Lock()
	now := time.Now()
	if !e.lastCatchUpAt.IsZero() && now.Sub(e.lastCatchUpAt) < time.Second {
		e.mu.Unlock()
		return
	}
	e.lastCatchUpAt = now
	e.mu.Unlock()
	msg, err := p2p.NewGetBlocksMessage(currentHeight + 1)
	if err != nil {
		fmt.Printf("failed to build catch-up request: %v\n", err)
		return
	}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast catch-up request: %v\n", err)
	}
}

func (e *Engine) broadcastCommittedBlock(block *types.Block) {
	if e == nil || e.broadcaster == nil || block == nil {
		return
	}
	msg, err := p2p.NewBlockMessage(block)
	if err != nil {
		fmt.Printf("failed to build committed block message: %v\n", err)
		return
	}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast committed block: %v\n", err)
	}
}

func (e *Engine) broadcastStatus() {
	if e == nil || e.broadcaster == nil || e.node == nil {
		return
	}
	msg, err := p2p.NewStatusMessage(e.node.GetHeight())
	if err != nil {
		fmt.Printf("failed to build status message: %v\n", err)
		return
	}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast status: %v\n", err)
	}
}

func (e *Engine) broadcastVote(vote *SignedVote) {
	payload, err := json.Marshal(vote)
	if err != nil {
		fmt.Printf("failed to marshal vote: %v\n", err)
		return
	}
	msg := &p2p.Message{Type: p2p.MsgTypeVote, Payload: payload}
	e.broadcaster.Broadcast(msg)
}

func (e *Engine) createVote(t VoteType, blockHash []byte, round int, height uint64) (*SignedVote, error) {
	vote := &Vote{BlockHash: blockHash, Round: round, Type: t, Height: height}
	voteHash := sha256.Sum256(vote.bytes())
	sig, err := ethcrypto.Sign(voteHash[:], e.privKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign vote: %w", err)
	}
	return &SignedVote{
		Vote:      vote,
		Validator: e.privKey.PubKey().Address().Bytes(),
		Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig},
	}, nil
}

func (e *Engine) acceptProposal(p *SignedProposal) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeProposal != nil {
		return false
	}
	e.activeProposal = p
	return true
}

func (e *Engine) addVoteIfRelevant(v *SignedVote) (bool, bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeProposal == nil || e.activeProposal.Proposal == nil || e.activeProposal.Proposal.Block == nil || e.activeProposal.Proposal.Block.Header == nil || v == nil || v.Vote == nil {
		return false, false, false
	}

	expectedHash, err := e.activeProposal.Proposal.Block.Header.Hash()
	if err != nil {
		fmt.Printf("failed to hash active proposal: %v\n", err)
		return false, false, false
	}
	if !bytes.Equal(expectedHash, v.Vote.BlockHash) {
		return false, false, false
	}

	voteMap, ok := e.receivedVotes[v.Vote.Type]
	if !ok {
		voteMap = make(map[string]*SignedVote)
		e.receivedVotes[v.Vote.Type] = voteMap
	}
	key := string(v.Validator)
	if _, exists := voteMap[key]; exists {
		return false, e.hasTwoThirdsPowerLocked(Prevote), e.hasTwoThirdsPowerLocked(Precommit)
	}
	voteMap[key] = v

	weight := e.validatorSet[key]
	if weight == nil {
		weight = big.NewInt(0)
	}
	if e.receivedPower == nil {
		e.receivedPower = make(map[VoteType]*big.Int)
	}
	if _, ok := e.receivedPower[v.Vote.Type]; !ok {
		e.receivedPower[v.Vote.Type] = big.NewInt(0)
	}
	e.receivedPower[v.Vote.Type] = new(big.Int).Add(e.receivedPower[v.Vote.Type], weight)

	reachedPrevote := e.hasTwoThirdsPowerLocked(Prevote)
	reachedPrecommit := e.hasTwoThirdsPowerLocked(Precommit)

	// NHB-AUDIT-C1: the instant >=2/3 voting power prevotes this round's
	// block for the FIRST time (a Polka), lock onto it for the rest of this
	// height and snapshot the actual signed prevotes that constitute it --
	// both read by lockCompliesLocked/propose() to enforce/continue
	// Proof-of-Lock-Change. The snapshot (not just the fact it happened)
	// is what lets this validator, if it becomes proposer again, hand
	// OTHER validators cryptographic proof instead of asking them to trust
	// its memory or their own -- see Proposal.ValidRoundProof's doc
	// comment for why that distinction is the whole point of this design.
	// Guarded by polkaHistory's own presence check so a Polka is recorded
	// (and the lock set) at most once per round.
	if v.Vote.Type == Prevote && reachedPrevote {
		round := e.currentState.Round
		if _, already := e.polkaHistory[round]; !already {
			block := e.activeProposal.Proposal.Block
			hashCopy := append([]byte(nil), expectedHash...)
			votes := make([]*SignedVote, 0, len(voteMap))
			for _, sv := range voteMap {
				votes = append(votes, sv)
			}
			e.polkaHistory[round] = polkaRecord{block: block, blockHash: hashCopy, votes: votes}
			e.validBlock = block
			e.validRound = round
			e.lockedBlockHash = hashCopy
			e.lockedRound = round
			// NHB-AUDIT-C2: mirror the new lock to disk immediately, before
			// this vote's caller ever acts on it (e.g. broadcasting our own
			// prevote/precommit for this height/round) -- see
			// lock_snapshot.go for why an atomic overwrite here is
			// sufficient for crash safety without append-log/fsync-ordering
			// complexity. A write failure is logged, never fatal: it only
			// degrades this feature back to pre-NHB-AUDIT-C2 behavior for
			// this one height, it never corrupts the in-memory lock that
			// just took effect above.
			//
			// NHB-AUDIT-C2-followup: LockedBlock/PolkaVotes below persist
			// the exact same block and votes slice just written into
			// polkaHistory[round] above -- the liveness PERMISSION, not
			// just the safety PROHIBITION -- so a restart that loses this
			// in-memory polkaHistory (see the Engine struct's polkaHistory
			// doc comment) can still recover it from disk via NewEngine
			// instead of leaving every restarted validator holding a lock
			// no future proposal could ever satisfy again.
			if err := writeLockSnapshot(e.lockSnapshotPath, lockSnapshot{
				Height:          e.currentState.Height,
				LockedRound:     e.lockedRound,
				LockedBlockHash: e.lockedBlockHash,
				LockedBlock:     block,
				PolkaVotes:      votes,
			}); err != nil {
				fmt.Printf("failed to write lock snapshot: %v\n", err)
			}
		}
	}

	return true, reachedPrevote, reachedPrecommit
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// NOTE: called with e.mu **locked**
func (e *Engine) broadcastPrevoteNilLocked(reason string) {
	if e.broadcaster == nil {
		return
	}
	round := e.currentState.Round
	height := e.currentState.Height

	vote, err := e.createVote(Prevote, nil, round, height) // nil = vote for NIL
	if err != nil {
		fmt.Printf("failed to create prevote nil: %v\n", err)
		return
	}

	if _, ok := e.receivedVotes[Prevote]; !ok {
		e.receivedVotes[Prevote] = make(map[string]*SignedVote)
	}
	e.receivedVotes[Prevote][string(vote.Validator)] = vote

	payload, _ := json.Marshal(vote)
	msg := &p2p.Message{Type: p2p.MsgTypeVote, Payload: payload}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast prevote nil: %v\n", err)
		return
	}
	fmt.Printf("PREVOTE NIL: Broadcasting nil vote: %s\n", reason)
}

// NOTE: called with e.mu **locked**
func (e *Engine) resetProposalStateLocked() {
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.resetVoteTrackingLocked()
}

func (e *Engine) resetVoteTrackingLocked() {
	e.receivedVotes = map[VoteType]map[string]*SignedVote{
		Prevote:   make(map[string]*SignedVote),
		Precommit: make(map[string]*SignedVote),
	}
	e.receivedPower = map[VoteType]*big.Int{
		Prevote:   big.NewInt(0),
		Precommit: big.NewInt(0),
	}
}

func (e *Engine) recalculateVotingPowerLocked() {
	if e.totalVotingPower == nil {
		e.totalVotingPower = big.NewInt(0)
	}
	e.totalVotingPower.SetInt64(0)
	for _, weight := range e.validatorSet {
		if weight != nil {
			e.totalVotingPower.Add(e.totalVotingPower, weight)
		}
	}
}

func (e *Engine) hasTwoThirdsPowerLocked(vt VoteType) bool {
	if e.totalVotingPower == nil || e.totalVotingPower.Sign() <= 0 {
		return false
	}
	power, ok := e.receivedPower[vt]
	if !ok || power == nil {
		return false
	}
	threshold := new(big.Int).Mul(e.totalVotingPower, big.NewInt(2))
	threshold.Add(threshold, big.NewInt(2))
	threshold.Div(threshold, big.NewInt(3))
	return power.Cmp(threshold) >= 0
}

func (e *Engine) startNewRound() {
	e.requeueActiveProposal()
	e.mu.Lock()
	defer e.mu.Unlock()

	heightBefore := e.currentState.Height
	if !e.syncHeightWithNodeLocked() {
		if e.committedBlocks[e.currentState.Height] {
			delete(e.committedBlocks, e.currentState.Height)
			e.currentState.Height++
			e.currentState.Round = 0
			e.syncHeightWithNodeLocked()
		} else {
			e.currentState.Round++
		}
	}
	// NHB-AUDIT-C1: a round TIMING OUT must never clear the lock -- that
	// was the exact bug (see the Engine struct's lockedBlockHash doc comment).
	// The lock/valid/polka state is per-HEIGHT, so it's only ever reset
	// here when this call actually advanced the height (via the resync or
	// catch-up branches above), never on an ordinary same-height round
	// bump.
	if e.currentState.Height != heightBefore {
		e.resetLockStateLocked()
	}
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.validatorSet = e.node.GetValidatorSet()
	e.recalculateVotingPowerLocked()
	if nextRound, ok := e.nextBufferedRoundLocked(e.currentState.Height, e.currentState.Round+1); ok {
		e.currentState.Round = nextRound
	}
	e.resetVoteTrackingLocked()
	fmt.Printf("\n--- Starting BFT round for Height: %d, Round: %d ---\n", e.currentState.Height, e.currentState.Round)
}

// resetLockStateLocked clears all Proof-of-Lock-Change state. Must be
// called with e.mu held, and only when the engine is genuinely moving to a
// new height (a lock/valid/polka observation from height H must never
// leak into height H+1's decisions) -- never merely because a round timed
// out within the same height.
func (e *Engine) resetLockStateLocked() {
	e.lockedBlockHash = nil
	e.lockedRound = -1
	e.validBlock = nil
	e.validRound = -1
	e.polkaHistory = make(map[int]polkaRecord)
	// NHB-AUDIT-C2: overwrite the on-disk snapshot to match -- tagged with
	// e.currentState.Height, which by the time either call site reaches
	// this function is already the NEW (post-advance) height (commit()
	// increments it at line ~727 before calling this; startNewRound()'s
	// call is gated on syncHeightWithNodeLocked/the catch-up branch having
	// already updated it). This is what prevents a stale on-disk lock from
	// height H being wrongly reapplied at height H+1: even if the process
	// crashes immediately after this write, NewEngine's restore only ever
	// applies a snapshot whose Height equals the height it is about to
	// contest, and this write already recorded that height as unlocked. If
	// the crash instead happens BEFORE this write (between the height
	// advance above and reaching this line), NewEngine still rejects the
	// old, still-locked snapshot on restart because ITS Height field is the
	// prior height, which by then is behind the node's real committed
	// chain height -- see the height-equality check in NewEngine.
	if err := writeLockSnapshot(e.lockSnapshotPath, lockSnapshot{
		Height:      e.currentState.Height,
		LockedRound: -1,
	}); err != nil {
		fmt.Printf("failed to clear lock snapshot: %v\n", err)
	}
}

func (e *Engine) syncHeightWithNodeLocked() bool {
	if e.node == nil {
		return false
	}
	nodeHeight := e.node.GetHeight()
	for height := range e.committedBlocks {
		if height <= nodeHeight {
			delete(e.committedBlocks, height)
		}
	}
	if e.currentState.Height <= nodeHeight {
		e.currentState.Height = nodeHeight + 1
		e.currentState.Round = 0
		for height := range e.bufferedProposal {
			if height <= nodeHeight {
				delete(e.bufferedProposal, height)
			}
		}
		for height := range e.bufferedVotes {
			if height <= nodeHeight {
				delete(e.bufferedVotes, height)
			}
		}
		return true
	}
	return false
}

func (e *Engine) bufferProposalLocked(p *SignedProposal) {
	if p == nil || p.Proposal == nil || p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return
	}
	height := p.Proposal.Block.Header.Height
	round := p.Proposal.Round
	if _, ok := e.bufferedProposal[height]; !ok {
		e.bufferedProposal[height] = make(map[int][]*SignedProposal)
	}
	e.bufferedProposal[height][round] = append(e.bufferedProposal[height][round], p)
}

func (e *Engine) bufferVoteLocked(v *SignedVote) {
	if v == nil || v.Vote == nil {
		return
	}
	height := v.Vote.Height
	round := v.Vote.Round
	if _, ok := e.bufferedVotes[height]; !ok {
		e.bufferedVotes[height] = make(map[int][]*SignedVote)
	}
	e.bufferedVotes[height][round] = append(e.bufferedVotes[height][round], v)
}

func (e *Engine) nextBufferedRoundLocked(height uint64, minRound int) (int, bool) {
	next := 0
	found := false
	if rounds := e.bufferedProposal[height]; rounds != nil {
		for round, proposals := range rounds {
			if round < minRound || len(proposals) == 0 {
				continue
			}
			if !found || round < next {
				next = round
				found = true
			}
		}
	}
	if rounds := e.bufferedVotes[height]; rounds != nil {
		for round, votes := range rounds {
			if round < minRound || len(votes) == 0 {
				continue
			}
			if !found || round < next {
				next = round
				found = true
			}
		}
	}
	return next, found
}

func (e *Engine) replayBufferedMessages(height uint64, round int) {
	e.mu.Lock()
	var proposals []*SignedProposal
	if rounds := e.bufferedProposal[height]; rounds != nil {
		proposals = append(proposals, rounds[round]...)
		delete(rounds, round)
		if len(rounds) == 0 {
			delete(e.bufferedProposal, height)
		}
	}
	var votes []*SignedVote
	if rounds := e.bufferedVotes[height]; rounds != nil {
		votes = append(votes, rounds[round]...)
		delete(rounds, round)
		if len(rounds) == 0 {
			delete(e.bufferedVotes, height)
		}
	}
	e.mu.Unlock()

	for _, proposal := range proposals {
		select {
		case e.proposalCh <- proposal:
		default:
			fmt.Printf("dropping buffered proposal for height %d round %d: proposal queue full\n", height, round)
		}
	}
	for _, vote := range votes {
		select {
		case e.voteCh <- vote:
		default:
			fmt.Printf("dropping buffered vote for height %d round %d: vote queue full\n", height, round)
		}
	}
}

// maxEngagementBoostBps bounds how much a validator's EngagementScore can
// add to its stake-based proposer-selection weight: at most this many
// basis points of the validator's OWN stake, reached only at maximum
// engagement. Deliberately modest and stake-anchored (never a standalone
// weight, never able to exceed a validator's own stake-derived power) --
// engagement resists neither Sybil nor farming attacks the way locked stake
// does (see docs/issue30.md item 22), so its influence on who proposes
// blocks is capped rather than unbounded.
const maxEngagementBoostBps = 2000

func engagementWeightBoost(stake *big.Int, engagementScore uint64) *big.Int {
	if stake == nil || stake.Sign() <= 0 {
		return big.NewInt(0)
	}
	engagementCap := engagement.DefaultConfig().DailyCap
	if engagementCap == 0 {
		return big.NewInt(0)
	}
	if engagementScore > engagementCap {
		engagementScore = engagementCap
	}
	boost := new(big.Int).Mul(stake, new(big.Int).SetUint64(engagementScore))
	boost.Mul(boost, big.NewInt(maxEngagementBoostBps))
	boost.Div(boost, new(big.Int).SetUint64(engagementCap*10000))
	return boost
}

func (e *Engine) selectProposer(round int) []byte {
	keys := make([]string, 0, len(e.validatorSet))
	for addrStr := range e.validatorSet {
		keys = append(keys, addrStr)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0 })

	var (
		validators [][]byte
		weights    []*big.Int
		totalPower = big.NewInt(0)
	)

	for _, addrStr := range keys {
		addrBytes := []byte(addrStr)
		account, err := e.node.GetAccount(addrBytes)
		if err != nil {
			continue
		}

		stake := account.Stake
		power := new(big.Int).Add(stake, engagementWeightBoost(stake, account.EngagementScore))

		validators = append(validators, addrBytes)
		weights = append(weights, power)
		totalPower.Add(totalPower, power)
	}

	if len(validators) == 0 {
		return nil
	}
	if totalPower.Sign() == 0 {
		return validators[round%len(validators)]
	}

	lastCommit := e.node.GetLastCommitHash()
	roundBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(roundBytes, uint64(round))
	seedInput := append(append([]byte{}, lastCommit...), roundBytes...)
	seedHash := sha256.Sum256(seedInput)
	pick := new(big.Int).Mod(new(big.Int).SetBytes(seedHash[:]), totalPower)

	for i, addrBytes := range validators {
		weight := weights[i]
		if pick.Cmp(weight) < 0 {
			fmt.Printf("Deterministic proposer selection: %s (Power: %s)\n", crypto.MustNewAddress(crypto.NHBPrefix, addrBytes).String(), weight.String())
			return addrBytes
		}
		pick.Sub(pick, weight)
	}

	return validators[0]
}

func (e *Engine) verifySignedProposal(p *SignedProposal) error {
	if p == nil || p.Proposal == nil || p.Signature == nil {
		return fmt.Errorf("invalid signed proposal")
	}
	if p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return fmt.Errorf("proposal missing block header")
	}
	// NHB-AUDIT-C1: a re-proposal (ValidRound >= 0) legitimately carries an
	// earlier round's original author in Header.Validator -- changing it
	// would change the block's hash, breaking the whole point of
	// re-proposing the SAME value that earlier reached a Polka -- while
	// p.Proposer is whoever is actually proposing THIS round. These are
	// only required to match for a fresh proposal (ValidRound < 0), where
	// Header.Validator is set by the proposer building a brand new block.
	// p.Proposer's signature over the full payload (including the
	// attached ValidRoundProof) is still verified below either way, and
	// lockCompliesLocked independently, cryptographically re-verifies the
	// ValidRound claim before anyone acts on it -- relaxing this specific
	// equality check here does not weaken authentication.
	if p.Proposal.ValidRound < 0 {
		if !bytes.Equal(p.Proposer, p.Proposal.Block.Header.Validator) {
			return fmt.Errorf("proposal proposer mismatch")
		}
	}

	hash := sha256.Sum256(p.Proposal.bytes())
	return verifySignature(hash[:], p.Signature, p.Proposer)
}

func (e *Engine) verifySignedVote(v *SignedVote) error {
	if v == nil || v.Vote == nil || v.Signature == nil {
		return fmt.Errorf("invalid signed vote")
	}
	hash := sha256.Sum256(v.Vote.bytes())
	return verifySignature(hash[:], v.Signature, v.Validator)
}

func verifySignature(msgHash []byte, sig *Signature, expectedAddr []byte) error {
	if sig == nil {
		return fmt.Errorf("missing signature")
	}
	switch sig.Scheme {
	case SignatureSchemeSecp256k1:
		if len(sig.Signature) != 65 {
			return fmt.Errorf("invalid secp256k1 signature length")
		}
		pubKey, err := ethcrypto.SigToPub(msgHash, sig.Signature)
		if err != nil {
			return fmt.Errorf("secp256k1 recover failed: %w", err)
		}
		recovered := ethcrypto.PubkeyToAddress(*pubKey).Bytes()
		if !bytes.Equal(recovered, expectedAddr) {
			return fmt.Errorf("signature address mismatch")
		}
		return nil
	case SignatureSchemeEd25519:
		if len(sig.PublicKey) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid ed25519 public key length")
		}
		if !ed25519.Verify(sig.PublicKey, msgHash, sig.Signature) {
			return fmt.Errorf("invalid ed25519 signature")
		}
		recovered := ethcrypto.Keccak256(sig.PublicKey)[12:]
		if !bytes.Equal(recovered, expectedAddr) {
			return fmt.Errorf("signature address mismatch")
		}
		return nil
	default:
		return fmt.Errorf("unsupported signature scheme %q", sig.Scheme)
	}
}

func (vt VoteType) String() string {
	if vt == Prevote {
		return "Prevote"
	}
	return "Precommit"
}
