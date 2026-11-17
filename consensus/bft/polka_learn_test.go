package bft

// Tests for what a validator accepts from a polka that someone else passes on
// (round_sync.go, learnValidBlock), and for the lock that the polka of a round it
// is in must still set.
//
// A validator remembers the block and the polka of a proposal that proves one, so
// that when it is the proposer it re-proposes that block. The proof is the signed
// prevotes of more than two thirds of the power, and they cover the block's header
// and nothing else: the transactions are covered only by the root in the header.
// So a validator that knew of a real polka could send its header with a body of its
// own, and every validator that took it for the valid block would fail its own
// proposer turns ("tx root mismatch") until the height ended, and pass it on to the
// rest. And a polka learnt for the round a validator is in was taken, by the record
// it left, for the lock that the validator's own tally sets for that round, so it
// precommitted a block without being locked on it. Each test below fails on the
// engine without the checks, but the last two, which pin what the checks rely on:
// that a vote still verifies once it is copied, and that the root of a block's
// transactions survives its trip over the wire.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/types"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// polkaOf returns the prevotes of every validator of tv for block at round: a polka.
func polkaOf(t *testing.T, tv *testValidators, block *types.Block, round int) []*SignedVote {
	t.Helper()
	hash := headerHash(t, block)
	var votes []*SignedVote
	for i := range tv.keys {
		votes = append(votes, signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hash, round, 1))
	}
	return votes
}

// polkaMessage is the proposal message that validator idx of tv signs to cite the
// polka proof of round validRound, for block, in a proposal of round.
func polkaMessage(t *testing.T, tv *testValidators, idx int, block *types.Block, round, validRound int, proof []*SignedVote) *SignedProposal {
	t.Helper()
	proposal := &Proposal{Block: block, Round: round, ValidRound: validRound, ValidRoundProof: proof}
	sig, err := ethSign(sha256Sum(proposal.bytes()), tv.keys[idx])
	if err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	return &SignedProposal{Proposal: proposal, Proposer: tv.addrs[idx], Signature: &Signature{Scheme: SignatureSchemeSecp256k1, Signature: sig}}
}

// learnt returns the valid block and valid round the engine holds, and how many
// rounds it has a record of a polka for.
func learnt(e *Engine) (*types.Block, int, int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.validBlock, e.validRound, len(e.polkaHistory)
}

// bodyCheckingNode is a node that checks, as the real one does before anything else,
// that the transactions of a block are the ones its header commits to.
type bodyCheckingNode struct{ *signGuardNode }

func (n bodyCheckingNode) ValidateBlock(b *types.Block) error {
	if b != nil && b.Header != nil && !bytes.Equal(testTxRoot(b.Transactions...), b.Header.TxRoot) {
		return errors.New("tx root mismatch")
	}
	return n.signGuardNode.ValidateBlock(b)
}

// A polka's proof covers a header. A message that cites a real polka with the header
// of the block that got it but a body that is not that block's is not learnt, in
// whatever way the body differs, and it is neither passed on nor in the way of the
// real block, which is learnt when it comes.
func TestAPolkaIsNotLearntWithABodyThatIsNotTheHeadersOwn(t *testing.T) {
	tv := newTestValidators(t, 3)
	tx1, tx2, tx3 := &types.Transaction{Nonce: 1}, &types.Transaction{Nonce: 2}, &types.Transaction{Nonce: 3}
	real := blockWithTxs(1, tv.addrs[0], 0x41, tx1, tx2)
	proof := polkaOf(t, tv, real, 4)
	withBody := func(txs ...*types.Transaction) *types.Block { return types.NewBlock(real.Header, txs) }

	forged := []struct {
		name  string
		block *types.Block
	}{
		{"a transaction more", withBody(tx1, tx2, tx3)},
		{"a transaction less", withBody(tx1)},
		{"another transaction", withBody(tx1, tx3)},
		{"the transactions in another order", withBody(tx2, tx1)},
		{"no transactions", withBody()},
		{"blank transactions", withBody(&types.Transaction{}, &types.Transaction{})},
		// Bodies that only a hostile sender has: they must be turned away, not crash
		// the goroutine that reads the network.
		{"a missing transaction", withBody(tx1, nil)},
		{"a transaction that cannot be encoded", withBody(tx1, &types.Transaction{Nonce: 2, Value: big.NewInt(-1)})},
	}
	for _, tc := range forged {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, _, br := engineAt(t, tv, 8)
			if err := engine.HandleProposal(polkaMessage(t, tv, 1, tc.block, 9, 4, proof)); err != nil {
				t.Fatalf("proposal: %v", err)
			}
			held, round, records := learnt(engine)
			if held != nil || round != -1 || records != 0 {
				t.Fatalf("a body that is not the header's own was learnt: valid block %v, valid round %d, %d polka records", held != nil, round, records)
			}
			engine.relayValidBlock()
			if n := len(relaysBy(br.messages(), tv.addrs[0])); n != 0 {
				t.Fatalf("a validator with nothing learnt passed a polka on (%d messages)", n)
			}
		})
	}

	t.Run("and the real block is still learnt", func(t *testing.T) {
		engine, node, br := engineAt(t, tv, 8)
		for _, block := range []*types.Block{withBody(tx1), real} {
			if err := engine.HandleProposal(polkaMessage(t, tv, 1, block, 9, 4, proof)); err != nil {
				t.Fatalf("proposal: %v", err)
			}
		}
		held, round, records := learnt(engine)
		if held == nil || round != 4 || records != 1 || len(held.Transactions) != 2 || !bytes.Equal(headerHash(t, held), headerHash(t, real)) {
			t.Fatalf("the real block was not learnt after a forged one: valid block %v, valid round %d, %d polka records", held != nil, round, records)
		}
		// Learning is not a validation: it costs the node nothing but the checks of the message.
		if n := len(node.validatedHashes()); n != 0 {
			t.Fatalf("learning a polka ran %d block validations", n)
		}
		engine.relayValidBlock()
		relays := relaysBy(br.messages(), tv.addrs[0])
		if len(relays) != 1 || len(relays[0].Proposal.Block.Transactions) != 2 {
			t.Fatalf("expected the real block to be passed on once, got %d messages", len(relays))
		}
	})
}

// The stall itself: a validator that took a forged body for its valid block
// re-proposed it on its own turn, and its own validation of it failed, so it could
// not propose at all for the rest of the height. It proposes a block that validates.
func TestAValidatorSentAForgedBodyStillProposesABlockThatValidates(t *testing.T) {
	tv := newTestValidators(t, 3)
	real := candidateBlock(1, 4, tv.addrs[0], 0x41)
	proof := polkaOf(t, tv, real, 4)
	forged := types.NewBlock(real.Header, []*types.Transaction{{}, {}})

	node := bodyCheckingNode{&signGuardNode{validatorSet: tv.set, self: tv.addrs[0]}}
	br := &captureBroadcaster{}
	engine := NewEngine(node, tv.keys[0], br)
	engine.mu.Lock()
	engine.currentState = State{Height: 1, Round: 8}
	engine.mu.Unlock()

	if err := engine.HandleProposal(polkaMessage(t, tv, 1, forged, 9, 4, proof)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	engine.mu.Lock()
	engine.currentState.Round = 9
	engine.mu.Unlock()
	if err := engine.propose(); err != nil {
		t.Fatalf("the validator could not propose after a polka message with a forged body: %v", err)
	}
	proposals := relaysBy(br.messages(), tv.addrs[0])
	if len(proposals) != 1 {
		t.Fatalf("expected one proposal, got %d", len(proposals))
	}
	if block := proposals[0].Proposal.Block; len(block.Transactions) != 0 || !bytes.Equal(testTxRoot(block.Transactions...), block.Header.TxRoot) {
		t.Fatalf("the validator proposed a block that is not its own to propose")
	}
}

// The polka of the round a validator is in is not learnt from a message: the
// validator's own tally of that round has not finished, and it is the tally that
// locks the validator on the block. A record of the round made by the message left
// the tally with nothing to do, and the validator precommitted the block without
// being locked on it -- free to prevote another block in a later round after
// precommitting this one, which is what the lock is there to stop.
func TestAPolkaOfTheRoundThisValidatorIsInIsNotLearntAndItsOwnTallyStillLocks(t *testing.T) {
	const round = 5
	tv := validatorsWithProposer(t, 2, round, 1)
	engine, _, br := engineAt(t, tv, round)
	block := goodBlock(round, tv.addrs[1], 0x58)
	hash := headerHash(t, block)
	if !engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("test setup: the proposal was not accepted")
	}
	engine.prevote()
	mine := br.votes(tv.addrs[0])
	if len(mine) == 0 {
		t.Fatalf("test setup: no prevote of this validator")
	}
	theirs := signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, round, 1)

	// The other validator's message for a later round cites the polka of this round
	// before its prevote reaches this validator.
	message := polkaMessage(t, tv, 1, block, round+1, round, []*SignedVote{mine[len(mine)-1], theirs})
	if err := engine.HandleProposal(message); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if held, _, records := learnt(engine); held != nil || records != 0 {
		t.Errorf("the polka of the round the validator is in was learnt from a message (%d polka records)", records)
	}

	// The prevote itself arrives: the polka, and the lock that goes with the precommit.
	if _, reachedPrevote, _ := engine.addVoteIfRelevant(theirs); !reachedPrevote {
		t.Fatalf("test setup: the prevotes are no polka")
	}
	engine.precommit()
	_, _, locked := engine.Status()
	if precommits := countType(br.votes(tv.addrs[0]), Precommit); precommits != 1 || locked != round {
		t.Errorf("SAFETY: %d precommit(s) for the block of round %d, and the validator is locked at round %d", precommits, round, locked)
	}

	// What the lock is for: a validator that precommitted a block does not prevote
	// another in a later round unless a later polka has moved on.
	later := round + 2
	engine.mu.Lock()
	engine.currentState.Round = later
	engine.activeProposal = nil
	engine.prevoteSent = false
	engine.mu.Unlock()
	other := goodBlock(later, tv.addrs[1], 0x59)
	if !engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: other, Round: later, ValidRound: -1}, Proposer: tv.addrs[1]}) {
		t.Fatalf("test setup: the proposal of round %d was not accepted", later)
	}
	engine.prevote()
	votes := br.votes(tv.addrs[0])
	if last := votes[len(votes)-1]; last.Vote.Type != Prevote || last.Vote.Round != later || len(last.Vote.BlockHash) != 0 {
		t.Fatalf("SAFETY: the validator precommitted the block of round %d and then prevoted another block in round %d: %s", round, later, describeVotes(votes))
	}
}

// A polka is learnt only when it is later than the one held and of a round that the
// validator has already passed: never one of the round it is in, or of a later round.
func TestAPolkaIsLearntOnlyOfARoundThisValidatorHasPassed(t *testing.T) {
	tv := newTestValidators(t, 3)
	cases := []struct {
		name       string
		polkaRound int
		learn      bool
	}{
		{"the round before", 7, true},
		{"an earlier round", 2, true},
		{"the round it is in", 8, false},
		{"the next round", 9, false},
		{"a round far ahead", 40, false},
	}
	for i, tc := range cases {
		tc := tc
		salt := byte(0x60 + i)
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineAt(t, tv, 8)
			block := candidateBlock(1, tc.polkaRound, tv.addrs[0], salt)
			message := polkaMessage(t, tv, 1, block, 50, tc.polkaRound, polkaOf(t, tv, block, tc.polkaRound))
			if err := engine.HandleProposal(message); err != nil {
				t.Fatalf("proposal: %v", err)
			}
			held, round, records := learnt(engine)
			if got := held != nil; got != tc.learn {
				t.Fatalf("learnt=%v for a polka of round %d, want %v", got, tc.polkaRound, tc.learn)
			}
			if tc.learn && (round != tc.polkaRound || records != 1) {
				t.Fatalf("valid round %d with %d records, want %d with 1", round, records, tc.polkaRound)
			}
			engine.mu.RLock()
			defer engine.mu.RUnlock()
			for r := range engine.polkaHistory {
				if r >= engine.currentState.Round {
					t.Fatalf("a record of the polka of round %d was made in round %d, from a message", r, engine.currentState.Round)
				}
			}
		})
	}
}

// The lock is set when this validator's own tally reaches the polka of a round,
// whatever else is recorded for that round, and a lock is never lowered to an
// earlier round.
func TestTheLockIsSetAtThePolkaOfARoundWhateverElseIsRecordedForIt(t *testing.T) {
	tally := func(t *testing.T, tv *testValidators, e *Engine, round int, block *types.Block) {
		t.Helper()
		hash := headerHash(t, block)
		if !e.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: round, ValidRound: -1}, Proposer: tv.addrs[1]}) {
			t.Fatalf("test setup: the proposal was not accepted")
		}
		for i := range tv.keys {
			e.addVoteIfRelevant(signVoteAs(t, tv.keys[i], tv.addrs[i], Prevote, hash, round, 1))
		}
	}

	t.Run("a record of the round made otherwise", func(t *testing.T) {
		const round = 5
		tv := newTestValidators(t, 3)
		engine, _, _ := engineAt(t, tv, round)
		block, other := goodBlock(round, tv.addrs[1], 0x33), goodBlock(2, tv.addrs[1], 0x34)
		engine.mu.Lock()
		engine.polkaHistory[round] = polkaRecord{block: other, blockHash: headerHash(t, other), votes: polkaOf(t, tv, other, round)}
		engine.mu.Unlock()

		tally(t, tv, engine, round, block)

		engine.mu.RLock()
		defer engine.mu.RUnlock()
		if engine.lockedRound != round || !bytes.Equal(engine.lockedBlockHash, headerHash(t, block)) {
			t.Fatalf("SAFETY: the polka of round %d did not lock the validator (locked round %d)", round, engine.lockedRound)
		}
		if record := engine.polkaHistory[round]; !bytes.Equal(record.blockHash, headerHash(t, block)) || engine.validBlock != block || engine.validRound != round {
			t.Fatalf("the record of the round is not the polka this validator tallied")
		}
	})

	// A lock a running validator holds is always of a round it has reached; this is
	// what a lock restored from disk looks like beside a round counter that has not
	// caught up, and a polka of the round it is in must not take the lock back to it.
	t.Run("a lock of a later round", func(t *testing.T) {
		tv := newTestValidators(t, 3)
		engine, _, _ := engineAt(t, tv, 5)
		locked := goodBlock(9, tv.addrs[1], 0x35)
		engine.mu.Lock()
		engine.lockedRound, engine.lockedBlockHash = 9, headerHash(t, locked)
		engine.mu.Unlock()

		tally(t, tv, engine, 5, goodBlock(5, tv.addrs[1], 0x36))

		engine.mu.RLock()
		defer engine.mu.RUnlock()
		if engine.lockedRound != 9 || !bytes.Equal(engine.lockedBlockHash, headerHash(t, locked)) {
			t.Fatalf("the lock of round 9 was replaced by one of round %d", engine.lockedRound)
		}
	})
}

// Checking a message that carries a polka must not exclude the validator's other
// work: it is done for every proposal of every validator, on the goroutines that
// read the network, and it holds no more than a read lock while it does. With a
// reader in, a proof that fails its checks -- whether its signatures do not verify
// or the body is not the header's own -- is turned away without waiting for the write
// lock.
func TestCheckingAPolkaProofDoesNotWaitForTheEnginesWriteLock(t *testing.T) {
	tv := newTestValidators(t, 3)
	real := blockWithTxs(1, tv.addrs[0], 0x41, &types.Transaction{Nonce: 1})
	hash := headerHash(t, real)

	// Votes that name each validator but are signed by the next one's key.
	var unsigned []*SignedVote
	for i := range tv.keys {
		unsigned = append(unsigned, signVoteAs(t, tv.keys[(i+1)%len(tv.keys)], tv.addrs[i], Prevote, hash, 4, 1))
	}
	cases := []struct {
		name    string
		message *SignedProposal
	}{
		{"signatures that do not verify", polkaMessage(t, tv, 1, real, 9, 4, unsigned)},
		{"a body that is not the header's own", polkaMessage(t, tv, 1, types.NewBlock(real.Header, nil), 9, 4, polkaOf(t, tv, real, 4))},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine, _, _ := engineAt(t, tv, 8)
			engine.mu.RLock() // another reader of the engine's state, in the middle of its work
			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.learnValidBlock(tc.message)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				engine.mu.RUnlock()
				<-done
				t.Fatalf("checking the proof waited for the engine's write lock")
			}
			engine.mu.RUnlock()
			if held, _, _ := learnt(engine); held != nil {
				t.Fatalf("a proof that fails its checks was learnt")
			}
		})
	}
}

// A block restored from the lock snapshot is only the block the validator locked on
// if its transactions are the ones its header commits to: the hash the snapshot
// records covers the header and no more. A restored body that is not the header's
// own is not kept as the block to re-propose, and the lock itself is restored all the
// same.
func TestARestoredBlockWhoseBodyIsNotTheHeadersOwnIsNotKept(t *testing.T) {
	f := newFourValidatorFixture(t)
	tx1, tx2 := &types.Transaction{Nonce: 1}, &types.Transaction{Nonce: 2}
	block := blockWithTxs(1, f.addrs[0], 0x11, tx1, tx2)
	hash := headerHash(t, block)
	votes := []*SignedVote{
		f.signedPrevote(t, 0, hash, 0, 1),
		f.signedPrevote(t, 1, hash, 0, 1),
		f.signedPrevote(t, 2, hash, 0, 1),
	}
	cases := []struct {
		name string
		body *types.Block
		keep bool
	}{
		{"the block that was locked on", block, true},
		{"a body with a transaction less", types.NewBlock(block.Header, []*types.Transaction{tx1}), false},
		{"a body of other transactions", types.NewBlock(block.Header, []*types.Transaction{tx1, {Nonce: 3}}), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "polc_lock.json")
			if err := writeLockSnapshot(path, lockSnapshot{Height: 1, LockedRound: 0, LockedBlockHash: hash, LockedBlock: tc.body, PolkaVotes: votes}); err != nil {
				t.Fatalf("write snapshot: %v", err)
			}
			engine := NewEngine(&trackingNode{validatorSet: f.validator}, f.keys[0], &recordingBroadcaster{}, WithLockSnapshotPath(path))
			engine.mu.RLock()
			defer engine.mu.RUnlock()
			if engine.lockedRound != 0 || !bytes.Equal(engine.lockedBlockHash, hash) {
				t.Fatalf("the lock was not restored: round %d", engine.lockedRound)
			}
			if kept := engine.validBlock != nil; kept != tc.keep {
				t.Fatalf("the restored block was kept=%v, want %v", kept, tc.keep)
			}
		})
	}
}

// Of a polka it is told of, a validator keeps what the proof and the root vouch for
// and nothing else. A message that carries more -- a quorum certificate on the
// block, votes that are not part of the polka, padding in a vote's unsigned fields
// -- would have this validator carry it into its own proposal of the block and the
// messages it sends of the polka, which a peer ends its connection over when they
// are larger than the transport allows.
func TestWhatIsLearntOfAPolkaIsOnlyWhatWasProven(t *testing.T) {
	tv := newTestValidators(t, 4)
	real := blockWithTxs(1, tv.addrs[0], 0x41, &types.Transaction{Nonce: 1})
	hash := headerHash(t, real)

	padded := types.NewBlock(real.Header, real.Transactions)
	padded.QuorumCert = &types.QuorumCert{Height: 1, Round: 4, BlockHash: hash,
		Signatures: []types.QuorumSignature{{Validator: tv.addrs[0], Signature: make([]byte, 200<<10)}}}
	proof := polkaOf(t, tv, real, 4)[:3:3] // three of four make the polka
	for _, vote := range proof {
		vote.Signature.PublicKey = make([]byte, 100<<10) // not signed, not used to verify
	}
	// A vote of the fourth validator that is not part of the polka, with room to spare.
	proof = append(proof, signVoteAs(t, tv.keys[3], tv.addrs[3], Prevote, make([]byte, 200<<10), 4, 1))

	engine, _, br := engineAt(t, tv, 8)
	if err := engine.HandleProposal(polkaMessage(t, tv, 1, padded, 9, 4, proof)); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	held, round, _ := learnt(engine)
	if held == nil || round != 4 {
		t.Fatalf("the polka was not learnt (valid round %d)", round)
	}
	if held.QuorumCert != nil {
		t.Errorf("the quorum certificate that came with the block was kept")
	}
	engine.mu.RLock()
	kept := engine.polkaHistory[4].votes
	engine.mu.RUnlock()
	if len(kept) != 3 {
		t.Errorf("%d votes were kept of the proof, want the 3 that make the polka", len(kept))
	}
	for _, vote := range kept {
		if len(vote.Signature.PublicKey) != 0 {
			t.Errorf("a vote was kept with %d bytes of unsigned padding", len(vote.Signature.PublicKey))
		}
	}
	if counted, _ := countedPolkaVotes(kept, 4, hash, 1, tv.set); len(counted) != 3 {
		t.Errorf("what was kept of the proof is not the polka: %d of its votes count", len(counted))
	}

	engine.relayValidBlock()
	relays := br.messages()
	if len(relays) != 1 {
		t.Fatalf("expected the polka to be passed on once, %d messages were sent", len(relays))
	}
	if size := len(relays[0].Payload); size > 16<<10 {
		t.Fatalf("the polka was passed on in a message of %d bytes", size)
	}
}

// The same holds for a polka this validator tallied itself: a validator whose vote is
// part of it can pad the vote's unsigned fields, and the message that passes the polka
// on -- which a message from any validator that is behind makes this one send --
// does not carry them.
func TestAPolkaPassedOnCarriesNoPaddingFromTheVotesThatMadeIt(t *testing.T) {
	const round = 7
	tv := newTestValidators(t, 2)
	engine, _, br := engineAt(t, tv, 0)
	block := candidateBlock(1, round, tv.addrs[0], 0x58)
	hash := headerHash(t, block)
	engine.mu.Lock()
	engine.currentState.Round = round
	engine.mu.Unlock()
	if !engine.acceptProposal(&SignedProposal{Proposal: &Proposal{Block: block, Round: round, ValidRound: -1}, Proposer: tv.addrs[0]}) {
		t.Fatalf("test setup: the proposal was not accepted")
	}
	engine.prevote()
	theirs := signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, round, 1)
	theirs.Signature.PublicKey = make([]byte, 300<<10) // signed over without it
	engine.addVoteIfRelevant(theirs)
	if _, _, locked := engine.Status(); locked != round {
		t.Fatalf("test setup: not locked at round %d", round)
	}
	engine.mu.Lock()
	engine.currentState.Round = 12
	engine.activeProposal = nil
	engine.mu.Unlock()

	engine.relayValidBlock()
	relays := relaysBy(br.messages(), tv.addrs[0])
	if len(relays) != 1 {
		t.Fatalf("expected the polka to be passed on once, %d messages were sent", len(relays))
	}
	if size := len(br.messages()[len(br.messages())-1].Payload); size > 16<<10 {
		t.Fatalf("the polka was passed on in a message of %d bytes", size)
	}
	// And what is passed on is still a polka to the validator that hears it.
	other, _, _ := engineAt(t, tv, 13)
	if err := other.HandleProposal(relays[0]); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if held, validRound, _ := learnt(other); held == nil || validRound != round {
		t.Fatalf("what was passed on was not learnt as the polka of round %d", round)
	}
}

// The votes that are kept and passed on still verify, whatever scheme they were
// signed in: the public key is dropped from the ones whose verification does not use
// it, and kept for the ones whose does.
func TestPlainVotesStillVerify(t *testing.T) {
	tv := newTestValidators(t, 1)
	hash := sha256Sum([]byte("block"))

	secp := signVoteAs(t, tv.keys[0], tv.addrs[0], Prevote, hash, 3, 1)
	secp.Signature.PublicKey = []byte("padding")

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	vote := &Vote{BlockHash: hash, Round: 3, Type: Prevote, Height: 1}
	edSigned := &SignedVote{
		Vote:      vote,
		Validator: ethcrypto.Keccak256(pub)[12:],
		Signature: &Signature{Scheme: SignatureSchemeEd25519, Signature: ed25519.Sign(priv, sha256Sum(vote.bytes())), PublicKey: pub},
	}
	for name, sv := range map[string]*SignedVote{"secp256k1": secp, "ed25519": edSigned} {
		if err := verifyVoteSignature(sv); err != nil {
			t.Fatalf("test setup: %s vote does not verify: %v", name, err)
		}
	}

	plain := plainVotes([]*SignedVote{secp, nil, {}, {Vote: vote}, edSigned})
	if len(plain) != 2 {
		t.Fatalf("%d votes came out of plainVotes, want the 2 whole ones", len(plain))
	}
	for _, sv := range plain {
		if err := verifyVoteSignature(sv); err != nil {
			t.Fatalf("a plain %s vote no longer verifies: %v", sv.Signature.Scheme, err)
		}
	}
	if len(plain[0].Signature.PublicKey) != 0 {
		t.Fatalf("the padding of a secp256k1 vote was kept")
	}
	if !bytes.Equal(plain[1].Signature.PublicKey, pub) {
		t.Fatalf("the public key of an ed25519 vote, which verifying it needs, was not kept")
	}
	// A copy: what was passed in is not changed, and does not share its bytes.
	if len(secp.Signature.PublicKey) == 0 || &plain[0].Signature.Signature[0] == &secp.Signature.Signature[0] {
		t.Fatalf("plainVotes did not copy")
	}
}

// A block reaches a validator as JSON, and the body is only the header's own if the
// root of the transactions that came out of the JSON is the one that went in. It is,
// for transactions with every field a transaction has -- the check would otherwise
// turn away every real block.
func TestABlockKeepsItsTransactionRootThroughTheWire(t *testing.T) {
	txs := []*types.Transaction{
		{ChainID: types.NHBChainID(), Type: types.TxTypeTransfer, Nonce: 7, To: bytes.Repeat([]byte{0xAB}, 20), Value: big.NewInt(1_000_000),
			Data: []byte{0x00, 0xFF, 0x10, 0x80}, GasLimit: 21000, GasPrice: big.NewInt(1), R: big.NewInt(12345), S: big.NewInt(67890), V: big.NewInt(27)},
		{ChainID: types.NHBChainID(), Type: types.TxTypeHeartbeat, Nonce: 0, To: nil, Value: big.NewInt(0), Data: nil, GasLimit: 0, GasPrice: big.NewInt(0),
			R: new(big.Int).Lsh(big.NewInt(1), 250), S: big.NewInt(1), V: big.NewInt(28)},
		{ChainID: types.NHBChainID(), Type: types.TxTypeMint, Nonce: 1 << 40, To: bytes.Repeat([]byte{0x01}, 20), Value: new(big.Int).Lsh(big.NewInt(1), 130), Data: bytes.Repeat([]byte{0xC3}, 300),
			GasLimit: 1 << 33, GasPrice: big.NewInt(9), MaxBlockHeight: 99, Paymaster: bytes.Repeat([]byte{0x02}, 20), IntentRef: bytes.Repeat([]byte{0x03}, 32), IntentExpiry: 12345,
			MerchantAddress: "merchant", DeviceID: "device", RefundOf: "refund", R: big.NewInt(3), S: big.NewInt(4), V: big.NewInt(27),
			PaymasterR: big.NewInt(5), PaymasterS: big.NewInt(6), PaymasterV: big.NewInt(28)},
		{Type: types.TxTypeStake},
	}
	block := blockWithTxs(1, bytes.Repeat([]byte{0x05}, 20), 0x77, txs...)
	if !blockBodyMatchesHeader(block) {
		t.Fatalf("test setup: a block does not match its own header")
	}
	raw, err := json.Marshal(&SignedProposal{Proposal: &Proposal{Block: block, Round: 3, ValidRound: -1}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded SignedProposal
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !blockBodyMatchesHeader(decoded.Proposal.Block) {
		t.Fatalf("the transactions of a block no longer match its header's root after a round trip through JSON")
	}
}
