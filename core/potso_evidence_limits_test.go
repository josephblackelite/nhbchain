package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	nativecommon "nhbchain/native/common"
)

// TestClassifyProposalErrorEvidence pins how the block builder treats each way
// a TxTypeSubmitEvidence can fail. Every one is an ordinary per-transaction
// failure: prune when it can never succeed, skip when it might later, and never
// the default abort that would fail the whole block build.
func TestClassifyProposalErrorEvidence(t *testing.T) {
	validation := func(reason evidence.RejectReason) error {
		return fmt.Errorf("%w: %w", ErrInvalidTransaction, &evidence.ValidationError{Reason: reason, Message: string(reason)})
	}
	cases := []struct {
		name string
		err  error
		want proposalTxDisposition
	}{
		{"invalid payload", fmt.Errorf("apply: %w", ErrEvidenceInvalidPayload), proposalDispositionPrune},
		{"reporter is not the signer", ErrEvidenceReporterMismatch, proposalDispositionPrune},
		{"already recorded", ErrEvidenceAlreadyRecorded, proposalDispositionPrune},
		{"expired", validation(evidence.RejectReasonExpired), proposalDispositionPrune},
		{"bad signature", validation(evidence.RejectReasonInvalidSignature), proposalDispositionPrune},
		{"bad equivocation proof", validation(evidence.RejectReasonInvalidEquivocationProof), proposalDispositionPrune},
		{"oversized", validation(evidence.RejectReasonOversized), proposalDispositionPrune},
		{"negative timestamp", validation(evidence.RejectReasonInvalidTimestamp), proposalDispositionPrune},
		{"invalid type", validation(evidence.RejectReasonInvalidType), proposalDispositionPrune},
		{"height not reached yet", validation(evidence.RejectReasonFutureHeight), proposalDispositionSkip},
		{"reporter not bonded", ErrEvidenceReporterNotBonded, proposalDispositionSkip},
		{"index full", fmt.Errorf("potsoSubmitEvidence: persist record: %w", evidence.ErrIndexFull), proposalDispositionSkip},
		{"reporter quota", evidence.ErrReporterQuota, proposalDispositionSkip},
		{"module paused", nativecommon.ErrModulePaused, proposalDispositionSkip},
	}
	for _, tc := range cases {
		if got := classifyProposalError(tc.err); got != tc.want {
			t.Errorf("%s: classified as %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSubmitEvidenceIndexLimitsAreExact uses the exported limits: the index
// accepts exactly MaxPendingRecords reports, the next is refused with
// evidence.ErrIndexFull, and one reporter is refused at MaxRecordsPerReporter
// with evidence.ErrReporterQuota.
func TestSubmitEvidenceIndexLimitsAreExact(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	offender := 0
	submit := func(reporter *evidenceTestKey, nonce uint64) error {
		offender++
		tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(offender), 1), reporter, nonce)
		return applyEvidenceTx(t, node, 200, tx)
	}

	quota := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, quota.address())
	for i := 0; i < evidence.MaxRecordsPerReporter; i++ {
		if err := submit(quota, uint64(i)); err != nil {
			t.Fatalf("report %d of %d for one reporter: %v", i+1, evidence.MaxRecordsPerReporter, err)
		}
	}
	if err := submit(quota, uint64(evidence.MaxRecordsPerReporter)); !errors.Is(err, evidence.ErrReporterQuota) {
		t.Fatalf("expected evidence.ErrReporterQuota past the per-reporter limit, got %v", err)
	}

	stored := evidence.MaxRecordsPerReporter
	for stored < evidence.MaxPendingRecords {
		reporter := newEvidenceTestKey(t)
		seedEvidenceReporterBond(t, node, reporter.address())
		for i := 0; i < evidence.MaxRecordsPerReporter && stored < evidence.MaxPendingRecords; i++ {
			if err := submit(reporter, uint64(i)); err != nil {
				t.Fatalf("report %d of %d: %v", stored+1, evidence.MaxPendingRecords, err)
			}
			stored++
		}
	}
	if n := evidenceIndexSize(t, node); n != evidence.MaxPendingRecords {
		t.Fatalf("expected the index at exactly %d entries, got %d", evidence.MaxPendingRecords, n)
	}
	last := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, last.address())
	if err := submit(last, 0); !errors.Is(err, evidence.ErrIndexFull) {
		t.Fatalf("expected evidence.ErrIndexFull once the index is at its bound, got %v", err)
	}
}

// TestSubmitEvidenceFullIndexFreesRoomAsRecordsExpire: a full index does not stay
// full. Once its records have aged out of the evidence window, the very next
// submission prunes them and is accepted, even though no block-level pass has
// run in between.
func TestSubmitEvidenceFullIndexFreesRoomAsRecordsExpire(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	const recordedAt = 200
	offender := 0
	for stored := 0; stored < evidence.MaxPendingRecords; {
		reporter := newEvidenceTestKey(t)
		seedEvidenceReporterBond(t, node, reporter.address())
		for i := 0; i < evidence.MaxRecordsPerReporter && stored < evidence.MaxPendingRecords; i++ {
			offender++
			tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(offender), 100), reporter, uint64(i))
			if err := applyEvidenceTx(t, node, recordedAt, tx); err != nil {
				t.Fatalf("fill the index: %v", err)
			}
			stored++
		}
	}
	fresh := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, fresh.address())
	tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, fresh, distinctOffender(9_000), 100), fresh, 0)
	if err := applyEvidenceTx(t, node, recordedAt, tx); !errors.Is(err, evidence.ErrIndexFull) {
		t.Fatalf("expected a full index to refuse a new report while its records are live, got %v", err)
	}

	// Height 100 is now one block outside the window: every stored record is
	// expired, though none has been pruned yet.
	later := uint64(100) + evidence.DefaultMaxAgeBlocks + 1
	tx = signedSubmitEvidenceTx(t, downtimeEvidence(t, fresh, distinctOffender(9_001), later), fresh, 0)
	if err := applyEvidenceTx(t, node, later, tx); err != nil {
		t.Fatalf("expected the expired records to make room for a new report: %v", err)
	}
	if n := evidenceIndexSize(t, node); n != 1 {
		t.Fatalf("expected only the new report left in the index, got %d", n)
	}
}

// TestSubmitEvidencePayloadBounds: a record is stored for its whole retention
// window, so its size is bounded. A report exactly at the limits is accepted and
// one past them is refused with a validation error (never a storage failure).
func TestSubmitEvidencePayloadBounds(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	nonce := uint64(0)

	build := func(offender int, heights []uint64, details []byte) evidence.Evidence {
		ev := evidence.Evidence{
			Type:      evidence.TypeDowntime,
			Offender:  distinctOffender(offender),
			Heights:   heights,
			Details:   details,
			Reporter:  reporter.address(),
			Timestamp: 1,
		}
		hash, err := ev.CanonicalHash()
		if err != nil {
			t.Fatalf("canonical hash: %v", err)
		}
		sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporter.priv)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		ev.ReporterSig = sig
		return ev
	}
	ascending := func(n int) []uint64 {
		heights := make([]uint64, n)
		for i := range heights {
			heights[i] = uint64(i + 1)
		}
		return heights
	}
	expect := func(name string, ev evidence.Evidence, want evidence.RejectReason) {
		t.Helper()
		err := applyEvidenceTx(t, node, 200, signedSubmitEvidenceTx(t, ev, reporter, nonce))
		if want == "" {
			if err != nil {
				t.Fatalf("%s: expected acceptance, got %v", name, err)
			}
			nonce++
			return
		}
		var verr *evidence.ValidationError
		if !errors.As(err, &verr) || verr.Reason != want {
			t.Fatalf("%s: expected a %q validation error, got %v", name, want, err)
		}
	}

	expect("max heights", build(1, ascending(evidence.MaxHeightsPerEvidence), nil), "")
	expect("too many heights", build(2, ascending(evidence.MaxHeightsPerEvidence+1), nil), evidence.RejectReasonOversized)
	expect("max details", build(3, []uint64{1}, bytes.Repeat([]byte{'a'}, evidence.MaxDetailsBytes)), "")
	expect("details too long", build(4, []uint64{1}, bytes.Repeat([]byte{'a'}, evidence.MaxDetailsBytes+1)), evidence.RejectReasonOversized)
}

// TestSubmitEvidenceNegativeTimestampIsAValidationError: the timestamp is signed
// as an int64 but stored as a uint64, so a payload whose value does not fit an
// int64 decodes to a negative number that could never be stored. It must be
// refused up front as an ordinary validation failure -- an unclassified storage
// error at that point would abort the block build instead.
func TestSubmitEvidenceNegativeTimestampIsAValidationError(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())

	wire := struct {
		Type        string
		Offender    [20]byte
		Heights     []uint64
		Details     []byte
		Reporter    [20]byte
		ReporterSig []byte
		Timestamp   uint64
	}{
		Type:        string(evidence.TypeDowntime),
		Offender:    distinctOffender(1),
		Heights:     []uint64{1},
		Reporter:    reporter.address(),
		ReporterSig: make([]byte, 65),
		Timestamp:   1 << 63,
	}
	payload, err := rlp.EncodeToBytes(&wire)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	tx := signedSubmitEvidenceTx(t, evidence.Evidence{}, reporter, 0)
	tx.Data = payload
	if err := tx.Sign(reporter.priv); err != nil {
		t.Fatalf("re-sign: %v", err)
	}

	var verr *evidence.ValidationError
	err = applyEvidenceTx(t, node, 200, tx)
	if !errors.As(err, &verr) || verr.Reason != evidence.RejectReasonInvalidTimestamp {
		t.Fatalf("expected an %q validation error, got %v", evidence.RejectReasonInvalidTimestamp, err)
	}
	if got := classifyProposalError(err); got != proposalDispositionPrune {
		t.Fatalf("expected the block builder to prune it, got %v", got)
	}
}

// TestSubmitEvidenceReporterQuotaFreesAsRecordsExpire: the per-reporter quota is
// counted against live records, so once a reporter's records age out of the
// window they can report again.
func TestSubmitEvidenceReporterQuotaFreesAsRecordsExpire(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	reporter := newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	nonce := uint64(0)
	for i := 0; i < evidence.MaxRecordsPerReporter; i++ {
		tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(i+1), 100), reporter, nonce)
		if err := applyEvidenceTx(t, node, 200, tx); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
		nonce++
	}
	later := uint64(100) + evidence.DefaultMaxAgeBlocks + 1
	tx := signedSubmitEvidenceTx(t, downtimeEvidence(t, reporter, distinctOffender(99), later), reporter, nonce)
	if err := applyEvidenceTx(t, node, later, tx); err != nil {
		t.Fatalf("expected the reporter's expired records to stop counting against their quota: %v", err)
	}
}

// TestEvidenceSeveralReportsInOneBlockAgreeAcrossNodes: a block carrying two
// reports against the same offender and one against an account with nothing
// staked applies each penalty exactly once, in the same order on every node. The
// second report against the offender finds nothing left to slash; the report
// against the unstaked account slashes nothing. Both are still recorded as
// applied, so neither is reconsidered in a later block.
func TestEvidenceSeveralReportsInOneBlockAgreeAcrossNodes(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 500)
	f.stake(f.reporter, 10_000)
	nonce := f.nonce(f.reporter)
	adminBefore := f.account(f.proposer, f.admin)

	f.advance(
		f.evidenceTxAt(f.offender, f.reporter, 1, nonce),
		f.evidenceTxAt(f.offender, f.reporter, 2, nonce+1),
		f.evidenceTxAt(f.bystander, f.reporter, 2, nonce+2),
	)
	f.advance()

	if got := f.account(f.proposer, f.offender); got.stake.Sign() != 0 || got.locked.Sign() != 0 {
		t.Fatalf("expected the offender's stake slashed, got stake=%s locked=%s", got.stake, got.locked)
	}
	if got := f.account(f.proposer, f.bystander); got.balance.Cmp(evidenceZNHB(1_000)) != 0 {
		t.Fatalf("expected the unstaked account untouched, got balance=%s", got.balance)
	}
	adminAfter := f.account(f.proposer, f.admin)
	if got := new(big.Int).Sub(adminAfter.balance, adminBefore.balance); got.Cmp(evidenceZNHB(500)) != 0 {
		t.Fatalf("expected exactly the offender's 500 ZNHB forfeited once, got %s", got)
	}
	f.checkInvariant(f.proposer, "after the block of reports")

	f.proposer.stateMu.RLock()
	manager := nhbstate.NewManager(f.proposer.state.Trie)
	entries, err := manager.PotsoEvidenceIndex()
	if err != nil {
		f.proposer.stateMu.RUnlock()
		t.Fatalf("index: %v", err)
	}
	for _, entry := range entries {
		if applied, err := manager.PotsoPenaltyApplied(entry.Offense, entry.Offender); err != nil || !applied {
			f.proposer.stateMu.RUnlock()
			t.Fatalf("expected every recorded report marked applied: entry %x applied=%v err=%v", entry.Hash[:4], applied, err)
		}
	}
	f.proposer.stateMu.RUnlock()
	if len(entries) != 3 {
		t.Fatalf("expected the three reports in the index, got %d", len(entries))
	}

	fresh := f.newNode()
	for i, b := range f.chain {
		if err := fresh.CommitBlock(b); err != nil {
			t.Fatalf("fresh node replay of block %d (height %d) failed: %v", i+1, b.Header.Height, err)
		}
	}
	if fresh.state.PendingRoot() != f.proposer.state.PendingRoot() {
		t.Fatalf("fresh replay root differs from the proposer's")
	}
}
