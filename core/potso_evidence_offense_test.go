package core

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// One double-sign is one offense. A validator that signs two conflicting votes at
// one height, round and vote type is answerable for that once, however many ways a
// report of it can be written down: every one of them has its own canonical hash
// (the hash covers the heights a reporter lists and the exact bytes of the proof),
// so a chain that decided "already recorded" and "already penalised" by the hash
// slashed the same double-sign again, for whatever the offender had bonded since,
// each time a bonded reporter chose to write it differently -- and, because the
// proof was never tied to the heights the report lists, for as long as the chain
// ran. These tests pin the rules that replaced that: an offense is recorded and
// penalised once, for as long as a report of it could still be submitted.

// rewriting is one way of writing the same double-sign down. It changes the proof
// and/or the heights the report lists and returns the bytes to carry as Details
// (nil to encode the changed proof as ordinary JSON).
type rewriting struct {
	name    string
	rewrite func(t *testing.T, offender *evidenceTestKey, proof *evidence.EquivocationProof, ev *evidence.Evidence) []byte
}

func upperHex(value string) string {
	return "0X" + strings.ToUpper(strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X"))
}

// malleatedSignature returns the other valid encoding of a signature: (r, n-s) with
// the recovery id flipped recovers the same key from the same digest.
func malleatedSignature(t *testing.T, value string) string {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(raw) != 65 {
		t.Fatalf("bad signature %q: %v", value, err)
	}
	s := new(big.Int).SetBytes(raw[32:64])
	s.Sub(ethcrypto.S256().Params().N, s)
	out := make([]byte, 65)
	copy(out[:32], raw[:32])
	s.FillBytes(out[32:64])
	out[64] = raw[64] ^ 1
	return "0x" + hex.EncodeToString(out)
}

// rewritingsOfOneDoubleSign are ways to write the report of one double-sign
// differently, each of which is a report the chain would have accepted as a first
// one when hashes alone told reports apart.
func rewritingsOfOneDoubleSign() []rewriting {
	return []rewriting{
		{"the two votes swapped", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, _ *evidence.Evidence) []byte {
			p.VoteA, p.VoteB = p.VoteB, p.VoteA
			return nil
		}},
		{"hex in upper case", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, _ *evidence.Evidence) []byte {
			p.VoteA.BlockHash, p.VoteA.Signature = upperHex(p.VoteA.BlockHash), upperHex(p.VoteA.Signature)
			p.VoteB.BlockHash, p.VoteB.Signature = upperHex(p.VoteB.BlockHash), upperHex(p.VoteB.Signature)
			return nil
		}},
		{"the JSON laid out differently", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, _ *evidence.Evidence) []byte {
			details, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			return details
		}},
		{"a signature in its other valid encoding", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, _ *evidence.Evidence) []byte {
			p.VoteA.Signature = malleatedSignature(t, p.VoteA.Signature)
			return nil
		}},
		{"a third conflicting vote in place of one", func(t *testing.T, offender *evidenceTestKey, p *evidence.EquivocationProof, _ *evidence.Evidence) []byte {
			p.VoteB = offender.signVote([]byte{0x03}, p.Round, p.VoteType, p.Height)
			return nil
		}},
		{"another height listed as well", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, ev *evidence.Evidence) []byte {
			ev.Heights = []uint64{p.Height, p.Height + 1}
			return nil
		}},
		{"an older height listed as well", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, ev *evidence.Evidence) []byte {
			ev.Heights = []uint64{0, p.Height}
			return nil
		}},
		{"another timestamp", func(t *testing.T, _ *evidenceTestKey, p *evidence.EquivocationProof, ev *evidence.Evidence) []byte {
			ev.Timestamp = 7_777
			return nil
		}},
	}
}

// rewrittenEvidence returns a report signed by reporter of the same double-sign as
// genuine, written the way rw says.
func rewrittenEvidence(t *testing.T, genuine evidence.Evidence, offender, reporter *evidenceTestKey, rw rewriting) evidence.Evidence {
	t.Helper()
	var proof evidence.EquivocationProof
	if err := json.Unmarshal(genuine.Details, &proof); err != nil {
		t.Fatalf("decode the genuine proof: %v", err)
	}
	ev := genuine.Clone()
	details := rw.rewrite(t, offender, &proof, &ev)
	if details == nil {
		var err error
		if details, err = json.Marshal(&proof); err != nil {
			t.Fatalf("marshal proof: %v", err)
		}
	}
	ev.Details = details
	return signedReport(t, ev, reporter)
}

// signedReport (re)signs ev as reporter over its canonical hash.
func signedReport(t *testing.T, ev evidence.Evidence, reporter *evidenceTestKey) evidence.Evidence {
	t.Helper()
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporter.priv)
	if err != nil {
		t.Fatalf("sign report: %v", err)
	}
	ev.ReporterSig = sig
	return ev
}

// doubleSignEvidence is the report of the offender's genuine double-sign at the
// decision point, listing exactly the proof's height.
func doubleSignEvidence(t *testing.T, offender, reporter *evidenceTestKey, height uint64, round int, voteType evidence.EquivocationVoteType) evidence.Evidence {
	t.Helper()
	proof := &evidence.EquivocationProof{
		Height:   height,
		Round:    round,
		VoteType: voteType,
		VoteA:    offender.signVote([]byte{0x01}, round, voteType, height),
		VoteB:    offender.signVote([]byte{0x02}, round, voteType, height),
	}
	ev, _ := buildAndSignEvidenceTx(t, offender.address(), reporter, proof, height)
	return ev
}

// executeOnScratchState applies tx as the next block on a private copy of node's
// state, leaving the node untouched, and returns what execution said.
func executeOnScratchState(t *testing.T, node *Node, tx *types.Transaction) error {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	scratch, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	scratch.BeginBlock(node.GetHeight()+1, time.Unix(1_800_000_000, 0).UTC())
	defer scratch.EndBlock()
	return scratch.ApplyTransaction(tx)
}

// TestEvidenceOneDoubleSignIsSlashedOnceHoweverItIsReportedAgain is the direct
// regression for the double slash: the offender is slashed for a genuine
// double-sign and bonds again; the same double-sign is then reported, by a bonded
// reporter, written another way that changes its hash. The report is not admitted,
// built into a block or executed, and the new stake is untouched on the proposer,
// on its peer and on a node that replays the chain from scratch. Each way of
// writing the report down is checked on a chain of its own.
func TestEvidenceOneDoubleSignIsSlashedOnceHoweverItIsReportedAgain(t *testing.T) {
	for _, rw := range rewritingsOfOneDoubleSign() {
		rw := rw
		t.Run(rw.name, func(t *testing.T) {
			f := newEvidenceFlow(t).bootstrapped()
			f.stake(f.offender, 500)
			f.stake(f.reporter, 10_000)
			offenderKey := &evidenceTestKey{priv: f.offender.PrivateKey}
			reporterKey := &evidenceTestKey{priv: f.reporter.PrivateKey}

			genuine := doubleSignEvidence(t, offenderKey, reporterKey, 1, 1, evidence.EquivocationVotePrevote)
			genuineHash, err := genuine.CanonicalHash()
			if err != nil {
				t.Fatalf("canonical hash: %v", err)
			}
			f.advance(signedSubmitEvidenceTx(t, genuine, reporterKey, f.nonce(f.reporter)))
			if got := f.account(f.proposer, f.offender); got.locked.Sign() != 0 {
				t.Fatalf("expected the genuine double-sign slashed, got locked=%s", got.locked)
			}
			f.stake(f.offender, 250)
			f.advance()

			rewritten := rewrittenEvidence(t, genuine, offenderKey, reporterKey, rw)
			hash, err := rewritten.CanonicalHash()
			if err != nil {
				t.Fatalf("canonical hash: %v", err)
			}
			if hash == genuineHash && rw.name != "another timestamp" {
				t.Fatalf("test premise: the rewritten report must have another canonical hash")
			}
			tx := signedSubmitEvidenceTx(t, rewritten, reporterKey, f.nonce(f.reporter))

			if err := f.proposer.AddTransaction(tx); !errors.Is(err, ErrEvidenceAlreadyRecorded) {
				t.Fatalf("expected admission to refuse a second report of the same offense as already recorded, got %v", err)
			}
			if err := f.peer.AddTransaction(tx); !errors.Is(err, ErrEvidenceAlreadyRecorded) {
				t.Fatalf("expected the peer to refuse it too, got %v", err)
			}
			// Executed anyway (a proposer that skipped admission, a block from a
			// peer): refused, and as a report that can never succeed, so the builder
			// drops it.
			err = executeOnScratchState(t, f.proposer, tx)
			if !errors.Is(err, ErrEvidenceAlreadyRecorded) {
				t.Fatalf("expected execution to refuse it as already recorded, got %v", err)
			}
			if got := classifyProposalError(err); got != proposalDispositionPrune {
				t.Fatalf("expected the block builder to prune it, got %v", got)
			}
			block, err := f.proposer.CreateBlock([]*types.Transaction{tx})
			if err != nil {
				t.Fatalf("a block must build around the refused report: %v", err)
			}
			if len(block.Transactions) != 0 {
				t.Fatalf("expected the refused report left out of the block, got %d transactions", len(block.Transactions))
			}
			f.advance()
			f.advance()

			fresh := f.newNode()
			for i, b := range f.chain {
				if err := fresh.CommitBlock(b); err != nil {
					t.Fatalf("fresh node replay of block %d (height %d) failed: %v", i+1, b.Header.Height, err)
				}
			}
			for name, node := range map[string]*Node{"proposer": f.proposer, "peer": f.peer, "fresh replay": fresh} {
				if got := f.account(node, f.offender); got.locked.Cmp(evidenceZNHB(250)) != 0 {
					t.Fatalf("%s: the re-bonded stake must be untouched by a report of a double-sign already penalised, got locked=%s", name, got.locked)
				}
			}
			if fresh.state.PendingRoot() != f.proposer.state.PendingRoot() || f.peer.state.PendingRoot() != f.proposer.state.PendingRoot() {
				t.Fatalf("the nodes disagree on the state root")
			}
			f.checkInvariant(f.proposer, "after the refused report")
		})
	}
}

// TestEvidenceRewrittenReportOfAPenalisedDoubleSignIsRefusedAtExecution is the
// state-level twin of the test above, run through the block sequence one node
// executes: recorded and penalised in one block, stake bonded again, the same
// double-sign reported another way in the next -- refused, and the stake
// untouched. Each way of writing the report down is checked on a node of its own.
func TestEvidenceRewrittenReportOfAPenalisedDoubleSignIsRefusedAtExecution(t *testing.T) {
	for _, rw := range rewritingsOfOneDoubleSign() {
		rw := rw
		t.Run(rw.name, func(t *testing.T) {
			node := newEvidenceBoundsNode(t)
			offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
			seedEvidenceReporterBond(t, node, reporter.address())
			const stake = 1_000

			locked := func() *big.Int {
				t.Helper()
				addr := offender.address()
				account, err := node.GetAccount(addr[:])
				if err != nil {
					t.Fatalf("load offender: %v", err)
				}
				return new(big.Int).Set(account.LockedZNHB)
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

			seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(stake))
			genuine := doubleSignEvidence(t, offender, reporter, 100, 2, evidence.EquivocationVotePrecommit)
			if err := applyEvidenceTx(t, node, 200, signedSubmitEvidenceTx(t, genuine, reporter, 0)); err != nil {
				t.Fatalf("record the genuine report: %v", err)
			}
			process(200)
			if got := locked(); got.Sign() != 0 {
				t.Fatalf("expected the double-sign penalised, got locked=%s", got)
			}

			seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(stake))
			rewritten := rewrittenEvidence(t, genuine, offender, reporter, rw)
			err := applyEvidenceTx(t, node, 201, signedSubmitEvidenceTx(t, rewritten, reporter, 1))
			if !errors.Is(err, ErrEvidenceAlreadyRecorded) {
				t.Fatalf("expected the report to be refused as already recorded, got %v", err)
			}
			process(201)
			if got := locked(); got.Cmp(big.NewInt(stake)) != 0 {
				t.Fatalf("the re-bonded stake was slashed again: locked=%s", got)
			}
			if n := evidenceIndexSize(t, node); n != 1 {
				t.Fatalf("expected the offense held once, got %d records", n)
			}
		})
	}
}

// TestEvidenceTwoReportsOfOneDoubleSignInOneBlockSlashOnce: the second of two
// reports of one offense in the same block is refused as soon as the first is
// recorded, without waiting for a block boundary. The builder keeps the first and
// drops the second, and the offender is slashed exactly once on every node.
func TestEvidenceTwoReportsOfOneDoubleSignInOneBlockSlashOnce(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 500)
	f.stake(f.reporter, 10_000)
	offenderKey := &evidenceTestKey{priv: f.offender.PrivateKey}
	reporterKey := &evidenceTestKey{priv: f.reporter.PrivateKey}
	genuine := doubleSignEvidence(t, offenderKey, reporterKey, 1, 1, evidence.EquivocationVotePrevote)
	swapped := rewrittenEvidence(t, genuine, offenderKey, reporterKey, rewritingsOfOneDoubleSign()[0])
	nonce := f.nonce(f.reporter)
	first := signedSubmitEvidenceTx(t, genuine, reporterKey, nonce)
	second := signedSubmitEvidenceTx(t, swapped, reporterKey, nonce+1)

	// Executed one after the other in one block, the second is refused.
	f.proposer.stateMu.Lock()
	scratch, err := f.proposer.state.Copy()
	if err != nil {
		f.proposer.stateMu.Unlock()
		t.Fatalf("copy state: %v", err)
	}
	scratch.BeginBlock(f.proposer.GetHeight()+1, time.Unix(1_800_000_000, 0).UTC())
	firstErr := scratch.ApplyTransaction(first)
	secondErr := scratch.ApplyTransaction(second)
	scratch.EndBlock()
	f.proposer.stateMu.Unlock()
	if firstErr != nil {
		t.Fatalf("the first report must apply: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrEvidenceAlreadyRecorded) {
		t.Fatalf("expected the second report of the offense in the same block to be refused as already recorded, got %v", secondErr)
	}

	adminBefore := f.account(f.proposer, f.admin)
	block, err := f.proposer.CreateBlock([]*types.Transaction{first, second})
	if err != nil {
		t.Fatalf("a block must build around the second report: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the builder to keep the first report and drop the second, got %d transactions", len(block.Transactions))
	}
	commitBlockOnBoth(t, f.proposer, f.peer, block)
	f.chain = append(f.chain, block)
	for name, node := range map[string]*Node{"proposer": f.proposer, "peer": f.peer} {
		if got := f.account(node, f.offender); got.locked.Sign() != 0 {
			t.Fatalf("%s: expected the double-sign slashed, got locked=%s", name, got.locked)
		}
	}
	adminAfter := f.account(f.proposer, f.admin)
	if got := new(big.Int).Sub(adminAfter.balance, adminBefore.balance); got.Cmp(evidenceZNHB(500)) != 0 {
		t.Fatalf("expected exactly the offender's 500 ZNHB forfeited once, got %s", got)
	}
	f.checkInvariant(f.proposer, "after the block")
}

// TestEvidenceOffenseIsRememberedUntilItsHeightLeavesTheWindow: the record of an
// offense is kept, and a second report of it refused, for exactly as long as a
// report of it can still be submitted -- while the height of the double-sign is in
// the evidence window -- even when the first report also listed an older height
// that has since expired. After that a report of it is refused as expired, so it is
// never penalised twice, and nothing of it is left in state.
func TestEvidenceOffenseIsRememberedUntilItsHeightLeavesTheWindow(t *testing.T) {
	const window = evidence.DefaultMaxAgeBlocks
	const offenseHeight = 100
	const olderHeight = 60
	node := newEvidenceBoundsNode(t)
	offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(1_000))

	locked := func() *big.Int {
		t.Helper()
		addr := offender.address()
		account, err := node.GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load offender: %v", err)
		}
		return new(big.Int).Set(account.LockedZNHB)
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

	// The first report lists an older height beside the double-sign's own. It is
	// recorded at the last block at which that older height is still in the window.
	first := doubleSignEvidence(t, offender, reporter, offenseHeight, 1, evidence.EquivocationVotePrevote)
	first.Heights = []uint64{olderHeight, offenseHeight}
	first = signedReport(t, first, reporter)
	recordedAt := uint64(olderHeight) + window
	if err := applyEvidenceTx(t, node, recordedAt, signedSubmitEvidenceTx(t, first, reporter, 0)); err != nil {
		t.Fatalf("record the first report: %v", err)
	}
	process(recordedAt)
	if got := locked(); got.Sign() != 0 {
		t.Fatalf("expected the double-sign penalised, got locked=%s", got)
	}
	seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(1_000))

	rewrite := func() evidence.Evidence {
		ev := doubleSignEvidence(t, offender, reporter, offenseHeight, 1, evidence.EquivocationVotePrevote)
		ev.Details = append([]byte(nil), ev.Details...)
		var proof evidence.EquivocationProof
		if err := json.Unmarshal(ev.Details, &proof); err != nil {
			t.Fatalf("decode proof: %v", err)
		}
		proof.VoteA, proof.VoteB = proof.VoteB, proof.VoteA
		details, err := json.Marshal(&proof)
		if err != nil {
			t.Fatalf("marshal proof: %v", err)
		}
		ev.Details = details
		return signedReport(t, ev, reporter)
	}

	// One block on, the older height has left the window and the double-sign's own
	// has not: the report of the offense is still a duplicate. A record measured
	// from the oldest height listed would be gone by now, and the offense open to
	// a fresh report and a second slash.
	stillListed := recordedAt + 1
	process(stillListed)
	if n := evidenceIndexSize(t, node); n != 1 {
		t.Fatalf("expected the record kept while its offense is in the window, got %d records", n)
	}
	err := applyEvidenceTx(t, node, stillListed, signedSubmitEvidenceTx(t, rewrite(), reporter, 1))
	if !errors.Is(err, ErrEvidenceAlreadyRecorded) {
		t.Fatalf("expected the same offense to be refused as already recorded, got %v", err)
	}
	process(stillListed)
	if got := locked(); got.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("the re-bonded stake was slashed a second time: locked=%s", got)
	}

	// On the last block that the offense's height is inside the window.
	lastInWindow := uint64(offenseHeight) + window
	process(lastInWindow)
	if n := evidenceIndexSize(t, node); n != 1 {
		t.Fatalf("expected the record kept on the boundary, got %d records", n)
	}
	if err := applyEvidenceTx(t, node, lastInWindow, signedSubmitEvidenceTx(t, rewrite(), reporter, 1)); !errors.Is(err, ErrEvidenceAlreadyRecorded) {
		t.Fatalf("expected the same offense to still be refused as already recorded on the boundary, got %v", err)
	}

	// One block later the offense can no longer be reported at all.
	expired := lastInWindow + 1
	err = applyEvidenceTx(t, node, expired, signedSubmitEvidenceTx(t, rewrite(), reporter, 1))
	var verr *evidence.ValidationError
	if !errors.As(err, &verr) || verr.Reason != evidence.RejectReasonExpired {
		t.Fatalf("expected the report to be refused as expired, got %v", err)
	}
	if got := classifyProposalError(err); got != proposalDispositionPrune {
		t.Fatalf("expected the builder to prune it, got %v", got)
	}
	process(expired)
	if n := evidenceIndexSize(t, node); n != 0 {
		t.Fatalf("expected the record pruned once its offense left the window, got %d records", n)
	}
	if got := locked(); got.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("the re-bonded stake was slashed after the offense expired: locked=%s", got)
	}
	// Listing a height in today's window does not carry the old proof: it is not
	// one of the heights the proof is about.
	carried := rewrite()
	carried.Heights = []uint64{expired}
	err = applyEvidenceTx(t, node, expired, signedSubmitEvidenceTx(t, signedReport(t, carried, reporter), reporter, 1))
	if !errors.As(err, &verr) || verr.Reason != evidence.RejectReasonInvalidEquivocationProof {
		t.Fatalf("expected a proof for an expired height carried by a current one to be refused as an invalid proof, got %v", err)
	}
	if got := classifyProposalError(err); got != proposalDispositionPrune {
		t.Fatalf("expected the builder to prune it, got %v", got)
	}
}

// TestEvidenceProofMustBeForAHeightTheReportListsAndTheChainHasReached: a proof is
// about the height it names, so the report has to list that height and it cannot lie
// beyond the block being applied. Before this, the window checks only looked at the
// heights the report listed, which the reporter picks: a genuine double-sign of any
// age -- or votes for a height the chain never reached -- rode on a report listing
// the current height, was accepted and recorded, and slashed the offender.
func TestEvidenceProofMustBeForAHeightTheReportListsAndTheChainHasReached(t *testing.T) {
	const applyAt = 500
	offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
	cases := []struct {
		name        string
		proofHeight uint64
		heights     []uint64
	}{
		{"an old double-sign carried by the current height", 3, []uint64{applyAt}},
		{"an old double-sign carried by heights around today", 3, []uint64{applyAt - 1, applyAt}},
		{"votes for a height that was never reached, carried by the current height", 1_000_000_000, []uint64{applyAt}},
		{"votes for a height that was never reached, listed honestly", 1_000_000_000, []uint64{1_000_000_000}},
		{"votes for the next height, listed honestly", applyAt + 1, []uint64{applyAt + 1}},
		{"a height the report leaves out", 40, []uint64{39, 41}},
	}
	for _, tc := range cases {
		node := newEvidenceBoundsNode(t)
		seedEvidenceReporterBond(t, node, reporter.address())
		seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(1_000))
		ev := doubleSignEvidence(t, offender, reporter, tc.proofHeight, 7, evidence.EquivocationVotePrecommit)
		ev.Heights = tc.heights
		ev = signedReport(t, ev, reporter)

		err := applyEvidenceTx(t, node, applyAt, signedSubmitEvidenceTx(t, ev, reporter, 0))
		var verr *evidence.ValidationError
		if !errors.As(err, &verr) || verr.Reason != evidence.RejectReasonInvalidEquivocationProof {
			t.Errorf("%s: expected an %q validation error, got %v", tc.name, evidence.RejectReasonInvalidEquivocationProof, err)
			continue
		}
		if got := classifyProposalError(err); got != proposalDispositionPrune {
			t.Errorf("%s: expected the builder to prune it, got %v", tc.name, got)
		}
		if n := evidenceIndexSize(t, node); n != 0 {
			t.Errorf("%s: nothing may be recorded, got %d records", tc.name, n)
		}
	}

	// The same proof, listed honestly for a height the chain has reached, is fine.
	node := newEvidenceBoundsNode(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	ev := doubleSignEvidence(t, offender, reporter, 400, 7, evidence.EquivocationVotePrecommit)
	if err := applyEvidenceTx(t, node, applyAt, signedSubmitEvidenceTx(t, ev, reporter, 0)); err != nil {
		t.Fatalf("expected a proof for a listed height inside the window to be accepted: %v", err)
	}
}

// TestEvidenceEachDoubleSignOfOneValidatorIsPenalisedOnce: what is deduplicated is
// the offense, not the offender. Double-signing at another round, in the other kind
// of vote, or at another height is another misdeed with its own proof, and each is
// penalised in its own right, for what the offender has bonded when it is reported.
func TestEvidenceEachDoubleSignOfOneValidatorIsPenalisedOnce(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	const stake = 1_000

	offenses := []struct {
		name     string
		height   uint64
		round    int
		voteType evidence.EquivocationVoteType
	}{
		{"first", 10, 1, evidence.EquivocationVotePrevote},
		{"another round", 10, 2, evidence.EquivocationVotePrevote},
		{"the other kind of vote", 10, 1, evidence.EquivocationVotePrecommit},
		{"another height", 11, 1, evidence.EquivocationVotePrevote},
	}
	for i, o := range offenses {
		seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(stake))
		ev := doubleSignEvidence(t, offender, reporter, o.height, o.round, o.voteType)
		if err := applyEvidenceTx(t, node, 200, signedSubmitEvidenceTx(t, ev, reporter, uint64(i))); err != nil {
			t.Fatalf("%s: a different double-sign must be recorded: %v", o.name, err)
		}
		node.stateMu.Lock()
		node.state.BeginBlock(200, time.Unix(1_700_000_000, 0).UTC())
		err := node.processPendingEvidenceForState(node.state, 200)
		node.state.EndBlock()
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("%s: process evidence: %v", o.name, err)
		}
		addr := offender.address()
		account, err := node.GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load offender: %v", err)
		}
		if account.LockedZNHB.Sign() != 0 {
			t.Fatalf("%s: expected this double-sign penalised for the stake bonded at the time, got locked=%s", o.name, account.LockedZNHB)
		}

		// ...and only once: the same double-sign again, written another way, is refused.
		again := rewrittenEvidence(t, ev, offender, reporter, rewritingsOfOneDoubleSign()[0])
		if err := applyEvidenceTx(t, node, 200, signedSubmitEvidenceTx(t, again, reporter, uint64(i+1))); !errors.Is(err, ErrEvidenceAlreadyRecorded) {
			t.Fatalf("%s: expected its rewriting refused as already recorded, got %v", o.name, err)
		}
	}
	if n := evidenceIndexSize(t, node); n != len(offenses) {
		t.Fatalf("expected one record per offense, got %d", n)
	}

	treasury := node.state.evidenceSlashTreasury()
	account, err := node.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if want := big.NewInt(stake * int64(len(offenses))); account.BalanceZNHB.Cmp(want) != 0 {
		t.Fatalf("expected the treasury to hold exactly %s forfeited (one slash per offense), got %s", want, account.BalanceZNHB)
	}
}

// TestEvidenceIndexEntryCarriesTheOffenseAndItsHeight: what the block-level
// evidence pass reads from state for a recorded double-sign is the offense it is
// about and the height of the double-sign, and the penalty is recorded against the
// offense.
func TestEvidenceIndexEntryCarriesTheOffenseAndItsHeight(t *testing.T) {
	node := newEvidenceBoundsNode(t)
	offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
	seedEvidenceReporterBond(t, node, reporter.address())
	seedEvidenceOffenderStake(t, node, offender.address(), big.NewInt(1_000))
	ev := doubleSignEvidence(t, offender, reporter, 120, 4, evidence.EquivocationVotePrevote)
	ev.Heights = []uint64{100, 120}
	ev = signedReport(t, ev, reporter)
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	if err := applyEvidenceTx(t, node, 200, signedSubmitEvidenceTx(t, ev, reporter, 0)); err != nil {
		t.Fatalf("record: %v", err)
	}
	node.stateMu.Lock()
	node.state.BeginBlock(200, time.Unix(1_700_000_000, 0).UTC())
	err = node.processPendingEvidenceForState(node.state, 200)
	node.state.EndBlock()
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("process evidence: %v", err)
	}

	record, ok, err := node.PotsoEvidenceByHash(hash)
	if err != nil || !ok {
		t.Fatalf("the record is retrievable by its canonical hash: ok=%v err=%v", ok, err)
	}
	offense := record.Offense()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	manager := nhbstate.NewManager(node.state.Trie)
	entries, err := manager.PotsoEvidenceIndex()
	if err != nil || len(entries) != 1 {
		t.Fatalf("index: %v (%d entries)", err, len(entries))
	}
	if entries[0].Hash != hash || entries[0].Offense != offense.Key || entries[0].AnchorHeight != 120 {
		t.Fatalf("unexpected index entry %+v (offense %x, want anchor height 120)", entries[0], offense.Key[:6])
	}
	if offense.Key == hash {
		t.Fatalf("an equivocation's offense must not be the report's own hash")
	}
	if applied, err := manager.PotsoPenaltyApplied(offense.Key, offender.address()); err != nil || !applied {
		t.Fatalf("expected the penalty recorded against the offense: applied=%v err=%v", applied, err)
	}
	if applied, _ := manager.PotsoPenaltyApplied(hash, offender.address()); applied {
		t.Fatalf("the penalty must not be recorded against the report's own hash")
	}
}
