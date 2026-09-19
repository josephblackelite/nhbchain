package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
)

// Evidence used to be a senderless, feeless, nonce-less transaction whose
// records were indexed forever: anyone who could gossip a transaction could
// grow the set, and the per-block work that walks it, without limit. These
// tests cover what replaced it -- an ordinary signed transaction from a bonded
// reporter, a hard bound on the index, and pruning by the evidence window.
//
// The bound values written as literals below (256 records in the index, 4 per
// reporter, 200 per listed page) are the documented limits, kept literal so the
// tests state the limits themselves rather than echoing whatever the constants
// currently say.

// downtimeEvidence builds a signed DOWNTIME report (which needs no proof, so a
// test can mint as many distinct ones as it likes) by reporter against offender
// at height.
func downtimeEvidence(t *testing.T, reporter *evidenceTestKey, offender [20]byte, height uint64) evidence.Evidence {
	t.Helper()
	ev := evidence.Evidence{
		Type:      evidence.TypeDowntime,
		Offender:  offender,
		Heights:   []uint64{height},
		Reporter:  reporter.address(),
		Timestamp: 1,
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporter.priv)
	if err != nil {
		t.Fatalf("sign evidence: %v", err)
	}
	ev.ReporterSig = sig
	return ev
}

// distinctOffender returns a non-zero address unique to n, so each evidence
// built against it has its own canonical hash.
func distinctOffender(n int) [20]byte {
	var addr [20]byte
	addr[0] = 0xEE
	addr[18] = byte(n >> 8)
	addr[19] = byte(n)
	return addr
}

// applyEvidenceTx executes tx against node's own pending state as a block at
// height, the way the existing evidence tests do.
func applyEvidenceTx(t *testing.T, node *Node, height uint64, tx *types.Transaction) error {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	node.state.BeginBlock(height, time.Unix(1_700_000_000, 0).UTC())
	defer node.state.EndBlock()
	return node.state.ApplyTransaction(tx)
}

func evidenceIndexSize(t *testing.T, node *Node) int {
	t.Helper()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	hashes, err := nhbstate.NewManager(node.state.Trie).PotsoEvidencePendingHashes()
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	return len(hashes)
}

func newEvidenceBoundsNode(t *testing.T) *Node {
	t.Helper()
	return buildSwapAdminTestNode(t, writeSwapAdminGenesis(t))
}

// TestSubmitEvidenceRejectsUnsignedTransaction: a senderless evidence
// transaction -- the shape anyone could gossip for free -- is refused at
// mempool admission and again at execution.
func TestSubmitEvidenceRejectsUnsignedTransaction(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())

	ev := downtimeEvidence(t, reporter, distinctOffender(1), 1)
	payload, err := encodeSubmitEvidenceTransaction(ev)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	unsigned := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSubmitEvidence,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	if err := node.AddTransaction(unsigned); err == nil {
		t.Fatalf("expected an unsigned evidence transaction to be refused at admission")
	}
	if len(node.GetMempool()) != 0 {
		t.Fatalf("expected nothing admitted to the mempool")
	}
	if err := applyEvidenceTx(t, node, 200, unsigned); err == nil {
		t.Fatalf("expected an unsigned evidence transaction to be refused at execution")
	}
	if n := evidenceIndexSize(t, node); n != 0 {
		t.Fatalf("expected no evidence recorded, got %d", n)
	}
}

// TestSubmitEvidenceRequiresBondedReporter: a validly signed report from an
// account with no bonded stake is refused; one with exactly the minimum
// validator stake bonded is accepted, and one unit less is not.
func TestSubmitEvidenceRequiresBondedReporter(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	minStake := governance.DefaultMinimumValidatorStake()

	setBond := func(reporter *evidenceTestKey, locked *big.Int) {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		manager := nhbstate.NewManager(node.state.Trie)
		addr := reporter.address()
		account, err := manager.GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load reporter: %v", err)
		}
		account.LockedZNHB = new(big.Int).Set(locked)
		if err := manager.PutAccount(addr[:], account); err != nil {
			t.Fatalf("seed bond: %v", err)
		}
	}

	stranger := newEvidenceTestKey(t)
	tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, stranger, distinctOffender(1), 1), stranger, 0)
	if err := applyEvidenceTx(t, node, 200, tx); err == nil {
		t.Fatalf("expected a reporter with nothing bonded to be refused")
	}

	short := newEvidenceTestKey(t)
	setBond(short, new(big.Int).Sub(minStake, big.NewInt(1)))
	tx = signedSubmitEvidenceTx(t, downtimeEvidence(t, short, distinctOffender(2), 1), short, 0)
	if err := applyEvidenceTx(t, node, 200, tx); err == nil {
		t.Fatalf("expected a reporter one unit below the minimum stake to be refused")
	}

	exact := newEvidenceTestKey(t)
	setBond(exact, minStake)
	tx = signedSubmitEvidenceTx(t, downtimeEvidence(t, exact, distinctOffender(3), 1), exact, 0)
	if err := applyEvidenceTx(t, node, 200, tx); err != nil {
		t.Fatalf("expected a reporter at exactly the minimum stake to be accepted: %v", err)
	}
	if n := evidenceIndexSize(t, node); n != 1 {
		t.Fatalf("expected exactly the one accepted report recorded, got %d", n)
	}
}

// TestSubmitEvidenceReporterMustBeSigner: the report's Reporter field must be
// the account that signed the transaction. A bonded account cannot lend its
// standing to a report carrying someone else's identity.
func TestSubmitEvidenceReporterMustBeSigner(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	signer := newEvidenceTestKey(t)
	named := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, signer.address())
	seedEvidenceReporterBond(t, node, named.address())

	ev := downtimeEvidence(t, named, distinctOffender(1), 1)
	tx := signedSubmitEvidenceTx(t, ev, signer, 0)
	if err := applyEvidenceTx(t, node, 200, tx); err == nil {
		t.Fatalf("expected a report naming a different reporter than the signer to be refused")
	}
	if n := evidenceIndexSize(t, node); n != 0 {
		t.Fatalf("expected nothing recorded, got %d", n)
	}
}

// TestSubmitEvidenceConsumesNonceAndRejectsReplay: a recorded report advances
// the reporter's nonce, so the same signed transaction cannot be replayed, and
// a transaction that skips ahead is refused.
func TestSubmitEvidenceConsumesNonceAndRejectsReplay(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())

	tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(1), 1), reporter, 0)
	if err := applyEvidenceTx(t, node, 200, tx); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	addr := reporter.address()
	account, err := node.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("load reporter: %v", err)
	}
	if account.Nonce != 1 {
		t.Fatalf("expected the reporter's nonce to advance to 1, got %d", account.Nonce)
	}
	if err := applyEvidenceTx(t, node, 200, tx); err == nil {
		t.Fatalf("expected the identical signed transaction to be refused on replay")
	}
	skip := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(2), 1), reporter, 5)
	if err := applyEvidenceTx(t, node, 200, skip); err == nil {
		t.Fatalf("expected a transaction that skips ahead of the account nonce to be refused")
	}
}

// TestSubmitEvidenceOneReporterCannotFillTheIndex: a single bonded reporter
// holds at most four live records, however many reports they sign.
func TestSubmitEvidenceOneReporterCannotFillTheIndex(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())

	accepted := 0
	for i := 0; i < 10; i++ {
		tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(i+1), 1), reporter, uint64(accepted))
		if err := applyEvidenceTx(t, node, 200, tx); err == nil {
			accepted++
		}
	}
	if accepted != 4 {
		t.Fatalf("expected one reporter to be limited to 4 live records, got %d", accepted)
	}
	if n := evidenceIndexSize(t, node); n != 4 {
		t.Fatalf("expected 4 records in the index, got %d", n)
	}
}

// TestSubmitEvidenceIndexIsHardBounded: eighty bonded reporters each try to add
// four reports (320 in all); the index stops at its hard bound of 256, and every
// report past that is refused rather than stored.
func TestSubmitEvidenceIndexIsHardBounded(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	const reporters, perReporter, bound = 80, 4, 256

	accepted, refused := 0, 0
	offender := 0
	for r := 0; r < reporters; r++ {
		reporter := newEvidenceTestKey(t)
		seedEvidenceReporterBond(t, node, reporter.address())
		nonce := uint64(0)
		for i := 0; i < perReporter; i++ {
			offender++
			tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(offender), 1), reporter, nonce)
			if err := applyEvidenceTx(t, node, 200, tx); err != nil {
				refused++
				continue
			}
			accepted++
			nonce++
		}
	}
	if accepted != bound {
		t.Fatalf("expected exactly %d reports accepted, got %d (refused %d)", bound, accepted, refused)
	}
	if n := evidenceIndexSize(t, node); n != bound {
		t.Fatalf("expected the index to hold exactly %d entries, got %d", bound, n)
	}
}

// TestEvidenceIsPrunedOnceOutsideTheWindow: the per-block evidence pass drops
// records whose oldest referenced height has aged out of the evidence window --
// the same window that stops an old report being submitted -- and keeps a
// record that is exactly on the boundary.
func TestEvidenceIsPrunedOnceOutsideTheWindow(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	const window = evidence.DefaultMaxAgeBlocks
	const recordedAt = window + 100
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())

	// Referencing height 100: falls out of the window at block 100+window+1.
	old := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(1), 100), reporter, 0)
	// Referencing height 101: falls out one block later.
	next := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(2), 101), reporter, 1)
	for _, tx := range []*types.Transaction{old, next} {
		if err := applyEvidenceTx(t, node, recordedAt, tx); err != nil {
			t.Fatalf("record evidence: %v", err)
		}
	}
	if n := evidenceIndexSize(t, node); n != 2 {
		t.Fatalf("expected both reports recorded, got %d", n)
	}

	process := func(height uint64) {
		t.Helper()
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		node.state.BeginBlock(height, time.Unix(1_700_000_000, 0).UTC())
		defer node.state.EndBlock()
		if err := node.processPendingEvidenceForState(node.state, height); err != nil {
			t.Fatalf("process evidence at %d: %v", height, err)
		}
	}
	process(100 + window) // height 100 is exactly `window` blocks old: still in the window
	if n := evidenceIndexSize(t, node); n != 2 {
		t.Fatalf("expected both records kept on the boundary, got %d", n)
	}
	process(100 + window + 1) // now height 100 is one block too old
	if n := evidenceIndexSize(t, node); n != 1 {
		t.Fatalf("expected the expired record pruned, leaving 1, got %d", n)
	}
	process(101 + window + 1)
	if n := evidenceIndexSize(t, node); n != 0 {
		t.Fatalf("expected every record pruned, got %d", n)
	}
}

// TestEvidencePassWithNothingRecordedChangesNothing: the block-lifecycle
// evidence pass runs on every block, including every block already on the live
// chain, where no evidence has ever been recorded. With an empty evidence set it
// must leave the state root untouched and emit nothing, whatever the height.
func TestEvidencePassWithNothingRecordedChangesNothing(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	for _, height := range []uint64{0, 1, 2, evidence.DefaultMaxAgeBlocks, evidence.DefaultMaxAgeBlocks + 1, 196_693, 1 << 40} {
		node.stateMu.Lock()
		node.state.BeginBlock(height, time.Unix(1_700_000_000, 0).UTC())
		before := node.state.PendingRoot()
		eventsBefore := len(node.state.events)
		err := node.processPendingEvidenceForState(node.state, height)
		after := node.state.PendingRoot()
		eventsAfter := len(node.state.events)
		node.state.EndBlock()
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("height %d: %v", height, err)
		}
		if before != after {
			t.Fatalf("height %d: an empty evidence pass moved the state root %s -> %s", height, before, after)
		}
		if eventsBefore != eventsAfter {
			t.Fatalf("height %d: an empty evidence pass emitted events", height)
		}
	}
}

// TestPotsoEvidenceListPageIsBounded: a caller cannot ask for an unbounded page.
func TestPotsoEvidenceListPageIsBounded(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	for i := 0; i < 250; i++ {
		offender := distinctOffender(i + 1)
		ev := evidence.Evidence{
			Type:     evidence.TypeDowntime,
			Offender: offender,
			Heights:  []uint64{uint64(i + 1)},
			Reporter: distinctOffender(1000 + i),
		}
		hash, err := ev.CanonicalHash()
		if err != nil {
			node.stateMu.Unlock()
			t.Fatalf("canonical hash: %v", err)
		}
		if err := manager.PotsoEvidencePutRecord(&evidence.Record{Hash: hash, Evidence: ev, ReceivedAt: 1}); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("seed record %d: %v", i, err)
		}
	}
	node.stateMu.Unlock()

	records, next, err := node.PotsoEvidenceList(evidence.Filter{Limit: 1_000_000})
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	if len(records) != 200 {
		t.Fatalf("expected one page to be capped at 200 records, got %d", len(records))
	}
	if next != 200 {
		t.Fatalf("expected the next page to start at offset 200, got %d", next)
	}
}

// TestCreateBlockDoesNotAbortOnBadEvidence: a report that cannot be applied is
// an ordinary per-transaction failure. Building a block around it must exclude
// just that transaction, never fail the whole build -- otherwise anyone who can
// get one such transaction in front of a proposer stops it from proposing.
func TestCreateBlockDoesNotAbortOnBadEvidence(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.reporter, 10_000)
	f.advance(f.evidenceTx(f.offender, f.reporter, 1)) // recorded: a later copy is a duplicate

	reporterKey := &evidenceTestKey{priv: f.reporter.PrivateKey}
	offenderKey := &evidenceTestKey{priv: f.offender.PrivateKey}
	strangerKey := newEvidenceTestKey(t)
	nonce := f.nonce(f.reporter)

	// Undecodable payload, from a bonded reporter.
	garbage := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeSubmitEvidence, Nonce: nonce,
		Data: []byte{0xde, 0xad, 0xbe, 0xef}, GasLimit: 21_000, GasPrice: big.NewInt(1),
	}
	if err := garbage.Sign(f.reporter.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Reporter field names someone other than the signer.
	other, _ := buildGenuineEquivocationEvidence(t, offenderKey, strangerKey, 2)
	mismatch := signedSubmitEvidenceTx(t, other, reporterKey, nonce)
	// A copy of the report already recorded.
	dup, _ := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, 1)
	duplicate := signedSubmitEvidenceTx(t, dup, reporterKey, nonce)
	// Valid report, but from an account with nothing bonded.
	unbonded, _ := buildGenuineEquivocationEvidence(t, offenderKey, strangerKey, 3)
	notBonded := signedSubmitEvidenceTx(t, unbonded, strangerKey, 0)
	// One good report, to prove the rest of the block is unaffected.
	good, _ := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, 3)
	valid := signedSubmitEvidenceTx(t, good, reporterKey, nonce)

	block, err := f.proposer.CreateBlock([]*types.Transaction{garbage, mismatch, duplicate, notBonded, valid})
	if err != nil {
		t.Fatalf("expected the block to build around the bad evidence, got: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected only the valid report in the block, got %d transactions", len(block.Transactions))
	}
	hash, _ := valid.Hash()
	got, _ := block.Transactions[0].Hash()
	if string(hash) != string(got) {
		t.Fatalf("the transaction that survived is not the valid report")
	}
}
