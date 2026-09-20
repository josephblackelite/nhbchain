package core

import (
	"errors"
	"math/big"
	"math/rand"
	"testing"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
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

// The equivocation slash is a function of state alone, so every pass over a block
// -- the proposer's build, the proposer's own validation, the commit, a peer's
// validation and commit -- computes the same state root, and the slash lands
// exactly once. The record of which penalties have been applied is a trie
// record, and the tracked weight is rebuilt from the offender's stake on each
// pass, so nothing about an earlier pass (or an earlier block) is remembered in
// memory where a discarded trial build could consume it.
//
// The block here carries real equivocation evidence: two conflicting prevotes
// signed by the offender's own key, reported by a bonded reporter in an
// ordinary signed transaction.
func TestEquivocationEvidenceBlockIsBuiltValidatedCommittedAndReplayedWithOneSlash(t *testing.T) {
	adminKey := livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{adminKey: adminKey, sharedValidatorKey: true})
	proposer, validator := pair.proposer, pair.validator
	admin := adminKey.PubKey().Address().Bytes()

	const stake = 1_000
	reporterKey := newEvidenceTestKey(t)
	offenderA, offenderB := newEvidenceTestKey(t), newEvidenceTestKey(t)
	for _, node := range []*Node{proposer, validator} {
		seedEvidenceOffenderStake(t, node, offenderA.address(), big.NewInt(stake))
		seedEvidenceOffenderStake(t, node, offenderB.address(), big.NewInt(stake))
		seedEvidenceReporterBond(t, node, reporterKey.address())
		node.stateMu.Lock()
		root, err := node.state.Commit(node.chain.GetHeight())
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("commit seeded state: %v", err)
		}
		commitStateAsEmptyBlock(t, node, root)
	}
	pair.requireAgreement()
	// The first block's lifecycle splits the treasury wallet's ZNHB into the
	// Sale and Reward Pools; start from there so the pool is tracked before
	// anything is measured.
	pair.mine()

	// observe reads what the slash moves: the offender's locked stake, the
	// treasury's liquid ZNHB and the Reward Pool, plus whether the penalty is
	// recorded as applied.
	type snapshot struct {
		locked, treasury, rewardPool *big.Int
		poolsTracked                 bool
	}
	observe := func(node *Node, offender [20]byte) snapshot {
		t.Helper()
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		manager := nhbstate.NewManager(node.state.Trie)
		offenderAccount, err := manager.GetAccount(offender[:])
		if err != nil {
			t.Fatalf("load offender: %v", err)
		}
		treasuryAccount, err := manager.GetAccount(admin)
		if err != nil {
			t.Fatalf("load treasury: %v", err)
		}
		tracked, err := manager.ZNHBPoolsBootstrapped()
		if err != nil {
			t.Fatalf("pool bootstrap flag: %v", err)
		}
		rewardPool, err := manager.ZNHBRewardPoolBalance()
		if err != nil {
			t.Fatalf("reward pool: %v", err)
		}
		return snapshot{
			locked:       new(big.Int).Set(offenderAccount.LockedZNHB),
			treasury:     new(big.Int).Set(treasuryAccount.BalanceZNHB),
			rewardPool:   rewardPool,
			poolsTracked: tracked,
		}
	}
	requireInvariant := func(node *Node, label string) {
		t.Helper()
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		if err := node.state.CheckZNHBSupplyInvariant(); err != nil {
			t.Fatalf("%s: supply invariant broken by the slash: %v", label, err)
		}
	}
	penaltyApplied := func(node *Node, ev evidence.Evidence, offender [20]byte) bool {
		t.Helper()
		hash, err := ev.CanonicalHash()
		if err != nil {
			t.Fatalf("evidence hash: %v", err)
		}
		// The record is kept against the offense the report accuses the offender
		// of, not against the report's own hash.
		offense := (&evidence.Record{Hash: hash, Evidence: ev}).Offense().Key
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		applied, err := nhbstate.NewManager(node.state.Trie).PotsoPenaltyApplied(offense, offender)
		if err != nil {
			t.Fatalf("penalty record: %v", err)
		}
		return applied
	}
	submit := func(ev evidence.Evidence, nonce uint64) *types.Transaction {
		t.Helper()
		payload, err := encodeSubmitEvidenceTransaction(ev)
		if err != nil {
			t.Fatalf("encode evidence transaction: %v", err)
		}
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSubmitEvidence, Nonce: nonce, Data: payload, GasLimit: 100_000, GasPrice: big.NewInt(0)}
		if err := tx.Sign(reporterKey.priv); err != nil {
			t.Fatalf("sign evidence transaction: %v", err)
		}
		pair.submit(tx)
		return tx
	}
	// requireSlashedOnce checks, on both nodes, that the offender's stake went
	// to the treasury exactly once (and the Reward Pool followed when the pools
	// are tracked), and that the record says so.
	requireSlashedOnce := func(label string, ev evidence.Evidence, offender [20]byte, before snapshot) {
		t.Helper()
		for name, node := range map[string]*Node{"proposer": proposer, "validator": validator} {
			after := observe(node, offender)
			if after.locked.Sign() != 0 {
				t.Fatalf("%s: the %s still holds %s locked stake: the slash did not land", label, name, after.locked)
			}
			if got := new(big.Int).Sub(after.treasury, before.treasury); got.Cmp(big.NewInt(stake)) != 0 {
				t.Fatalf("%s: the %s's treasury gained %s, want exactly the %d slashed (once, not twice and not zero)", label, name, got, stake)
			}
			if after.poolsTracked {
				if got := new(big.Int).Sub(after.rewardPool, before.rewardPool); got.Cmp(big.NewInt(stake)) != 0 {
					t.Fatalf("%s: the %s's Reward Pool gained %s, want %d", label, name, got, stake)
				}
			}
			if !penaltyApplied(node, ev, offender) {
				t.Fatalf("%s: the %s has no record that the penalty was applied", label, name)
			}
			requireInvariant(node, label+" ("+name+")")
		}
	}

	// ---- first offender: build, self-validate, commit, peer replay --------
	evA, _ := buildGenuineEquivocationEvidence(t, offenderA, reporterKey, 1)
	beforeA := observe(proposer, offenderA.address())
	if !beforeA.poolsTracked {
		t.Fatalf("the treasury pools must be tracked by now, or the Reward Pool checks below prove nothing")
	}
	txA := submit(evA, 0)

	first := pair.build()
	if !livenessBlockContains(t, first, txA) {
		t.Fatalf("the evidence transaction must be in the block")
	}
	// bft.propose runs the proposer's own validation next; it used to disagree
	// with the build over whether the slash had already been applied.
	if err := proposer.ValidateBlock(first); err != nil {
		t.Fatalf("the proposer must accept its own evidence block: %v", err)
	}
	// The peer re-executes it from its own state and must agree, then both commit.
	pair.commit(first)
	requireSlashedOnce("first block", evA, offenderA.address(), beforeA)

	// Later blocks neither slash again nor undo it.
	afterFirst := observe(proposer, offenderA.address())
	pair.mine()
	pair.mine()
	if again := observe(proposer, offenderA.address()); again.treasury.Cmp(afterFirst.treasury) != 0 || again.locked.Sign() != 0 || again.rewardPool.Cmp(afterFirst.rewardPool) != 0 {
		t.Fatalf("empty blocks after the slash moved it again: treasury %s -> %s, pool %s -> %s",
			afterFirst.treasury, again.treasury, afterFirst.rewardPool, again.rewardPool)
	}
	// The same report cannot be submitted a second time, so it cannot slash twice.
	dup := signedSubmitEvidenceTx(t, evA, reporterKey, 1)
	if err := proposer.AddTransaction(dup); !errors.Is(err, ErrEvidenceAlreadyRecorded) {
		t.Fatalf("a repeated report must be refused as already recorded, got %v", err)
	}

	// ---- second offender, after the first penalty was consumed -----------
	// The node that had already applied a penalty used to build its next
	// evidence block WITHOUT the slash, which a peer rejected.
	evB, _ := buildGenuineEquivocationEvidence(t, offenderB, reporterKey, 2)
	beforeB := observe(proposer, offenderB.address())
	txB := submit(evB, 1)
	second := pair.build()
	if !livenessBlockContains(t, second, txB) {
		t.Fatalf("the second evidence transaction must be in the block")
	}
	if err := proposer.ValidateBlock(second); err != nil {
		t.Fatalf("the proposer must accept its second evidence block: %v", err)
	}
	if err := validator.ValidateBlock(second); err != nil {
		t.Fatalf("the peer must accept the second evidence block: %v", err)
	}
	pair.commit(second)
	requireSlashedOnce("second block", evB, offenderB.address(), beforeB)
	// The first offender was not touched again.
	if again := observe(proposer, offenderA.address()); again.locked.Sign() != 0 {
		t.Fatalf("the first offender's stake changed again: %s", again.locked)
	}
}
