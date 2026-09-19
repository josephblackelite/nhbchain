package core

import (
	"errors"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// Two independent validators: a hardened proposer's blocks must be accepted,
// and re-derived to the identical state root, by a second node that never ran
// the containment code on them. Everything in the containment layer is
// proposer-local, so this is the property that makes a rolling deploy safe.

// poisonForRound builds one hostile transaction of the given type and payload
// from a fresh, unfunded signer (gas price 0 needs no funds).
func poisonForRound(t *testing.T, typ types.TxType, payload []byte) *types.Transaction {
	t.Helper()
	to := make([]byte, 20)
	to[19] = 0x03
	return livenessSign(t, livenessKey(t), typ, 0, payload, to, 0)
}

// The adversarial mempool: every round, a poison transaction of a handful of
// types (rotating through all of them over the run) sits next to valid
// transfers. Both validators must agree after every block.
func TestTwoNodeAdversarialMempoolAgreesForTwentyRounds(t *testing.T) {
	honest := []*crypto.PrivateKey{livenessKey(t), livenessKey(t), livenessKey(t)}
	pair := newLivenessPair(t, livenessPairOptions{funded: honest})
	nonces := make([]uint64, len(honest))
	all := matrixTypes()
	payloads := livenessPayloads()

	var totalIncluded int
	for round := 0; round < 20; round++ {
		// Poison from four different types, with payloads rotating too.
		for k := 0; k < 4; k++ {
			typ := all[(round*4+k)%len(all)]
			payload := payloads[(round+k)%len(payloads)]
			livenessInject(pair.proposer, poisonForRound(t, typ, payload.data))
		}
		// Valid traffic.
		for i, key := range honest {
			pair.submit(livenessTransfer(t, key, nonces[i]))
			nonces[i]++
		}
		block := pair.mine() // fails the test on any error or any state-root disagreement
		for _, tx := range block.Transactions {
			if tx.Type == types.TxTypeTransfer {
				totalIncluded++
			}
		}
	}
	if totalIncluded != 20*len(honest) {
		t.Fatalf("expected every valid transfer to be included, got %d of %d", totalIncluded, 20*len(honest))
	}
}

// A seeded random mix of garbage and valid transactions of random types, for
// fifty rounds. The seed is fixed so a failure is reproducible.
func TestTwoNodeSeededRandomMixAgreesForFiftyRounds(t *testing.T) {
	const seed = 20260919
	rng := rand.New(rand.NewSource(seed))
	honest := []*crypto.PrivateKey{livenessKey(t), livenessKey(t)}
	pair := newLivenessPair(t, livenessPairOptions{funded: honest})
	nonces := make([]uint64, len(honest))
	all := matrixTypes()
	payloads := livenessPayloads()

	for round := 0; round < 50; round++ {
		for k, count := 0, rng.Intn(7); k < count; k++ {
			typ := all[rng.Intn(len(all))]
			payload := payloads[rng.Intn(len(payloads))]
			livenessInject(pair.proposer, poisonForRound(t, typ, payload.data))
		}
		for i, key := range honest {
			if rng.Intn(3) == 0 {
				continue
			}
			pair.submit(livenessTransfer(t, key, nonces[i]))
			nonces[i]++
		}
		pair.mine()
	}
	if pair.proposer.GetHeight() != 50 || pair.validator.GetHeight() != 50 {
		t.Fatalf("expected both nodes at height 50, got %d and %d", pair.proposer.GetHeight(), pair.validator.GetHeight())
	}
}

// The empty-fallback block: a proposer whose lifecycle stage fails whenever
// transactions are present builds the empty block instead. A second node
// (which has no such fault) accepts it and derives the same state.
func TestTwoNodeEmptyFallbackBlockIsAcceptedByThePeer(t *testing.T) {
	honest := []*crypto.PrivateKey{livenessKey(t), livenessKey(t)}
	pair := newLivenessPair(t, livenessPairOptions{funded: honest})
	pair.proposer.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		if len(blockTxs) > 0 {
			return errors.New("injected lifecycle failure while transactions are present")
		}
		return nil
	})
	pair.submit(livenessTransfer(t, honest[0], 0))
	pair.submit(livenessTransfer(t, honest[1], 0))

	block := pair.build()
	if len(block.Transactions) != 0 {
		t.Fatalf("expected the empty fallback block, got %d transactions", len(block.Transactions))
	}
	pair.commit(block) // the peer validates and commits it, and the roots agree
	if pair.proposer.MempoolSize() != 2 {
		t.Fatalf("the fallback must not lose transactions, mempool has %d", pair.proposer.MempoolSize())
	}
}

// The prefix-fallback block: the wave cap is exhausted, the verified prefix is
// used, and the peer accepts that partial block.
func TestTwoNodePrefixFallbackBlockIsAcceptedByThePeer(t *testing.T) {
	honest := make([]*crypto.PrivateKey, 12)
	for i := range honest {
		honest[i] = livenessKey(t)
	}
	pair := newLivenessPair(t, livenessPairOptions{funded: honest})
	txs := make([]*types.Transaction, len(honest))
	for i, key := range honest {
		txs[i] = livenessTransfer(t, key, 0)
		pair.submit(txs[i])
	}
	waveCap := pair.proposer.buildConfigSnapshot().MaxWaves
	pair.proposer.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if wave >= 1 && wave <= waveCap && tx == txs[len(txs)-wave] {
			return errors.New("injected chain failure")
		}
		return nil
	})

	block := pair.build()
	if got, want := len(block.Transactions), len(txs)-waveCap; got != want {
		t.Fatalf("expected the verified prefix of %d transactions, got %d", want, got)
	}
	pair.commit(block)
}

// A proposer's exclusion of a transaction is local: the same transaction is
// still included by a peer that has no reason to exclude it, and the two chains
// stay identical either way.
func TestTwoNodeExclusionIsProposerLocal(t *testing.T) {
	honest := []*crypto.PrivateKey{livenessKey(t), livenessKey(t)}
	pair := newLivenessPair(t, livenessPairOptions{funded: honest})
	first := livenessTransfer(t, honest[0], 0)
	second := livenessTransfer(t, honest[1], 0)
	pair.submit(first)
	pair.submit(second)
	// This proposer alone refuses to include `second`.
	pair.proposer.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx == second {
			return errors.New("local policy: not included by this proposer")
		}
		return nil
	})
	block := pair.mine()
	if len(block.Transactions) != 1 || !livenessBlockContains(t, block, first) {
		t.Fatalf("expected only the first transaction, got %d", len(block.Transactions))
	}
	// The peer accepted that block. It can include the excluded transaction in
	// its own next block, and the original proposer accepts THAT.
	secondOnPeer := *second
	if err := pair.validator.AddTransaction(&secondOnPeer); err != nil {
		t.Fatalf("admit on the peer: %v", err)
	}
	pair.proposer.setProposalApplyHook(nil)
	peerBlock, err := pair.validator.CreateBlock(pair.validator.GetMempool())
	if err != nil {
		t.Fatalf("peer CreateBlock: %v", err)
	}
	if len(peerBlock.Transactions) != 1 {
		t.Fatalf("the peer must include the transaction the other proposer excluded, got %d", len(peerBlock.Transactions))
	}
	if err := pair.validator.CommitBlock(peerBlock); err != nil {
		t.Fatalf("peer commit: %v", err)
	}
	if err := pair.proposer.ValidateBlock(peerBlock); err != nil {
		t.Fatalf("the original proposer must accept the peer's block: %v", err)
	}
	if err := pair.proposer.CommitBlock(peerBlock); err != nil {
		t.Fatalf("original proposer commit: %v", err)
	}
	pair.requireAgreement()
}

// A rolling deploy: an UNHARDENED validator cannot be simulated here, but its
// role is exactly ValidateBlock and CommitBlock, which this change leaves
// untouched apart from the typed wrapper around the same error message. Pin the
// message and the unwrap behaviour, since bft and the logs depend on both.
func TestBlockApplyErrorKeepsTheOriginalMessageAndUnwraps(t *testing.T) {
	base := errors.New("escrow: cannot release in status 3")
	err := error(&blockApplyError{Index: 7, Err: base})
	if got, want := err.Error(), "apply transaction 7: escrow: cannot release in status 3"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if !errors.Is(err, base) {
		t.Fatalf("errors.Is must see through the wrapper")
	}
	var applyErr *blockApplyError
	if !errors.As(err, &applyErr) || applyErr.Index != 7 {
		t.Fatalf("errors.As must recover the index")
	}
}

// A block whose own validation fails on the proposer (a transaction that
// applied while building but not while validating) is charged to that
// transaction, which is evicted once it has caused Strikes such rejections.
func TestRejectedOwnProposalIsChargedToTheTransactionThatCausedIt(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 3)
	victim := txs[1]

	// Build a block, then make VALIDATION (not the build) reject the victim by
	// running the same block through a node whose state lacks its funds.
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if !livenessBlockContains(t, block, victim) {
		t.Fatalf("test setup: the victim must be in the block")
	}
	var applyErr error = &blockApplyError{Index: 1, Err: errors.New("injected: rejected by validation")}
	for i := 1; i <= 3; i++ {
		node.NoteProposalOutcome(block.Header.Height, 0, "local_validation_failed", len(block.Transactions), applyErr)
		if i < 3 && !livenessResident(t, node, victim) {
			t.Fatalf("the victim was evicted after %d rejections; expected the third", i)
		}
	}
	if livenessResident(t, node, victim) {
		t.Fatalf("a transaction that caused three rejections of the proposer's own block must be evicted")
	}
	if !livenessResident(t, node, txs[0]) || !livenessResident(t, node, txs[2]) {
		t.Fatalf("the innocent transactions of that block must stay")
	}
	if got := node.LivenessSnapshot().LocalValidationFailures; got != 3 {
		t.Fatalf("expected 3 local validation failures counted, got %d", got)
	}
}

// KNOWN ISSUE, confirmed on unmodified origin/main: penalty idempotency lives in
// a Node-wide, in-memory ledger (n.potsoLedger) that is not scoped to a state
// copy, and processPendingEvidenceForState runs on EVERY pass over a block --
// the proposer's build, the proposer's own validation, and the commit. The first
// pass applies the slash and records that it did; every later pass on the same
// node sees "already applied", skips it, and so computes a DIFFERENT state root.
// Consequences observed here:
//
//  1. a block that contains an accepted evidence submission fails its own
//     proposer's local ValidateBlock (bft.propose refuses to propose it);
//  2. once the ledger has consumed the penalty, that node builds evidence blocks
//     WITHOUT the slash, which a node with a fresh ledger (the peer, or the same
//     node after a restart) rejects with a state-root mismatch.
//
// The containment layer cannot fix this and must not try: it is consensus
// behaviour inside block processing (rule: nothing that changes what a block
// applies may ship as a proposer-local change). It does keep the chain alive
// through it (the proposer releases the block's transactions, and after two
// failed proposals at a height proposes an empty block). The fix is a
// coordinated one -- move the idempotency key into the state trie (or scope the
// ledger to the state copy) behind an activation height -- and is described in
// the report. This test pins the observed behaviour so the issue stays visible,
// and so that whoever fixes it is told, by this test failing, to delete it.
func TestKnownIssueEvidenceSlashingIsNotIdempotentAcrossPasses(t *testing.T) {
	adminKey := livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{adminKey: adminKey, sharedValidatorKey: true})
	proposer, validator := pair.proposer, pair.validator

	offenderKey, reporterKey := newEvidenceTestKey(t), newEvidenceTestKey(t)
	offender := offenderKey.address()
	for _, node := range []*Node{proposer, validator} {
		seedEvidenceOffenderStake(t, node, offender, big.NewInt(1_000))
		node.stateMu.Lock()
		root, err := node.state.Commit(node.chain.GetHeight())
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("commit seeded state: %v", err)
		}
		commitStateAsEmptyBlock(t, node, root)
	}
	pair.requireAgreement()

	ev, _ := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, 1)
	payload, err := encodeSubmitEvidenceTransaction(ev)
	if err != nil {
		t.Fatalf("encode evidence transaction: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSubmitEvidence, Data: payload, GasLimit: 0, GasPrice: big.NewInt(0)}
	pair.submit(tx)

	// Pass 1 (CreateBlock) applies the slash; pass 2 (the proposer's own
	// validation, exactly what bft.propose runs next) finds the penalty already
	// recorded and does not.
	first := pair.build()
	if !livenessBlockContains(t, first, tx) {
		t.Fatalf("the evidence transaction must be in the block")
	}
	err = proposer.ValidateBlock(first)
	if err == nil {
		t.Fatalf("the known issue appears to be FIXED (the proposer's own validation of an evidence block now passes): delete this test and update the report")
	}
	if !strings.Contains(err.Error(), "state root mismatch") {
		t.Fatalf("expected the known state-root mismatch, got: %v", err)
	}

	// The proposer's ledger has now consumed the penalty: its next evidence
	// block validates locally (both passes skip the slash) ...
	proposer.RequeueTransactions(first.Transactions)
	second := pair.build()
	if !livenessBlockContains(t, second, tx) {
		t.Fatalf("the evidence transaction must be offered again")
	}
	if err := proposer.ValidateBlock(second); err != nil {
		t.Fatalf("expected the second block to pass the proposer's own validation, got: %v", err)
	}
	// ... but a node with a fresh ledger applies the slash and disagrees.
	if err := validator.ValidateBlock(second); err == nil {
		t.Fatalf("the known issue appears to be FIXED (a peer accepts the evidence block built after the proposer's ledger consumed the penalty): delete this test and update the report")
	}
}
