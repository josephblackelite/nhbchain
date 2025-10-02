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

	proposalCh chan *SignedProposal
	voteCh     chan *SignedVote

	proposalTimeout  time.Duration
	prevoteTimeout   time.Duration
	precommitTimeout time.Duration
	commitTimeout    time.Duration

	prevoteSent   bool
	precommitSent bool
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
		proposalCh:       make(chan *SignedProposal, 16),
		voteCh:           make(chan *SignedVote, 128),
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

	return engine
}

func (e *Engine) Start() {
	fmt.Println("BFT Consensus Engine Started.")
	time.Sleep(5 * time.Second)
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
		case sp := <-e.proposalCh:
			if sp == nil || sp.Proposal == nil || sp.Proposal.Block == nil || sp.Proposal.Block.Header == nil {
				continue
			}
			if sp.Proposal.Block.Header.Height != height || sp.Proposal.Round != round {
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
		return nil
	}

	select {
	case e.voteCh <- v:
		return nil
	default:
		return fmt.Errorf("vote queue full")
	}
}

func (e *Engine) propose() error {
	txs := e.node.GetMempool()
	if len(txs) == 0 {
		fmt.Println("PROPOSE: Mempool empty, creating empty block proposal.")
	}

	var block *types.Block
	var err error
	if len(txs) == 0 {
		block, err = e.node.CreateBlock(nil)
	} else {
		block, err = e.node.CreateBlock(txs)
	}
	if err != nil {
		return fmt.Errorf("failed to build block: %w", err)
	}
	if block == nil || block.Header == nil {
		return fmt.Errorf("proposed block missing header")
	}

	e.mu.RLock()
	round := e.currentState.Round
	e.mu.RUnlock()

	proposal := &Proposal{Block: block, Round: round}
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
	fmt.Printf("COMMIT: Attempting to commit block %d.\n", block.Header.Height)
	if err := e.node.CommitBlock(block); err != nil {
		fmt.Printf("failed to commit block: %v\n", err)
		e.broadcastPrevoteNilLocked(err) // assumes lock is held
		e.resetProposalStateLocked()     // reset for next round
		return false
	}
	fmt.Printf("COMMIT: Successfully committed block %d.\n", block.Header.Height)

	e.committedBlocks[e.currentState.Height] = true
	e.currentState.Height++
	e.currentState.Round = 0
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.validatorSet = e.node.GetValidatorSet()
	e.recalculateVotingPowerLocked()
	e.syncHeightWithNodeLocked()
	return true
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
func (e *Engine) broadcastPrevoteNilLocked(execErr error) {
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
	fmt.Printf("PREVOTE NIL: Broadcasting nil vote due to execution failure: %v\n", execErr)
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
	e.mu.Lock()
	defer e.mu.Unlock()

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
	e.activeProposal = nil
	e.prevoteSent = false
	e.precommitSent = false
	e.validatorSet = e.node.GetValidatorSet()
	e.recalculateVotingPowerLocked()
	e.resetVoteTrackingLocked()
	fmt.Printf("\n--- Starting BFT round for Height: %d, Round: %d ---\n", e.currentState.Height, e.currentState.Round)
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
		return true
	}
	return false
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
		engagement := new(big.Int).SetUint64(account.EngagementScore)
		power := new(big.Int).Add(stake, engagement)

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
			fmt.Printf("Deterministic proposer selection: %s (Power: %s)\n", crypto.NewAddress(crypto.NHBPrefix, addrBytes).String(), weight.String())
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
	if !bytes.Equal(p.Proposer, p.Proposal.Block.Header.Validator) {
		return fmt.Errorf("proposal proposer mismatch")
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
