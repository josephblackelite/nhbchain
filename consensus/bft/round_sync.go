package bft

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"time"

	"nhbchain/core/txroot"
	"nhbchain/core/types"
	"nhbchain/p2p"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// Round synchronisation.
//
// Two validators (or more) stay in step only if each one knows which round the
// others are in. This engine used to learn it from messages for rounds it had
// not reached yet, which it buffered without limit and which, at the next round
// start, moved it to the LOWEST buffered round -- one message from one validator
// for round 1,000,000 was enough to strand it there, and after a restart it
// walked the rounds its peer had already left, one per round timeout, never
// catching up (a stall seen with two validators: one was restarted while the other
// kept running, and block production stopped for over a minute). The rules below
// replace that:
//
//   - a message for a future round is only kept when it is within maxFutureRounds
//     of the round this validator is in, and the amount kept is bounded per
//     validator and in total (bufferProposalLocked, bufferVoteLocked);
//   - what a message says about where its sender IS is remembered separately, as
//     one round number per validator (roundClaim), so it costs nothing to keep
//     however far ahead the message was;
//   - this validator moves to a higher round only on the evidence of those
//     round numbers (supportedRoundLocked): never on one message, never to the
//     lowest round anyone mentioned, and at once instead of at the end of the
//     round it is in.
//
// Nothing here changes what a valid message is, what is signed, or which blocks
// commit: every message keeps its encoding, and the vote and lock rules
// (sign_state.go, lockCompliesLocked) are untouched. The lock is still set by this
// validator's own tally of a round and by nothing it is told (addVoteIfRelevant).

const (
	// maxFutureRounds is how many rounds ahead of the round this validator is in
	// a vote or proposal is still kept for replay.
	// A round that does not commit lasts commitTimeout (4 s by default; the 2 s
	// proposal, prevote and precommit steps all run inside it), so 8 rounds is
	// about 32 s: longer than any healthy delay in this network -- a proposal is
	// due within 2 s and a whole round ends after 4 s -- and short enough that
	// what is kept, and what a single peer can make this validator hold, stays
	// small. A peer further ahead than that is stalled or was restarted; its
	// position is still recorded (roundClaim) and can move this validator, but
	// the message itself is dropped, never buffered.
	maxFutureRounds = 8

	// maxBufferedProposalsPerValidator is how many future-round proposals are
	// kept per validator and height; a further one replaces the oldest. Only
	// the round's proposer can have its proposal kept (see handleFutureProposal),
	// and a proposer that is more than a round or two ahead is followed by the
	// round jump, which discards everything below the round it jumps to, so two
	// is all a validator ever needs replayed.
	maxBufferedProposalsPerValidator = 2

	// maxBufferedProposals is how many future-round proposals are kept per
	// height in all, one for each round of the window. A proposal can be as
	// large as the transport's message limit (1 MiB by default), so this count
	// is what bounds the memory: about 8 MiB however many validators or rounds
	// are sending, since what is kept for a height is forgotten when the height
	// moves on (purgeBufferedBelowLocked).
	maxBufferedProposals = maxFutureRounds

	// maxBufferedVotesPerValidator is how many future-round votes are kept per
	// validator and height: one prevote and one precommit for each round of the
	// window, which is all an honest validator sends for those rounds. A vote is
	// a few hundred bytes, so the bound is on memory only in principle.
	maxBufferedVotesPerValidator = 2 * maxFutureRounds

	// dropLogInterval is the shortest time between two warnings about dropped
	// messages of one kind. One warning per interval is enough to notice a peer
	// that is out of step or misbehaving without one log line per message (a
	// peer sending a message a second would otherwise log every second); the
	// warning says how many messages it stands for.
	dropLogInterval = 10 * time.Second
)

// roundClaim is the highest round in which a validator has been seen to send a
// message at one height. It is only ever taken from a message whose signature
// checked out and whose signer is in the validator set, so a claim is a fact
// about what that validator signed -- though not a fact about which round it is
// in NOW (it may since have moved on, or restarted), which is why one claim is
// never enough to move (see supportedRoundLocked).
type roundClaim struct {
	height uint64
	round  int
}

// Reasons a message is dropped rather than kept, for the rate-limited warning.
const (
	dropTooFarAhead   = "too far ahead"
	dropNotProposer   = "signer is not the round's proposer"
	dropReplayedNotOK = "not the round's proposer at replay"
)

// dropNote is the state of the rate limiter for one drop reason.
type dropNote struct {
	at      time.Time
	dropped int
}

// noteDropLocked counts a dropped message and logs a warning for it, at most once
// per dropLogInterval for each reason. Must be called with e.mu held.
func (e *Engine) noteDropLocked(reason, what string, signer []byte, height uint64, round int) {
	if e.dropNotes == nil {
		e.dropNotes = make(map[string]*dropNote)
	}
	note := e.dropNotes[reason]
	if note == nil {
		note = &dropNote{}
		e.dropNotes[reason] = note
	}
	note.dropped++
	now := time.Now()
	if !note.at.IsZero() && now.Sub(note.at) < dropLogInterval {
		return
	}
	slog.Warn("BFT: dropped a consensus message",
		slog.String("event", "consensus_message_dropped"),
		slog.String("reason", reason),
		slog.String("message", what),
		slog.String("signer", fmt.Sprintf("%x", signer)),
		slog.Uint64("height", height),
		slog.Int("round", round),
		slog.Int("current_round", e.currentState.Round),
		slog.Int("window", maxFutureRounds),
		slog.Int("dropped_since_last_warning", note.dropped))
	note.at = now
	note.dropped = 0
}

// ownPowerLocked returns this validator's own voting power (zero when it is not
// in the validator set). Must be called with e.mu held.
func (e *Engine) ownPowerLocked() *big.Int {
	if len(e.selfAddr) == 0 {
		return new(big.Int)
	}
	if w := e.validatorSet[string(e.selfAddr)]; w != nil && w.Sign() > 0 {
		return w
	}
	return new(big.Int)
}

// recordRoundClaimLocked notes that signer sent a message for round at height.
// Must be called with e.mu held. This validator's own messages are not recorded:
// its own round is currentState.Round.
func (e *Engine) recordRoundClaimLocked(signer []byte, height uint64, round int) {
	if len(signer) == 0 || bytes.Equal(signer, e.selfAddr) {
		return
	}
	if e.roundClaims == nil {
		e.roundClaims = make(map[string]roundClaim)
	}
	key := string(signer)
	if have, ok := e.roundClaims[key]; !ok || have.height != height || round > have.round {
		e.roundClaims[key] = roundClaim{height: height, round: round}
	}
}

// moreThanOneThird reports whether power is strictly more than a third of total.
func moreThanOneThird(power, total *big.Int) bool {
	if power == nil || total == nil || total.Sign() <= 0 {
		return false
	}
	return new(big.Int).Mul(power, big.NewInt(3)).Cmp(total) > 0
}

// supportedRoundLocked returns the highest round above this validator's own that
// enough of the voting power has been seen in, and false when there is none.
// Must be called with e.mu held.
//
// "Seen in round R" means: a message signed by that validator for a round of R or
// later at this height (roundClaim). R is supported when the validators seen in
// it hold
//
//  1. strictly more than a third of the voting power -- so that at least one of
//     them is honest while less than a third is faulty, which is what makes it
//     safe to follow them (a validator, or a coalition below a third, cannot move
//     anyone by claiming any round it likes; the claim of a validator above
//     round R only counts towards R and below, never beyond it), and
//  2. together with this validator's own power, a quorum (types.HasQuorum,
//     strictly more than two thirds) -- so that the round can actually reach a
//     decision with this validator in it, which is the only reason to move to
//     it. A polka or a commit for a round is more than two thirds of the power
//     signing in that round, so it satisfies both conditions by itself.
//
// This validator's own power is counted in 2 because it is the one about to move:
// counting only the others would make it impossible to rejoin a network whose
// other validators cannot make a quorum without it -- every network of two
// validators, and any larger one that has lost a validator to a restart -- so
// the validator that restarted could never leave the rounds it is behind in.
// The result is the highest round for which both hold, so a validator that
// claims a round far above everyone else's does not raise it.
func (e *Engine) supportedRoundLocked() (int, bool) {
	total := e.totalVotingPower
	if total == nil || total.Sign() <= 0 || len(e.roundClaims) == 0 {
		return 0, false
	}
	type seen struct {
		key   string
		round int
		power *big.Int
	}
	var claims []seen
	for key, claim := range e.roundClaims {
		if claim.height != e.currentState.Height || claim.round <= e.currentState.Round {
			continue
		}
		if key == string(e.selfAddr) {
			continue
		}
		weight := e.validatorSet[key]
		if weight == nil || weight.Sign() <= 0 {
			continue
		}
		claims = append(claims, seen{key: key, round: claim.round, power: weight})
	}
	if len(claims) == 0 {
		return 0, false
	}
	// Highest round first; the address only makes the order total.
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].round != claims[j].round {
			return claims[i].round > claims[j].round
		}
		return claims[i].key < claims[j].key
	})
	own := e.ownPowerLocked()
	power := new(big.Int)
	for _, c := range claims {
		power.Add(power, c.power)
		if moreThanOneThird(power, total) && types.HasQuorum(new(big.Int).Add(power, own), total) {
			return c.round, true
		}
	}
	return 0, false
}

// admission is what to do with a message for a round after the current one.
type admission int

const (
	admitDrop   admission = iota // beyond the window, or for another height: do not keep it
	admitBuffer                  // keep it until this validator reaches its round
	admitNow                     // the round has become the current one: deliver it now
)

// admitFutureRoundLocked decides what to do with a verified message from signer
// for round at height, when the round was after this validator's current one.
// It records where the signer is, wakes the round loop when that moves this
// validator (see supportedRoundLocked), and applies the window: the round must be
// within maxFutureRounds of the round this validator is in. A message beyond it is
// dropped even when it is what moves this validator there: the position of its
// signer is kept (roundClaim), the message is not. Must be called with e.mu held.
func (e *Engine) admitFutureRoundLocked(signer []byte, what string, height uint64, round int) admission {
	if height != e.currentState.Height {
		return admitDrop
	}
	if round < e.currentState.Round {
		return admitDrop
	}
	if round == e.currentState.Round {
		return admitNow
	}
	e.recordRoundClaimLocked(signer, height, round)
	if target, ok := e.supportedRoundLocked(); ok && target > e.currentState.Round {
		// Do not wait for the end of the round: the validators this one needs
		// are somewhere else. The round loop returns and startNewRound moves.
		select {
		case e.roundSkipCh <- struct{}{}:
		default:
		}
	}
	if round-e.currentState.Round > maxFutureRounds {
		e.noteDropLocked(dropTooFarAhead, what, signer, height, round)
		return admitDrop
	}
	return admitBuffer
}

// sameProposal reports whether a and b are the same message: the same signer and
// the same signature (a signature is deterministic, so a proposal sent twice
// carries the same one).
func sameProposal(a, b *SignedProposal) bool {
	if a == nil || b == nil || a.Signature == nil || b.Signature == nil {
		return a == b
	}
	return bytes.Equal(a.Proposer, b.Proposer) && bytes.Equal(a.Signature.Signature, b.Signature.Signature)
}

// evictOldestProposalLocked drops the oldest buffered proposal at height -- the
// one for the lowest round, the first received among those -- from signer, or
// from anyone when signer is nil, and reports whether there was one. Must be
// called with e.mu held.
func (e *Engine) evictOldestProposalLocked(height uint64, signer []byte) bool {
	rounds := e.bufferedProposal[height]
	lowest, found := 0, false
	for round, proposals := range rounds {
		for _, p := range proposals {
			if signer != nil && !bytes.Equal(p.Proposer, signer) {
				continue
			}
			if !found || round < lowest {
				lowest, found = round, true
			}
			break
		}
	}
	if !found {
		return false
	}
	proposals := rounds[lowest]
	for i, p := range proposals {
		if signer != nil && !bytes.Equal(p.Proposer, signer) {
			continue
		}
		rounds[lowest] = append(proposals[:i:i], proposals[i+1:]...)
		break
	}
	if len(rounds[lowest]) == 0 {
		delete(rounds, lowest)
	}
	if len(rounds) == 0 {
		delete(e.bufferedProposal, height)
	}
	return true
}

func (e *Engine) countBufferedProposalsLocked(height uint64, signer []byte) int {
	count := 0
	for _, proposals := range e.bufferedProposal[height] {
		for _, p := range proposals {
			if signer == nil || bytes.Equal(p.Proposer, signer) {
				count++
			}
		}
	}
	return count
}

// bufferProposalLocked keeps a future-round proposal for replay. An identical
// proposal already kept is not kept twice; a validator that would exceed
// maxBufferedProposalsPerValidator, or the buffer maxBufferedProposals in all,
// loses its oldest proposal instead. Must be called with e.mu held.
func (e *Engine) bufferProposalLocked(p *SignedProposal) {
	if p == nil || p.Proposal == nil || p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return
	}
	height := p.Proposal.Block.Header.Height
	round := p.Proposal.Round
	for _, have := range e.bufferedProposal[height][round] {
		if sameProposal(have, p) {
			return
		}
	}
	for e.countBufferedProposalsLocked(height, p.Proposer) >= maxBufferedProposalsPerValidator {
		if !e.evictOldestProposalLocked(height, p.Proposer) {
			break
		}
	}
	for e.countBufferedProposalsLocked(height, nil) >= maxBufferedProposals {
		if !e.evictOldestProposalLocked(height, nil) {
			break
		}
	}
	if _, ok := e.bufferedProposal[height]; !ok {
		e.bufferedProposal[height] = make(map[int][]*SignedProposal)
	}
	e.bufferedProposal[height][round] = append(e.bufferedProposal[height][round], p)
}

// evictOldestVoteLocked drops the oldest buffered vote at height from validator
// (the one for the lowest round) and reports whether there was one. Must be
// called with e.mu held.
func (e *Engine) evictOldestVoteLocked(height uint64, validator []byte) bool {
	rounds := e.bufferedVotes[height]
	lowest, found := 0, false
	for round, votes := range rounds {
		for _, v := range votes {
			if bytes.Equal(v.Validator, validator) {
				if !found || round < lowest {
					lowest, found = round, true
				}
				break
			}
		}
	}
	if !found {
		return false
	}
	votes := rounds[lowest]
	for i, v := range votes {
		if bytes.Equal(v.Validator, validator) {
			rounds[lowest] = append(votes[:i:i], votes[i+1:]...)
			break
		}
	}
	if len(rounds[lowest]) == 0 {
		delete(rounds, lowest)
	}
	if len(rounds) == 0 {
		delete(e.bufferedVotes, height)
	}
	return true
}

func (e *Engine) countBufferedVotesLocked(height uint64, validator []byte) int {
	count := 0
	for _, votes := range e.bufferedVotes[height] {
		for _, v := range votes {
			if bytes.Equal(v.Validator, validator) {
				count++
			}
		}
	}
	return count
}

// bufferVoteLocked keeps a future-round vote for replay. A validator has one
// prevote and one precommit per round that count -- the engine only ever tallies
// the first of each -- so a second vote of the same type for the same round is
// not kept, whether identical or not; a validator that would exceed
// maxBufferedVotesPerValidator loses its oldest vote instead. Must be called with
// e.mu held.
func (e *Engine) bufferVoteLocked(v *SignedVote) {
	if v == nil || v.Vote == nil {
		return
	}
	height := v.Vote.Height
	round := v.Vote.Round
	for _, have := range e.bufferedVotes[height][round] {
		if have.Vote.Type == v.Vote.Type && bytes.Equal(have.Validator, v.Validator) {
			return
		}
	}
	for e.countBufferedVotesLocked(height, v.Validator) >= maxBufferedVotesPerValidator {
		if !e.evictOldestVoteLocked(height, v.Validator) {
			break
		}
	}
	if _, ok := e.bufferedVotes[height]; !ok {
		e.bufferedVotes[height] = make(map[int][]*SignedVote)
	}
	e.bufferedVotes[height][round] = append(e.bufferedVotes[height][round], v)
}

// stashEarlyVoteLocked keeps a vote for the current round that arrived before this
// validator had a proposal to count it for, until applyEarlyVotes. One vote is
// kept per validator and vote type -- the engine only tallies the first of each --
// so at most two for each validator in the set are ever held. Must be called with
// e.mu held.
func (e *Engine) stashEarlyVoteLocked(v *SignedVote) {
	if v.Vote.Height != e.currentState.Height || v.Vote.Round != e.currentState.Round {
		return
	}
	if v.Vote.Type != Prevote && v.Vote.Type != Precommit {
		return
	}
	if _, ok := e.validatorSet[string(v.Validator)]; !ok {
		return
	}
	key := string(v.Validator) + string([]byte{byte(v.Vote.Type)})
	if _, have := e.earlyVotes[key]; have {
		return
	}
	if e.earlyVotes == nil {
		e.earlyVotes = make(map[string]*SignedVote)
	}
	e.earlyVotes[key] = v
}

// applyEarlyVotes counts the votes stashed before the proposal that has just been
// accepted, the way the round loop counts a vote it reads (a quorum of prevotes
// makes this validator precommit, a quorum of precommits commits), and reports
// whether the block was committed.
func (e *Engine) applyEarlyVotes() bool {
	e.mu.Lock()
	votes := make([]*SignedVote, 0, len(e.earlyVotes))
	for _, v := range e.earlyVotes {
		votes = append(votes, v)
	}
	e.earlyVotes = nil
	e.mu.Unlock()
	// Prevotes before precommits, so that a quorum of prevotes is seen before the
	// precommits that follow it; the addresses only make the order total.
	sort.Slice(votes, func(i, j int) bool {
		if votes[i].Vote.Type != votes[j].Vote.Type {
			return votes[i].Vote.Type < votes[j].Vote.Type
		}
		return bytes.Compare(votes[i].Validator, votes[j].Validator) < 0
	})
	for _, v := range votes {
		added, reachedPrevote, reachedPrecommit := e.addVoteIfRelevant(v)
		if added {
			fmt.Printf("Received %s vote for block %x from %x\n", v.Vote.Type, v.Vote.BlockHash, v.Validator)
		}
		if reachedPrevote {
			e.precommit()
		}
		if reachedPrecommit && e.commit() {
			return true
		}
	}
	return false
}

// purgeBufferedBelowLocked forgets every buffered message for a round before
// round at height, and every one for an earlier height: this validator has passed
// them (or jumped over them, or committed the height) and they can no longer be
// replayed. Without the heights, what one peer sent for the rounds this validator
// never reached would stay for as long as the process runs, at every height it
// was sent at. Must be called with e.mu held.
func (e *Engine) purgeBufferedBelowLocked(height uint64, round int) {
	for h := range e.bufferedProposal {
		if h < height {
			delete(e.bufferedProposal, h)
		}
	}
	for h := range e.bufferedVotes {
		if h < height {
			delete(e.bufferedVotes, h)
		}
	}
	if rounds := e.bufferedProposal[height]; rounds != nil {
		for r := range rounds {
			if r < round {
				delete(rounds, r)
			}
		}
		if len(rounds) == 0 {
			delete(e.bufferedProposal, height)
		}
	}
	if rounds := e.bufferedVotes[height]; rounds != nil {
		for r := range rounds {
			if r < round {
				delete(rounds, r)
			}
		}
		if len(rounds) == 0 {
			delete(e.bufferedVotes, height)
		}
	}
}

// proposerKey is what a proposer is chosen for. The choice depends on the chain
// state at the height (the hash of the last block, the stakes) as well as on the
// round, so the proposer of a round is not the proposer of the same round at
// another height.
type proposerKey struct {
	height uint64
	round  int
}

// resetProposerCacheLocked forgets the proposers worked out so far, and makes any
// that is being worked out at this moment (proposerFor) unstorable. Must be called
// with e.mu held whenever what the choice is made from may have changed: the
// height moves on (the chain has a new last block), or the validator set is
// replaced.
func (e *Engine) resetProposerCacheLocked() {
	e.proposerCache = nil
	e.proposerEpoch++
}

// proposerFor returns the proposer of round at height -- what selectProposer
// chooses -- or nil when it cannot be told (another height than the engine's, or
// the accounts it is chosen from cannot be read). The choice depends only on the
// chain state of the height, so it is worked out once for a height and round and
// kept, instead of once per message, until resetProposerCacheLocked forgets it:
// when a round starts, once the validator set the choice is made from has been
// refreshed, and whenever the height moves on. It is kept by height as well as by
// round, so that the proposer of a round at one height is never taken for the
// proposer of the same round at another.
func (e *Engine) proposerFor(height uint64, round int) []byte {
	key := proposerKey{height: height, round: round}
	e.mu.RLock()
	if height != e.currentState.Height {
		e.mu.RUnlock()
		return nil
	}
	if proposer, ok := e.proposerCache[key]; ok {
		e.mu.RUnlock()
		return proposer
	}
	validators := e.validatorSet
	epoch := e.proposerEpoch
	e.mu.RUnlock()

	// Chosen without e.mu held: it reads accounts from the node.
	proposer := e.selectProposerIn(validators, round)
	if len(proposer) == 0 {
		return nil
	}
	e.mu.Lock()
	if e.proposerEpoch == epoch && e.currentState.Height == height {
		if e.proposerCache == nil {
			e.proposerCache = make(map[proposerKey][]byte)
		}
		e.proposerCache[key] = proposer
	}
	e.mu.Unlock()
	return proposer
}

// handleCurrentProposal queues a proposal for the round this validator is in. A
// proposal from a validator that is not the round's proposer is dropped here, so
// it can never take a place in the queue ahead of the real one (the round loop
// ignores such a proposal too, but only after it has queued and dequeued it).
func (e *Engine) handleCurrentProposal(p *SignedProposal, height uint64, round int) error {
	if proposer := e.proposerFor(height, round); len(proposer) > 0 && !bytes.Equal(p.Proposer, proposer) {
		e.mu.Lock()
		e.noteDropLocked(dropNotProposer, "proposal", p.Proposer, height, round)
		e.mu.Unlock()
		return nil
	}
	return e.enqueueProposal(p, height, round)
}

// handleFutureProposal keeps a proposal for a round after the current one, when
// it may be kept: it must come from that round's proposer and be within the
// window. Where its signer is is recorded whatever happens to the proposal.
func (e *Engine) handleFutureProposal(p *SignedProposal, height uint64) {
	round := p.Proposal.Round
	e.mu.Lock()
	decision := e.admitFutureRoundLocked(p.Proposer, "proposal", height, round)
	e.mu.Unlock()
	if decision == admitDrop {
		return
	}
	if decision == admitBuffer {
		if proposer := e.proposerFor(height, round); len(proposer) > 0 && !bytes.Equal(p.Proposer, proposer) {
			e.mu.Lock()
			e.noteDropLocked(dropNotProposer, "proposal", p.Proposer, height, round)
			e.mu.Unlock()
			return
		}
		e.mu.Lock()
		// The round may have been reached while the proposer was being worked out.
		if height == e.currentState.Height && round > e.currentState.Round {
			e.bufferProposalLocked(p)
			e.mu.Unlock()
			return
		}
		current, curHeight := e.currentState.Round, e.currentState.Height
		e.mu.Unlock()
		if height != curHeight || round != current {
			return
		}
	}
	_ = e.handleCurrentProposal(p, height, round)
}

// enqueueProposal puts a proposal for the current round on the queue the round
// loop reads. Only the round's proposer's proposals get this far, so the queue is
// not full of anyone else's; when it is full all the same, the entries the round
// loop would ignore (another height or round) are cleared out rather than the
// new proposal refused.
func (e *Engine) enqueueProposal(p *SignedProposal, height uint64, round int) error {
	e.queueMu.Lock()
	defer e.queueMu.Unlock()
	select {
	case e.proposalCh <- p:
		return nil
	default:
	}
	e.dropUnusableQueuedProposalsLocked(height, round)
	select {
	case e.proposalCh <- p:
		return nil
	default:
		return fmt.Errorf("proposal queue full")
	}
}

// dropUnusableQueuedProposalsLocked empties the proposal queue of every proposal
// that is not for (height, round) -- the round loop skips those -- and puts the
// rest back in their order. Must be called with e.queueMu held.
func (e *Engine) dropUnusableQueuedProposalsLocked(height uint64, round int) {
	var keep []*SignedProposal
drain:
	for {
		select {
		case q := <-e.proposalCh:
			if q != nil && q.Proposal != nil && q.Proposal.Block != nil && q.Proposal.Block.Header != nil &&
				q.Proposal.Block.Header.Height == height && q.Proposal.Round == round {
				keep = append(keep, q)
			}
		default:
			break drain
		}
	}
	for _, q := range keep {
		select {
		case e.proposalCh <- q:
		default:
		}
	}
}

// replayBufferedMessages hands the messages kept for (height, round) to the round
// loop, now that this validator has reached it, and forgets the ones for earlier
// rounds. A kept proposal from a validator that is not the round's proposer is
// dropped rather than replayed. The proposals are queued the way one that has just
// arrived is (enqueueProposal), and are few -- the proposer's alone, at most
// maxBufferedProposalsPerValidator -- so the real proposer's proposal cannot be
// pushed out of the queue by anyone else's.
func (e *Engine) replayBufferedMessages(height uint64, round int) {
	proposer := e.proposerFor(height, round)

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
	e.purgeBufferedBelowLocked(height, round)
	e.mu.Unlock()

	e.queueMu.Lock()
	e.dropUnusableQueuedProposalsLocked(height, round)
	e.queueMu.Unlock()

	for _, proposal := range proposals {
		if len(proposer) > 0 && !bytes.Equal(proposal.Proposer, proposer) {
			e.mu.Lock()
			e.noteDropLocked(dropReplayedNotOK, "proposal", proposal.Proposer, height, round)
			e.mu.Unlock()
			continue
		}
		if err := e.enqueueProposal(proposal, height, round); err != nil {
			fmt.Printf("dropping buffered proposal for height %d round %d: %v\n", height, round, err)
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

// Passing on a polka.
//
// A validator that holds a polka -- more than two thirds of the voting power
// prevoted one block in one round, and it has those signed prevotes -- can prove it
// to anyone, but until now it said so only when it was the proposer (a re-proposal
// carries the proof). So two validators that ended up with different views of a
// round -- one saw the polka and locked, the other's vote arrived a round late or
// it was restarted and knows nothing of it -- waited for the one that holds the
// polka to have a proposer turn, while the other, unlocked, went on proposing new
// blocks that the first refuses because of its lock. That took eleven rounds in
// the stall above, and with proposer turns weighted by stake it can take as many
// rounds as the locked validator's turns are rare.
//
// The validator that holds the polka now says so when it is refusing something
// because of it, and when it hears from a validator that is behind (a message for a
// round it has passed): it sends the proposal of its own current round that a
// proposer would -- the block, the round of the polka and the signed prevotes that
// prove it -- signed as a proposal. Nobody treats that as the round's proposal
// (only the round's proposer's is); a receiver that verifies the proof only
// LEARNS from it: it remembers the block and the polka as its own valid block, so
// that when it is the proposer it re-proposes that block, which the locked
// validator accepts (lockCompliesLocked), and, because the message names the
// sender's round, it learns where the sender is (roundClaim) and moves there.
// Neither the lock nor the vote rules are touched, no vote is signed, and the
// message is an ordinary proposal message, which an engine that does not know this
// ignores like any proposal from a validator that is not the round's proposer.
//
// What is learnt is checked first, because anyone in the validator set can send
// it: the proof says that more than two thirds of the power prevoted the header,
// and says nothing of the transactions, which the header commits to only by their
// root. A body that is not the one that root commits to is refused
// (blockBodyMatchesHeader): otherwise a validator that knew of a real polka could
// hand out its header with a body of its own, and have it re-proposed, and passed
// on, as the valid block.

// polkaLearnable reports whether the polka of round polkaRound, cited by a proposal
// for height, is one to learn from when this validator is in state and holds the
// polka of round held. It has to be later than the one held and of a round this
// validator has already passed. For the round it is in, or a later one, its own
// tally may still reach the polka, and a record of that round made from a message
// would stand in for the lock (see addVoteIfRelevant), which only the tally may
// set; and a polka of a round that is not before this validator's own could not be
// the ValidRound of a proposal of that round (propose). Nothing is lost by it: the
// validator that holds a polka says so again in each round in which it hears from
// one that is behind, and a validator that has moved on learns it then.
func polkaLearnable(height uint64, polkaRound int, state State, held int) bool {
	return height == state.Height && polkaRound > held && polkaRound < state.Round
}

// blockBodyMatchesHeader reports whether the transactions of b are the ones its
// header commits to (by their root, which is all of them that the header's hash
// covers). It reads nothing but b: a full validation would also execute the block
// against the node's state, which is not something to do for every message that
// carries a polka.
func blockBodyMatchesHeader(b *types.Block) bool {
	if b == nil || b.Header == nil {
		return false
	}
	root, err := txroot.Compute(b.Transactions)
	return err == nil && bytes.Equal(root, b.Header.TxRoot)
}

// What a validator keeps of a polka it is told of, and passes on, is what the proof
// and the root vouch for and no more: whatever else a message carries is not covered
// by any signature, and this validator would carry it into the proposal it makes of
// the block, and the messages it sends of the polka, which a peer refuses -- and ends
// its connection over -- when they are larger than the transport allows.

// plainBlock is b without its quorum certificate. A block is proven by its header
// (the hash the polka is of) and its transactions (the root in the header); the
// certificate is neither, and commit makes it again from the precommits of the round.
func plainBlock(b *types.Block) *types.Block {
	return types.NewBlock(b.Header, b.Transactions)
}

// plainVotes returns copies of votes that hold what each vote's signature covers,
// the validator, and the signature itself, and the public key only for a scheme that
// verifies with the one in the message: the rest of a signed vote is not signed, and
// is room to pad it. A vote that is not a whole signed vote is left out.
func plainVotes(votes []*SignedVote) []*SignedVote {
	plain := make([]*SignedVote, 0, len(votes))
	for _, sv := range votes {
		if sv == nil || sv.Vote == nil || sv.Signature == nil {
			continue
		}
		signature := &Signature{Scheme: sv.Signature.Scheme, Signature: append([]byte(nil), sv.Signature.Signature...)}
		if sv.Signature.Scheme == SignatureSchemeEd25519 {
			signature.PublicKey = append([]byte(nil), sv.Signature.PublicKey...)
		}
		plain = append(plain, &SignedVote{
			Vote:      &Vote{BlockHash: append([]byte(nil), sv.Vote.BlockHash...), Round: sv.Vote.Round, Type: sv.Vote.Type, Height: sv.Vote.Height},
			Validator: append([]byte(nil), sv.Validator...),
			Signature: signature,
		})
	}
	return plain
}

// learnValidBlock remembers the block and the polka of a proposal that carries a
// proof of one, when the proof verifies, the block is the one the proof is for
// (header and transactions), and the polka is later than the polka this validator
// already holds and of a round it has already passed (polkaLearnable). It is what
// turns a re-proposal, or the message of a validator that passes on its polka
// (relayValidBlock), into the valid block this validator re-proposes when its own
// turn comes. It does not touch the lock.
//
// It is called for every proposal of every validator, on the network's goroutines,
// so what it costs -- a signature check for each vote of the proof, and the root of
// the transactions -- is paid with no lock held, on a copy of what the checks need
// that is taken at the start; the write lock, which the round loop and every other
// message wait for, is taken once, at the end, to record the result.
func (e *Engine) learnValidBlock(p *SignedProposal) {
	if p == nil || p.Proposal == nil || p.Proposal.Block == nil || p.Proposal.Block.Header == nil {
		return
	}
	prop := p.Proposal
	if prop.ValidRound < 0 || prop.ValidRound >= prop.Round || len(prop.ValidRoundProof) == 0 {
		return
	}
	height := prop.Block.Header.Height

	e.mu.RLock()
	state, held := e.currentState, e.validRound
	validators := e.validatorSet
	total := new(big.Int)
	if e.totalVotingPower != nil {
		total.Set(e.totalVotingPower)
	}
	e.mu.RUnlock()
	if !polkaLearnable(height, prop.ValidRound, state, held) {
		return
	}
	// A proof holds one vote per validator at most; a longer one only costs the
	// signature checks.
	if len(prop.ValidRoundProof) > len(validators) {
		return
	}
	hash, err := prop.Block.Header.Hash()
	if err != nil {
		return
	}
	counted, power := countedPolkaVotes(prop.ValidRoundProof, prop.ValidRound, hash, height, validators)
	if !types.HasQuorum(power, total) {
		return
	}
	if !blockBodyMatchesHeader(prop.Block) {
		return
	}
	block, votes := plainBlock(prop.Block), plainVotes(counted)

	e.mu.Lock()
	// The height or the round may have moved on, or another message may have taught
	// a later polka, while the checks ran.
	if !polkaLearnable(height, prop.ValidRound, e.currentState, e.validRound) {
		e.mu.Unlock()
		return
	}
	e.validBlock = block
	e.validRound = prop.ValidRound
	e.polkaHistory[prop.ValidRound] = polkaRecord{
		block:     block,
		blockHash: append([]byte(nil), hash...),
		votes:     votes,
	}
	e.mu.Unlock()
	fmt.Printf("VALID BLOCK: learned block %x with a polka at round %d from %x\n", hash, prop.ValidRound, p.Proposer)
}

// buildValidBlockRelayLocked returns the message that passes this validator's polka
// on, or nil when it has none to pass on, or has passed it on already in this
// round (the message is at most one a round, however many messages ask for it).
// Must be called with e.mu held.
func (e *Engine) buildValidBlockRelayLocked() *p2p.Message {
	if e.broadcaster == nil || e.privKey == nil || e.validBlock == nil || e.validBlock.Header == nil {
		return nil
	}
	round, height := e.currentState.Round, e.currentState.Height
	if e.validRound < 0 || e.validRound >= round {
		return nil
	}
	record, ok := e.polkaHistory[e.validRound]
	if !ok || len(record.votes) == 0 {
		return nil
	}
	if e.relayed && e.relayedHeight == height && e.relayedRound == round {
		return nil
	}
	proof := plainVotes(record.votes)
	if len(proof) == 0 {
		return nil
	}
	proposal := &Proposal{Block: plainBlock(e.validBlock), Round: round, ValidRound: e.validRound, ValidRoundProof: proof}
	proposalHash := sha256.Sum256(proposal.bytes())
	sig, err := ethcrypto.Sign(proposalHash[:], e.privKey.PrivateKey)
	if err != nil {
		return nil
	}
	signed := &SignedProposal{
		Proposal:  proposal,
		Proposer:  e.selfAddr,
		Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig},
	}
	payload, err := json.Marshal(signed)
	if err != nil {
		return nil
	}
	e.relayed, e.relayedHeight, e.relayedRound = true, height, round
	return &p2p.Message{Type: p2p.MsgTypeProposal, Payload: payload}
}

// relayIfLockRefused passes the polka on when the prevote the round loop has just
// cast was a refusal because of the lock (prevote leaves that in lockRefused).
func (e *Engine) relayIfLockRefused() {
	e.mu.Lock()
	refused := e.lockRefused
	e.lockRefused = false
	e.mu.Unlock()
	if refused {
		e.relayValidBlock()
	}
}

// relayValidBlock passes this validator's polka on (see above) if it has one.
func (e *Engine) relayValidBlock() {
	if e == nil {
		return
	}
	e.mu.Lock()
	msg := e.buildValidBlockRelayLocked()
	round := e.currentState.Round
	e.mu.Unlock()
	if msg == nil {
		return
	}
	if err := e.broadcaster.Broadcast(msg); err != nil {
		fmt.Printf("failed to broadcast the polka of round %d: %v\n", round, err)
		return
	}
	fmt.Printf("VALID BLOCK: passing on the polka this validator holds (round %d)\n", round)
}
