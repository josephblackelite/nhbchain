package bft

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"sync"
	"time"

	// THE FIX IS HERE: Import the ethereum crypto library for signing functions
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

	currentState    State
	activeProposal  *Proposal
	receivedVotes   map[VoteType]map[string]*Vote
	committedBlocks map[uint64]bool
}

func NewEngine(node NodeInterface, key *crypto.PrivateKey, broadcaster p2p.Broadcaster) *Engine {
	return &Engine{
		node:            node,
		privKey:         key,
		validatorSet:    node.GetValidatorSet(),
		broadcaster:     broadcaster,
		currentState:    State{Height: 1, Round: 0},
		receivedVotes:   make(map[VoteType]map[string]*Vote),
		committedBlocks: make(map[uint64]bool),
	}
}

func (e *Engine) Start() {
	fmt.Println("BFT Consensus Engine Started.")
	time.Sleep(5 * time.Second)
	for {
		e.startNewRound()
		proposer := e.selectProposer(e.currentState.Round)
		myAddr := e.privKey.PubKey().Address().Bytes()
		if bytes.Equal(proposer, myAddr) {
			go e.propose()
		}
		<-time.After(2 * time.Second)
		go e.prevote()
		<-time.After(2 * time.Second)
		go e.precommit()
		<-time.After(2 * time.Second)
		go e.commit()
		<-time.After(4 * time.Second)
	}
}

func (e *Engine) HandleProposal(p *Proposal) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeProposal == nil {
		fmt.Printf("Received block proposal for height %d from %x\n", p.Block.Header.Height, p.Proposer)
		e.activeProposal = p
	}
	return nil
}

func (e *Engine) HandleVote(v *Vote) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.receivedVotes[v.Type]; !ok {
		e.receivedVotes[v.Type] = make(map[string]*Vote)
	}
	fmt.Printf("Received %s vote for block %x from %x\n", v.Type, v.BlockHash, v.Validator)
	e.receivedVotes[v.Type][string(v.Validator)] = v
	return nil
}

func (e *Engine) propose() {
	txs := e.node.GetMempool()
	if len(txs) == 0 {
		return
	}
	block := e.node.CreateBlock(txs)
	proposal := &Proposal{Block: block, Round: e.currentState.Round, Proposer: e.privKey.PubKey().Address().Bytes()}
	msgPayload, _ := json.Marshal(proposal)
	msg := &p2p.Message{Type: p2p.MsgTypeProposal, Payload: msgPayload}
	e.broadcaster.Broadcast(msg)
	fmt.Println("PROPOSE: Broadcasting our new block proposal.")
}

func (e *Engine) prevote() {
	e.mu.RLock()
	if e.activeProposal == nil {
		e.mu.RUnlock()
		return
	}
	blockHash, _ := e.activeProposal.Block.Header.Hash()
	e.mu.RUnlock()
	vote := e.createVote(Prevote, blockHash)
	msgPayload, _ := json.Marshal(vote)
	msg := &p2p.Message{Type: p2p.MsgTypeVote, Payload: msgPayload}
	e.broadcaster.Broadcast(msg)
	fmt.Println("PREVOTE: Broadcasting our prevote.")
}

func (e *Engine) precommit() {
	e.mu.RLock()
	if e.activeProposal == nil {
		e.mu.RUnlock()
		return
	}
	if len(e.receivedVotes[Prevote]) < e.twoThirdsMajority() {
		e.mu.RUnlock()
		return
	}
	blockHash, _ := e.activeProposal.Block.Header.Hash()
	e.mu.RUnlock()
	vote := e.createVote(Precommit, blockHash)
	msgPayload, _ := json.Marshal(vote)
	msg := &p2p.Message{Type: p2p.MsgTypeVote, Payload: msgPayload}
	e.broadcaster.Broadcast(msg)
	fmt.Println("PRECOMMIT: Broadcasting our precommit.")
}

func (e *Engine) commit() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeProposal == nil || e.committedBlocks[e.currentState.Height] {
		return
	}
	if len(e.receivedVotes[Precommit]) < e.twoThirdsMajority() {
		return
	}
	fmt.Printf("COMMIT: Committing block %d and adding to chain.\n", e.activeProposal.Block.Header.Height)
	e.node.CommitBlock(e.activeProposal.Block)
	e.committedBlocks[e.currentState.Height] = true
	e.currentState.Height++
	e.currentState.Round = 0
}

func (e *Engine) startNewRound() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.committedBlocks[e.currentState.Height] {
		e.currentState.Height++
		e.currentState.Round = 0
	} else {
		e.currentState.Round++
	}
	e.activeProposal = nil
	e.receivedVotes[Prevote] = make(map[string]*Vote)
	e.receivedVotes[Precommit] = make(map[string]*Vote)
	fmt.Printf("\n--- Starting BFT round for Height: %d, Round: %d ---\n", e.currentState.Height, e.currentState.Round)
}

func (e *Engine) selectProposer(round int) []byte {
	var validators [][]byte
	var totalPower = big.NewInt(0)
	weights := make(map[string]*big.Int)

	// Step 1: Calculate the "Power" (total weight) of each validator.
	for addrStr := range e.validatorSet {
		addrBytes := []byte(addrStr)
		account, err := e.node.GetAccount(addrBytes)
		if err != nil {
			continue // Skip validator if we can't get their account
		}

		// Power = Stake + EngagementScore
		// This balances capital investment with network activity.
		stake := account.Stake
		engagement := new(big.Int).SetUint64(account.EngagementScore)
		power := new(big.Int).Add(stake, engagement)

		weights[addrStr] = power
		totalPower.Add(totalPower, power)
		validators = append(validators, addrBytes)
	}

	if len(validators) == 0 {
		return nil
	}
	if totalPower.Cmp(big.NewInt(0)) == 0 {
		// Fallback to round-robin if no validator has any power
		return validators[round%len(validators)]
	}

	// Step 2: Pick a winning "ticket" based on the round number.
	// This is a deterministic pseudo-random selection.
	seed := new(big.Int).SetBytes(big.NewInt(int64(round)).Bytes())
	pick := new(big.Int).Mod(seed, totalPower)

	// Step 3: Find which validator holds the winning ticket.
	for _, addrBytes := range validators {
		addrStr := string(addrBytes)
		power := weights[addrStr]
		if pick.Cmp(power) < 0 {
			// This validator is the winner
			fmt.Printf("POTSO Proposer Selection: %s (Power: %s)\n", crypto.NewAddress(crypto.NHBPrefix, addrBytes).String(), power.String())
			return addrBytes
		}
		pick.Sub(pick, power)
	}

	// Should not be reached, but as a fallback:
	return validators[0]
}

func (e *Engine) createVote(t VoteType, blockHash []byte) *Vote {
	vote := &Vote{BlockHash: blockHash, Round: e.currentState.Round, Type: t, Validator: e.privKey.PubKey().Address().Bytes()}
	voteHash := sha256.Sum256(vote.bytes())
	// THE FIX IS HERE: Use ethcrypto.Sign to access the correct signing function.
	sig, _ := ethcrypto.Sign(voteHash[:], e.privKey.PrivateKey)
	vote.Signature = sig
	return vote
}

func (e *Engine) twoThirdsMajority() int { return (2*len(e.validatorSet))/3 + 1 }
func (v *Vote) bytes() []byte            { b, _ := json.Marshal(v); return b }
func (vt VoteType) String() string {
	if vt == Prevote {
		return "Prevote"
	}
	return "Precommit"
}
