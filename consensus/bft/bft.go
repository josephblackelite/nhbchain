package bft

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
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
// (>2/3 voting power prevoting the same non-nil block) during one round of
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
	// once >2/3 voting power prevotes for a block in some round (a
	// "Polka"), lockedBlock/lockedRound remember it for the rest of this
	// HEIGHT (never cleared by a round timeout, only by advancing to a new
	// height -- see resetLockStateLocked), and prevote()/lockCompliesLocked
	// refuse to prevote a conflicting block afterward unless a later round
	// carries CRYPTOGRAPHIC PROOF (never a bare claim, never a "did I
	// personally witness that round's gossip" check -- see
	// verifyPolkaProofLocked) that >2/3 power already moved past that
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
	//
	// A record is not always this validator's own tally: learnValidBlock also
	// records the polka of an earlier round that a message proved to it. Such a
	// record is only ever of a round this validator has already passed, and
	// whether a record exists for a round says nothing of whether this
	// validator locked in it -- the lock is lockedRound, and addVoteIfRelevant
	// sets it whatever is recorded here.
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
	// abandon the stale round immediately instead of waiting out the timer --
	// unless the chain has not reached the round's height, which means the
	// signal is for a block this engine committed itself (chainReached).
	externalCommitCh chan struct{}

	proposalTimeout  time.Duration
	prevoteTimeout   time.Duration
	precommitTimeout time.Duration
	commitTimeout    time.Duration

	// minBlockInterval is the shortest time this validator lets pass between seeing
	// one block commit and starting the round of the next height (WithMinBlockInterval;
	// zero starts it at once). lastCommitNanos is when it last saw a block commit --
	// its own, or one that reached the node from a peer (NotifyExternalCommit) -- in
	// nanoseconds on the monotonic clock since started, plus one, so that zero means
	// none yet. It is atomic rather than guarded by mu because NotifyExternalCommit
	// never waits for the engine.
	minBlockInterval time.Duration
	started          time.Time
	lastCommitNanos  atomic.Int64

	prevoteSent   bool
	precommitSent bool
	lastCatchUpAt time.Time

	// ownFailed counts, per height, this validator's own proposals that did
	// not turn into a committed block (the build failed, its own validation
	// rejected it, or the round ended without a commit). emptyAfter is how
	// many such failures at one height make propose() stop offering
	// transactions and propose an empty block instead, which depends on no
	// transaction at all. Zero disables the fallback. Both are guarded by mu.
	ownFailed  map[uint64]int
	emptyAfter int

	// signed is the record of the last vote this validator signed, and
	// signStatePath (WithSignStatePath) is where it is made durable. Together
	// they are the double-sign guard: createVote signs nothing that conflicts
	// with them (see sign_state.go). signMu guards both; createVote takes it
	// while mu may already be held (broadcastPrevoteNilLocked), so the lock
	// order is always mu, then signMu, never the reverse.
	signMu        sync.Mutex
	signed        signState
	signStatePath string

	// Round synchronisation (see round_sync.go). selfAddr is this validator's
	// address. roundClaims is, for each other validator, the highest round it has
	// been seen to send a message in at one height: the evidence a round jump
	// needs. roundSkipCh wakes the round loop when that evidence says this
	// validator should be in a later round than it is. dropNotes rate-limits the
	// warnings about dropped messages. proposerCache holds the proposers chosen
	// for a height and round since it was last cleared (resetProposerCacheLocked:
	// at every round start and whenever the height moves on), and proposerEpoch
	// changes when it is, so a proposer chosen before is never stored into it. All
	// of these are guarded by mu.
	selfAddr      []byte
	roundClaims   map[string]roundClaim
	roundSkipCh   chan struct{}
	dropNotes     map[string]*dropNote
	proposerCache map[proposerKey][]byte
	proposerEpoch uint64
	// earlyVotes holds the votes of the current round that arrived before a
	// proposal was accepted, keyed by validator and vote type (guarded by mu).
	earlyVotes map[string]*SignedVote
	// relayedHeight and relayedRound are the round in which this validator last
	// passed its polka on (relayed says whether it has), so that it does so once a
	// round (guarded by mu).
	relayed       bool
	relayedHeight uint64
	relayedRound  int
	// lockRefused is set when prevote refused a proposal because of the lock and
	// the round loop has not yet acted on it (guarded by mu).
	lockRefused bool
	// queueMu serialises who puts a proposal on proposalCh, so that clearing out
	// what the round loop would ignore (enqueueProposal) cannot lose a proposal
	// another goroutine is queueing at the same time. The round loop, which only
	// takes proposals off the channel, does not use it. It is never held together
	// with mu.
	queueMu sync.Mutex
}

// defaultEmptyAfterFailures is how many of its own failed proposals at one
// height make a proposer fall back to an empty block. Two failures are enough
// to rule out one-off causes (a dropped message, one slow round) while keeping
// a peer-rejection stall to a couple of rounds per height.
const defaultEmptyAfterFailures = 2

// emptyAfterEnv overrides defaultEmptyAfterFailures; "0" disables the fallback.
const emptyAfterEnv = "NHB_BFT_EMPTY_AFTER_FAILURES"

// proposalFeedback is an optional extension of NodeInterface: a node that
// implements it is told how this validator's own proposals fared, so it can
// log, count and attribute failures. It is an optional interface (not part of
// NodeInterface) so the many NodeInterface test doubles keep compiling.
type proposalFeedback interface {
	NoteProposalOutcome(height uint64, round int, kind string, txCount int, err error)
}

// Proposal outcome kinds passed to proposalFeedback.
const (
	outcomeBuildFailed           = "build_failed"
	outcomeLocalValidationFailed = "local_validation_failed"
	outcomeNotCommitted          = "not_committed"
)

func emptyAfterFromEnv() int {
	raw := os.Getenv(emptyAfterEnv)
	if raw == "" {
		return defaultEmptyAfterFailures
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		slog.Warn("ignoring malformed "+emptyAfterEnv, slog.String("value", raw))
		return defaultEmptyAfterFailures
	}
	return n
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

// WithMinBlockInterval makes this validator wait, after it sees a block commit --
// its own commit, or a block that reached the node from a peer -- until d has passed
// (on the monotonic clock) before it starts the round of the next height: before it
// proposes, votes or starts that round's timers. Zero, which is what an engine built
// without the option does, starts the next round at once.
//
// Nothing else in the engine spaces one block from the next, so without it the
// pace of a chain is whatever the messages, the block build and the commit allow --
// tens of milliseconds a block while the validators are in step -- less the time
// lost to rounds that fail and are waited out, which on a two-validator chain takes
// most of the time and makes the pace a matter of how often they fail (about 0.6 to
// 1 blocks a second over hours, and 2.5 in a stretch where fewer rounds failed, with
// the engine at the same speed throughout). An interval makes it a number the
// operator chooses: at most one block per interval, less the rounds that fail. Every
// per-block quantity of the chain (epoch length, emission per epoch, interest per
// block) follows the pace, so it is the operator's to set (config [consensus]
// MinBlockInterval; see docs/consensus/block-cadence.md).
//
// The wait is local. It is not a rule a block is checked against, it puts nothing in
// a message and changes nothing a validator signs or commits; a validator with a
// different value, or none, takes part in the same rounds, which is why it needs no
// coordination beyond the values not being so far apart that a round times out
// before its proposal comes: an interval of more than half the commit timer is
// lowered to half of it. The wait is only ever the rest of d since the last commit
// this validator saw, so it does not delay a round that follows a failed one, or the
// first round of a validator that has just started and has seen none; one that has
// just taken the last blocks from its peer waits at most d after the last of them.
func WithMinBlockInterval(d time.Duration) Option {
	return func(e *Engine) {
		if e == nil {
			return
		}
		if d < 0 {
			d = 0
		}
		e.minBlockInterval = d
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
		roundSkipCh:      make(chan struct{}, 1),
		roundClaims:      make(map[string]roundClaim),
		proposalTimeout:  defaultProposalTimeout,
		prevoteTimeout:   defaultPrevoteTimeout,
		precommitTimeout: defaultPrecommitTimeout,
		commitTimeout:    defaultCommitTimeout,
		ownFailed:        make(map[uint64]int),
		emptyAfter:       emptyAfterFromEnv(),
		started:          time.Now(),
	}
	if key != nil {
		engine.selfAddr = key.PubKey().Address().Bytes()
	}

	for _, opt := range opts {
		if opt != nil {
			opt(engine)
		}
	}
	if limit := engine.commitTimeout / 2; engine.minBlockInterval > limit {
		slog.Warn("BFT: lowering the minimum block interval to half the commit timeout",
			slog.String("event", "min_block_interval_lowered"),
			slog.Duration("configured", engine.minBlockInterval),
			slog.Duration("used", limit))
		engine.minBlockInterval = limit
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
				if !blockBodyMatchesHeader(snap.LockedBlock) {
					// The hash covers the header only: a body that is not the one the
					// header commits to would be re-proposed and fail its validation.
					fmt.Println("lock snapshot: persisted block's transactions are not the ones its header commits to; restoring lock without a re-propose value")
				} else if engine.verifyPolkaProofLocked(snap.PolkaVotes, snap.LockedRound, snap.LockedBlockHash, snap.Height) {
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

	// Restore the double-sign record: what this validator signed before this
	// start. Without it a restart in the middle of a height forgets every vote
	// and can sign a different block for a round it already voted in.
	engine.restoreSignState()

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
	proposer := e.proposerFor(height, round)
	e.replayBufferedMessages(height, round)

	// The block just committed has its interval to run before this round proposes or
	// votes. The height and the round are already this validator's own (startNewRound
	// above), so what the peer sends in the meantime is queued for the round, not
	// taken for a message from the past, and is here when the wait ends.
	if e.waitMinBlockInterval() {
		return
	}

	myAddr := e.privKey.PubKey().Address().Bytes()
	if bytes.Equal(proposer, myAddr) {
		if err := e.propose(); err != nil {
			fmt.Printf("failed to propose block: %v\n", err)
			// Greppable and structured: this line used to be the only signal a
			// stalled proposer produced, and only on stdout.
			slog.Error("LIVENESS: failed to propose block",
				slog.String("event", "propose_failed"),
				slog.Uint64("height", height),
				slog.Int("round", round),
				slog.String("error", boundedErrorText(err)))
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
			if !e.chainReached(height) {
				continue // for a block this engine has already counted: nothing has changed
			}
			return
		case <-e.roundSkipCh:
			// Enough of the voting power has been seen in a later round than
			// this one (round_sync.go, supportedRoundLocked): leave this round
			// now instead of waiting out its timers, and startNewRound moves
			// there. The rest of this round could not reach a quorum without them.
			return
		case sp := <-e.proposalCh:
			if sp == nil || sp.Proposal == nil || sp.Proposal.Block == nil || sp.Proposal.Block.Header == nil {
				continue
			}
			if sp.Proposal.Block.Header.Height != height || sp.Proposal.Round != round {
				continue
			}
			// Only the round's proposer proposes in it. HandleProposal accepts a
			// proposal from any validator, so without this any one validator could
			// front-run the real proposer with a block of its own and draw this
			// validator's prevote (and, sent a second proposal, its answer to that).
			// The proposer was chosen from the chain state at the top of this round,
			// the same choice this validator makes to decide whether to propose.
			if len(proposer) > 0 && !bytes.Equal(sp.Proposer, proposer) {
				fmt.Printf("ignoring a proposal for height %d round %d from %x: this round's proposer is %x\n", height, round, sp.Proposer, proposer)
				continue
			}
			if err := e.node.ValidateBlock(sp.Proposal.Block); err != nil {
				fmt.Printf("rejected invalid proposal for height %d: %v\n", sp.Proposal.Block.Header.Height, err)
				e.rejectProposal(err)
				continue
			}
			if e.acceptProposal(sp) {
				fmt.Printf("Received block proposal for height %d from %x\n", sp.Proposal.Block.Header.Height, sp.Proposer)
				if e.applyEarlyVotes() {
					return
				}
				e.prevote()
				// The proposer may not know what this validator's lock rests on.
				e.relayIfLockRefused()
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

// noteCommitSeen records that a block has just committed, for waitMinBlockInterval.
func (e *Engine) noteCommitSeen() {
	e.lastCommitNanos.Store(int64(time.Since(e.started)) + 1)
}

// waitMinBlockInterval waits out what is left of the minimum block interval, counted
// from the last commit this validator saw, and reports whether the round was given up
// instead: a block committed outside this engine's rounds that the chain holds at
// this round's height (chainReached), or a later round the others are in, means the
// round just started is not the one to propose or vote in, and the round loop starts
// another (the same two events end a round in progress).
// It waits for nothing when there is no interval, no commit yet -- a validator that
// has just started -- or the interval has already passed, as it has after a round
// that failed.
func (e *Engine) waitMinBlockInterval() bool {
	if e.minBlockInterval <= 0 {
		return false
	}
	seen := e.lastCommitNanos.Load()
	if seen == 0 {
		return false
	}
	remaining := e.minBlockInterval - (time.Since(e.started) - time.Duration(seen-1))
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer stopTimer(timer)
	e.mu.RLock()
	height := e.currentState.Height
	e.mu.RUnlock()
	for {
		select {
		case <-timer.C:
			return false
		case <-e.externalCommitCh:
			if e.chainReached(height) {
				return true
			}
		case <-e.roundSkipCh:
			return true
		}
	}
}

// chainReached reports whether the node's chain holds a block at height or later, which
// is what makes a NotifyExternalCommit signal news to the round of that height. The
// signal is sent for every block the node takes from its peer, and the peer's block for
// the height this engine is committing reaches the node while it does: the node commits
// a block once, whichever of the two is first, and answers the second without an error,
// so both report a commit and the engine has counted the block (commit moved it on to
// the next height) by the time the signal is read. Read as news it ended the first
// round of that next height before anything was done in it, startNewRound took it for a
// round that failed and started the height in round 2, and the peer, which had no such
// signal, started it in round 1.
func (e *Engine) chainReached(height uint64) bool {
	return e.node != nil && e.node.GetHeight() >= height
}

// rejectProposal answers a proposal that failed validation with a prevote for
// nil -- once. A validator votes once per round: if it has already prevoted in
// this round (for a block, or nil) a further proposal, however bad, draws no
// second vote. Before this, the nil prevote was signed regardless, so a bad
// proposal that followed a good one made this validator sign two conflicting
// prevotes for one round, which anyone can turn into a slashing proof. The
// double-sign guard in createVote refuses the second vote on its own; checking
// here as well keeps the intent readable and the log quiet.
func (e *Engine) rejectProposal(reason error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prevoteSent {
		fmt.Printf("PREVOTE NIL: not voting again: this validator already prevoted in round %d\n", e.currentState.Round)
		return
	}
	if e.broadcastPrevoteNilLocked(fmt.Sprintf("invalid proposal: %v", reason)) {
		e.prevoteSent = true
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

// ownFailedAt returns how many of this validator's own proposals at height
// have failed.
func (e *Engine) ownFailedAt(height uint64) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ownFailed[height]
}

// noteOwnFailure records a failed own proposal at height and tells the node.
func (e *Engine) noteOwnFailure(height uint64, round int, kind string, txCount int, err error) {
	e.mu.Lock()
	if e.ownFailed == nil {
		e.ownFailed = make(map[uint64]int)
	}
	e.ownFailed[height]++
	e.mu.Unlock()
	if fb, ok := e.node.(proposalFeedback); ok {
		fb.NoteProposalOutcome(height, round, kind, txCount, err)
	}
}

// countOwnUncommittedLocked counts previous as a failed own proposal if this
// validator authored it. Must be called with e.mu held.
func (e *Engine) countOwnUncommittedLocked(previous *SignedProposal) {
	if previous == nil || previous.Proposal == nil || previous.Proposal.Block == nil || previous.Proposal.Block.Header == nil {
		return
	}
	if !bytes.Equal(previous.Proposer, e.privKey.PubKey().Address().Bytes()) {
		return
	}
	height := previous.Proposal.Block.Header.Height
	if height != e.currentState.Height {
		return
	}
	if e.ownFailed == nil {
		e.ownFailed = make(map[uint64]int)
	}
	e.ownFailed[height]++
	if fb, ok := e.node.(proposalFeedback); ok {
		fb.NoteProposalOutcome(height, e.currentState.Round, outcomeNotCommitted, len(previous.Proposal.Block.Transactions), nil)
	}
}

// pruneOwnFailuresLocked forgets failure counts for heights below the current
// one. Must be called with e.mu held.
func (e *Engine) pruneOwnFailuresLocked() {
	for height := range e.ownFailed {
		if height < e.currentState.Height {
			delete(e.ownFailed, height)
		}
	}
}

// boundedErrorText keeps an error's text to a length safe for a log line.
func boundedErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func (e *Engine) HandleProposal(p *SignedProposal) error {
	if p != nil && p.Proposal != nil && !roundInRange(p.Proposal.Round) {
		return fmt.Errorf("proposal round %d is outside 0..%d", p.Proposal.Round, maxRound)
	}
	if err := e.verifySignedProposal(p); err != nil {
		return err
	}
	if p == nil || p.Proposal == nil || p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return fmt.Errorf("invalid proposal payload")
	}

	e.mu.RLock()
	_, isValidator := e.validatorSet[string(p.Proposer)]
	height := e.currentState.Height
	round := e.currentState.Round
	e.mu.RUnlock()
	if !isValidator {
		return fmt.Errorf("proposal from non-validator %x", p.Proposer)
	}

	// A proposal that proves a polka teaches this validator the block and the
	// polka whoever sent it and whichever round it is for (round_sync.go).
	e.learnValidBlock(p)

	if p.Proposal.Block.Header.Height != height || p.Proposal.Round < round {
		if p.Proposal.Block.Header.Height > height {
			e.requestCatchUp(height)
		} else if p.Proposal.Block.Header.Height == height {
			// A validator that is behind: tell it what this one knows of the polka.
			e.relayValidBlock()
		}
		return nil
	}
	if p.Proposal.Round > round {
		// Kept only within the window, only from that round's proposer and in
		// bounded numbers; where its signer is may move this validator (round_sync.go).
		e.handleFutureProposal(p, height)
		return nil
	}

	return e.handleCurrentProposal(p, height, round)
}

func (e *Engine) HandleVote(v *SignedVote) error {
	// Refused before anything is checked, kept or counted: an honest vote is for a
	// round in range and carries the hash of a header or none.
	if v != nil && v.Vote != nil {
		if !roundInRange(v.Vote.Round) {
			return fmt.Errorf("vote round %d is outside 0..%d", v.Vote.Round, maxRound)
		}
		if len(v.Vote.BlockHash) > maxVoteHashLen {
			return fmt.Errorf("vote block hash is %d bytes, at most %d allowed", len(v.Vote.BlockHash), maxVoteHashLen)
		}
	}
	if err := e.verifySignedVote(v); err != nil {
		return err
	}
	if v == nil || v.Vote == nil {
		return fmt.Errorf("invalid vote payload")
	}
	// A secp256k1 signature is verified by recovering the signer, so the public
	// key field is unused for it and not covered by the signature: whatever a
	// relaying peer put in it is room for padding. Keep the vote without it.
	if v.Signature.Scheme == SignatureSchemeSecp256k1 && len(v.Signature.PublicKey) > 0 {
		signature := *v.Signature
		signature.PublicKey = nil
		v = &SignedVote{Vote: v.Vote, Validator: v.Validator, Signature: &signature}
	}

	e.mu.RLock()
	_, isValidator := e.validatorSet[string(v.Validator)]
	height := e.currentState.Height
	round := e.currentState.Round
	e.mu.RUnlock()
	if !isValidator {
		return fmt.Errorf("vote from non-validator %x", v.Validator)
	}

	if v.Vote.Height != height || v.Vote.Round < round {
		if v.Vote.Height > height {
			e.requestCatchUp(height)
		} else if v.Vote.Height == height {
			// A validator that is behind: tell it what this one knows of the polka.
			e.relayValidBlock()
		}
		return nil
	}
	if v.Vote.Round > round {
		// Kept only within the window and in bounded numbers; where its signer
		// is may move this validator (round_sync.go).
		e.mu.Lock()
		decision := e.admitFutureRoundLocked(v.Validator, "vote", height, v.Vote.Round)
		if decision == admitBuffer {
			e.bufferVoteLocked(v)
		}
		e.mu.Unlock()
		if decision != admitNow {
			return nil
		}
		// The round was reached while the vote was being looked at.
	}

	e.voteCh <- v
	return nil
}

func (e *Engine) propose() error {
	e.mu.RLock()
	round := e.currentState.Round
	height := e.currentState.Height
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
		if len(txs) > 0 && e.emptyAfter > 0 && e.ownFailedAt(height) >= e.emptyAfter {
			// This validator's own proposals at this height keep failing to
			// commit although they build and pass its own validation, so
			// something about the transactions on offer is being rejected
			// elsewhere. Stop offering them for this height: an empty block
			// depends on no transaction. They are released, not dropped, and
			// are offered again at the next height.
			fmt.Printf("PROPOSE: %d earlier proposals at height %d did not commit; proposing an empty block.\n", e.ownFailedAt(height), height)
			e.node.RequeueTransactions(txs)
			txs = nil
		}
		if len(txs) == 0 {
			fmt.Println("PROPOSE: Mempool empty, creating empty block proposal.")
			block, err = e.node.CreateBlock(nil)
		} else {
			block, err = e.node.CreateBlock(txs)
		}
		if err != nil {
			e.noteOwnFailure(height, round, outcomeBuildFailed, len(txs), err)
			return fmt.Errorf("failed to build block: %w", err)
		}
	}
	if block == nil || block.Header == nil {
		return fmt.Errorf("proposed block missing header")
	}
	if err := e.node.ValidateBlock(block); err != nil {
		// The transactions of a block that will never be proposed must not stay
		// marked in flight; nothing else would release them.
		e.node.RequeueTransactions(block.Transactions)
		e.noteOwnFailure(height, round, outcomeLocalValidationFailed, len(block.Transactions), err)
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
	// proposal carries cryptographic proof (>2/3 signed prevotes) that
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
		e.lockRefused = true // the round loop tells the proposer what the lock rests on
		e.mu.Unlock()
		fmt.Printf("PREVOTE NIL: refusing to prevote a locked-conflicting proposal: %s\n", reason)
		return
	}
	e.prevoteSent = true
	e.mu.Unlock()

	vote, err := e.createVote(Prevote, blockHash, round, height)
	if err != nil {
		if errors.Is(err, errConflictingVote) {
			// This validator already voted in this round for something else. That
			// vote stands and no other is sent, so prevoteSent stays set.
			fmt.Printf("PREVOTE: not voting for block %x: %v\n", blockHash, err)
			return
		}
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
//     verifies as a real >2/3 Polka for this EXACT block at that round
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
// (height, round=vr, blockHash) to constitute a real Polka (>2/3 voting
// power) against the validator set active for the current height. This is
// a pure, stateless check -- it never asks whether this validator itself
// witnessed round vr's gossip in real time, which is exactly what makes it
// safe even when this validator's own round counter has already advanced
// past vr (see the Engine struct's lockedBlockHash doc comment for why that
// distinction matters). Must be called with e.mu held.
func (e *Engine) verifyPolkaProofLocked(proof []*SignedVote, vr int, blockHash []byte, height uint64) bool {
	return verifyPolkaProof(proof, vr, blockHash, height, e.validatorSet, e.totalVotingPower)
}

// verifyPolkaProof is verifyPolkaProofLocked over a validator set and a total
// voting power that the caller passes in, so that a caller that holds its own
// copy of them can check a proof with no lock held: a proof costs a signature
// check for each vote in it.
func verifyPolkaProof(proof []*SignedVote, vr int, blockHash []byte, height uint64, validators map[string]*big.Int, total *big.Int) bool {
	if len(proof) == 0 || total == nil || total.Sign() <= 0 {
		return false
	}
	_, signedPower := countedPolkaVotes(proof, vr, blockHash, height, validators)
	return types.HasQuorum(signedPower, total)
}

// countedPolkaVotes returns the votes of proof that count towards a polka of
// round vr for blockHash at height -- one for each validator of the set, and only
// if its signature verifies -- and the voting power they hold.
func countedPolkaVotes(proof []*SignedVote, vr int, blockHash []byte, height uint64, validators map[string]*big.Int) ([]*SignedVote, *big.Int) {
	seen := make(map[string]struct{}, len(proof))
	signedPower := big.NewInt(0)
	var counted []*SignedVote
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
		weight, isValidator := validators[key]
		if !isValidator || weight == nil || weight.Sign() <= 0 {
			continue
		}
		if err := verifyVoteSignature(sv); err != nil {
			continue
		}
		seen[key] = struct{}{}
		signedPower.Add(signedPower, weight)
		counted = append(counted, sv)
	}
	return counted, signedPower
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
		if errors.Is(err, errConflictingVote) {
			// Already precommitted in this round for something else: that vote
			// stands and no other is sent, so precommitSent stays set.
			fmt.Printf("PRECOMMIT: not voting for block %x: %v\n", blockHash, err)
			return
		}
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

	// Try to commit the block; on failure, broadcast prevote(nil) and reset. A
	// validator that already prevoted this block in this round (the usual case:
	// it prevoted and precommitted it before getting here) does not get to
	// change that vote: the prevote for nil is then refused by the double-sign
	// guard and only the reset happens. The reset clears the "already voted"
	// flags for the next proposal, but not the guard's record, so no proposal in
	// this round can draw a vote that conflicts with the ones already signed.
	block := e.activeProposal.Proposal.Block
	block.QuorumCert = e.buildQuorumCertLocked(block, e.activeProposal.Proposal.Round)
	fmt.Printf("COMMIT: Attempting to commit block %d.\n", block.Header.Height)
	if err := e.node.CommitBlock(block); err != nil {
		fmt.Printf("failed to commit block: %v\n", err)
		e.broadcastPrevoteNilLocked(fmt.Sprintf("execution failure: %v", err)) // assumes lock is held
		e.resetProposalStateLocked()                                          // reset for next round; lock/valid state deliberately untouched, see resetLockStateLocked
		return false
	}
	e.noteCommitSeen()
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
	// Who proposes a round depends on the last block, which is new, and the validator
	// set may be too: nothing chosen at the height just committed holds for the next.
	// Proposals for it are handled from here on, before the round loop has started
	// its first round.
	e.resetProposerCacheLocked()
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
// still targeting an already-finalized height); a signal read when the chain
// has not reached the height of the round in progress is for a block the
// engine has already counted and changes nothing (chainReached).
func (e *Engine) NotifyExternalCommit() {
	if e == nil {
		return
	}
	// A block committed here counts for the minimum block interval as one this
	// engine committed itself.
	e.noteCommitSeen()
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

// createVote is the only place a vote is signed. It first claims the vote's
// slot with the double-sign guard (claimVote): a validator that has already
// signed a different value for this height, round and vote type -- or has
// already signed for a later round -- gets errConflictingVote and no signature,
// whichever path asked. See sign_state.go.
func (e *Engine) createVote(t VoteType, blockHash []byte, round int, height uint64) (*SignedVote, error) {
	if err := e.claimVote(t, blockHash, round, height); err != nil {
		return nil, err
	}
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
	if v == nil || v.Vote == nil {
		return false, false, false
	}
	if e.activeProposal == nil || e.activeProposal.Proposal == nil || e.activeProposal.Proposal.Block == nil || e.activeProposal.Proposal.Block.Header == nil {
		// A vote ahead of the proposal it is for (messages of one round do not
		// always arrive in the order they were sent, and the ones replayed from the
		// buffer at a round start are queued separately from the proposal): keep it
		// until the proposal has been accepted instead of losing it.
		e.stashEarlyVoteLocked(v)
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

	// NHB-AUDIT-C1: the instant >2/3 voting power prevotes this round's
	// block for the FIRST time (a Polka), lock onto it for the rest of this
	// height and snapshot the actual signed prevotes that constitute it --
	// both read by lockCompliesLocked/propose() to enforce/continue
	// Proof-of-Lock-Change. The snapshot (not just the fact it happened)
	// is what lets this validator, if it becomes proposer again, hand
	// OTHER validators cryptographic proof instead of asking them to trust
	// its memory or their own -- see Proposal.ValidRoundProof's doc
	// comment for why that distinction is the whole point of this design.
	// Guarded by the round of the lock itself so a Polka is recorded (and the
	// lock set) at most once per round, and never raised again to a round this
	// validator has already locked in. It is not guarded by whether polkaHistory
	// holds a record of the round: a record is not a lock, and a record of the
	// round made from anything but this tally must not stand in for locking,
	// or this validator would precommit a block it is not locked on.
	if v.Vote.Type == Prevote && reachedPrevote {
		round := e.currentState.Round
		if e.lockedRound < round {
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

// broadcastPrevoteNilLocked signs and broadcasts a prevote for nil in the current
// round and reports whether it did. It does not when this validator has already
// voted in the round for anything else (the double-sign guard refuses -- a nil
// prevote after a block prevote is exactly the conflicting pair a slashing proof
// is made of), when it cannot sign, or when there is no broadcaster.
//
// NOTE: called with e.mu **locked**
func (e *Engine) broadcastPrevoteNilLocked(reason string) bool {
	if e.broadcaster == nil {
		return false
	}
	round := e.currentState.Round
	height := e.currentState.Height

	vote, err := e.createVote(Prevote, nil, round, height) // nil = vote for NIL
	if err != nil {
		if errors.Is(err, errConflictingVote) {
			fmt.Printf("PREVOTE NIL: not voting nil (%s): %v\n", reason, err)
		} else {
			fmt.Printf("failed to create prevote nil: %v\n", err)
		}
		return false
	}

	if _, ok := e.receivedVotes[Prevote]; !ok {
		e.receivedVotes[Prevote] = make(map[string]*SignedVote)
	}
	e.receivedVotes[Prevote][string(vote.Validator)] = vote

	payload, _ := json.Marshal(vote)
	msg := &p2p.Message{Type: p2p.MsgTypeVote, Payload: payload}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast prevote nil: %v\n", err)
		return false
	}
	fmt.Printf("PREVOTE NIL: Broadcasting nil vote: %s\n", reason)
	return true
}

// NOTE: called with e.mu **locked**
func (e *Engine) resetProposalStateLocked() {
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.resetVoteTrackingLocked()
}

func (e *Engine) resetVoteTrackingLocked() {
	e.earlyVotes = nil
	e.lockRefused = false
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

// hasTwoThirdsPowerLocked reports whether strictly more than two thirds of
// the voting power has cast vt for the active proposal (types.HasQuorum;
// exactly 2/3 is not enough). Must be called with e.mu held.
func (e *Engine) hasTwoThirdsPowerLocked(vt VoteType) bool {
	if e.totalVotingPower == nil || e.totalVotingPower.Sign() <= 0 {
		return false
	}
	power, ok := e.receivedPower[vt]
	if !ok || power == nil {
		return false
	}
	return types.HasQuorum(power, e.totalVotingPower)
}

func (e *Engine) startNewRound() {
	e.requeueActiveProposal()
	e.mu.Lock()
	defer e.mu.Unlock()

	heightBefore := e.currentState.Height
	previous := e.activeProposal
	if !e.syncHeightWithNodeLocked() {
		if e.committedBlocks[e.currentState.Height] {
			delete(e.committedBlocks, e.currentState.Height)
			e.currentState.Height++
			e.currentState.Round = 0
			e.syncHeightWithNodeLocked()
		} else {
			// Same height, no commit: if the round that just ended was
			// carrying this validator's own proposal, that proposal failed.
			e.countOwnUncommittedLocked(previous)
			e.currentState.Round = roundAfter(e.currentState.Round)
		}
	} else {
		// A block this engine did not commit -- the node took it from its peer -- has
		// moved the chain on: what starts is a new height, not the next round of one that
		// failed, and it starts in the round every new height starts in. That is round 1:
		// commit leaves round 0 and the branch above moves on one round from it, after
		// this engine's own commit and at a start. It stayed in round 0 here, so the
		// validator that was told a block started the next height one round behind the
		// one that committed it, and a validator sends nothing in a round in which it
		// neither proposes nor has a proposal: the two heard nothing from each other until
		// the round of the one behind timed out. Round 1 and not 0 because an engine that
		// has not been replaced yet starts the height after its own commit in round 1,
		// and the two must agree.
		e.currentState.Round = roundAfter(e.currentState.Round)
	}
	e.pruneOwnFailuresLocked()
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
	// The proposers chosen so far were chosen from the previous validator set.
	e.resetProposerCacheLocked()
	// Move on to a later round than the next one only when enough of the voting
	// power has been seen there (round_sync.go, supportedRoundLocked) -- never on
	// a single validator's message, and to the highest round that is supported,
	// not to the lowest one anyone has mentioned.
	if target, ok := e.supportedRoundLocked(); ok && target > e.currentState.Round {
		e.currentState.Round = min(target, maxJumpRound)
	}
	// Never start a round this validator has already signed in (or one before
	// it): the double-sign guard would refuse every vote there, so the round
	// could only be dead. This is what a restart does -- the engine comes back at
	// the first round of the height while its record says it voted in a later one
	// -- and it skips the rounds in between at once instead of waiting each out.
	// In a running process it changes nothing: the round has always moved past the
	// last one signed in.
	if floor, ok := e.signedRoundFloor(e.currentState.Height); ok && e.currentState.Round <= floor {
		e.currentState.Round = roundAfter(floor)
	}
	// Whatever was kept for the rounds this one has passed or jumped over can no
	// longer be replayed. The wake-up for a round jump is spent: the evidence that
	// raised it has just been used, and anything that arrives from here on
	// raises it again.
	e.purgeBufferedBelowLocked(e.currentState.Height, e.currentState.Round)
	select {
	case <-e.roundSkipCh:
	default:
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
		// The node has a new last block: what was chosen for the old height is not
		// what is chosen for this one.
		e.resetProposerCacheLocked()
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

// roundInRange reports whether round is one this engine is ever in or takes a
// message for: 0 to maxRound.
func roundInRange(round int) bool {
	return round >= 0 && round <= maxRound
}

// roundAfter returns the round after round, never above maxRound and never
// negative, whatever round is (it may come from a record on disk). At maxRound it
// stays there: the round cannot be left, which takes more than a hundred years of
// rounds that never committed to reach (a message cannot take the engine within
// half of it, see maxJumpRound) and is no worse than the wrap it replaces.
func roundAfter(round int) int {
	if round < 0 {
		return 0
	}
	if round >= maxRound {
		return maxRound
	}
	return round + 1
}

func (e *Engine) selectProposer(round int) []byte {
	return e.selectProposerIn(e.validatorSet, round)
}

// selectProposerIn chooses the proposer of round from validatorSet, so that a
// caller that does not hold e.mu can pass a snapshot of the set (see proposerFor).
func (e *Engine) selectProposerIn(validatorSet map[string]*big.Int, round int) []byte {
	keys := make([]string, 0, len(validatorSet))
	for addrStr := range validatorSet {
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
		// Unsigned, so that any round (not only 0 to maxRound) picks a validator.
		return validators[uint64(round)%uint64(len(validators))]
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
	return verifyVoteSignature(v)
}

// verifyVoteSignature checks that v is signed by the validator it names. It reads
// nothing of the engine's state.
func verifyVoteSignature(v *SignedVote) error {
	if v == nil || v.Vote == nil || v.Signature == nil {
		return fmt.Errorf("invalid signed vote")
	}
	hash := sha256.Sum256(v.Vote.bytes())
	return verifySignature(hash[:], v.Signature, v.Validator)
}

// secp256k1HalfN is half the order of the secp256k1 group: the largest s that
// ethcrypto.Sign produces.
var secp256k1HalfN = new(big.Int).Rsh(ethcrypto.S256().Params().N, 1)

func verifySignature(msgHash []byte, sig *Signature, expectedAddr []byte) error {
	if sig == nil {
		return fmt.Errorf("missing signature")
	}
	switch sig.Scheme {
	case SignatureSchemeSecp256k1:
		if len(sig.Signature) != 65 {
			return fmt.Errorf("invalid secp256k1 signature length")
		}
		// The signer is recovered from (r, s, v), and (r, n-s, v^1) recovers the same
		// one, so every message would have two valid encodings. Signing
		// (ethcrypto.Sign) always yields the low s, so this refuses nothing an
		// honest validator sends, and it makes the encoding of a signature unique.
		if new(big.Int).SetBytes(sig.Signature[32:64]).Cmp(secp256k1HalfN) > 0 {
			return fmt.Errorf("secp256k1 signature is not in canonical (low-s) form")
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

// MinBlockInterval returns the minimum block interval in force (WithMinBlockInterval,
// after it has been lowered to half the commit timeout if it was longer than that).
func (e *Engine) MinBlockInterval() time.Duration {
	if e == nil {
		return 0
	}
	return e.minBlockInterval
}

// Status returns the engine's current height, round and locked round (-1 when
// unlocked). It is a read-only view for liveness reporting: the watchdog puts
// it in its diagnostics so a stall can be told apart from a peer that is down
// or a lock that cannot be satisfied.
func (e *Engine) Status() (height uint64, round int, lockedRound int) {
	if e == nil {
		return 0, 0, -1
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.currentState.Height, e.currentState.Round, e.lockedRound
}
