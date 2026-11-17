package core

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	swap "nhbchain/native/swap"
)

// Conflict pairs: two (or more) transactions that each pass admission on their
// own -- admission simulates against COMMITTED state -- but cannot both apply
// once the first has been applied in the same block. Before the containment
// layer the second one returned a bare error that fell to the default ABORT:
// the whole proposal failed on every round, on both validators, and the loser
// was never removed from the mempool, so any two ordinary accounts could halt
// the chain permanently. The repository is public, so that is attacker-
// triggerable.
//
// Each test asserts the same shape of outcome:
//   - CreateBlock returns a block (no error);
//   - the honest transaction is in it, and exactly one of the pair is;
//   - the loser stays resident, is not stuck in flight, and carries a strike;
//   - where the family is funded through genesis the second validator
//     re-derives the identical state root;
//   - the loser is evicted after a bounded number of blocks while the chain
//     keeps producing blocks the whole time.

// requireBlockBuildsUntil mines blocks with mine until done reports true,
// failing if it takes more than limit blocks. A failing mine (CreateBlock or
// CommitBlock error) fails the test through mine itself.
func requireBlockBuildsUntil(t *testing.T, what string, limit int, mine func(), done func() bool) {
	t.Helper()
	for i := 0; i < limit; i++ {
		if done() {
			return
		}
		mine()
	}
	if !done() {
		t.Fatalf("%s did not happen within %d blocks", what, limit)
	}
}

// escrowID reproduces the escrow id derivation (keccak of payer, payee, empty
// metadata hash and the big-endian creation nonce).
func escrowID(payer, payee []byte, nonce uint64) [32]byte {
	var meta [32]byte
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)
	return ethcrypto.Keccak256Hash(payer, payee, meta[:], nonceBytes[:])
}

func escrowCreatePayload(t *testing.T, payee []byte, amount int64, deadline int64, nonce uint64) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"payee": payee, "token": "NHB", "amount": big.NewInt(amount), "feeBps": 0,
		"deadline": deadline, "nonce": nonce,
	})
	if err != nil {
		t.Fatalf("marshal escrow create payload: %v", err)
	}
	return data
}

// ---- (a) escrow status races -------------------------------------------------

// The verifier's reproduction: an escrow Refund (by the payer) and a Release
// (by the payee) on the same funded escrow. Both pass admission; the second to
// apply returns a bare error from native/escrow.
func TestConflictEscrowRefundAndReleaseOnTheSameEscrow(t *testing.T) {
	payerKey, payeeKey, honestKey := livenessKey(t), livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:             []*crypto.PrivateKey{payerKey, payeeKey, honestKey},
		deterministicClock: true,
	})
	payer, payee := payerKey.PubKey().Address().Bytes(), payeeKey.PubKey().Address().Bytes()
	id := escrowID(payer, payee, 1)
	deadline := pair.clock.Add(2 * time.Hour).Unix()

	pair.submit(livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0, escrowCreatePayload(t, payee, 100, deadline, 1), nil, 0))
	pair.mine()
	pair.submit(livenessSign(t, payerKey, types.TxTypeLockEscrow, 1, id[:], nil, 0))
	pair.mine()

	refund := livenessSign(t, payerKey, types.TxTypeRefundEscrow, 2, id[:], nil, 0)
	release := livenessSign(t, payeeKey, types.TxTypeReleaseEscrow, 0, id[:], nil, 0)
	honest := livenessTransfer(t, honestKey, 0)
	pair.submit(refund)
	pair.submit(release) // both pass admission: it simulates against committed state
	pair.submit(honest)
	if pair.proposer.MempoolSize() != 3 {
		t.Fatalf("test setup: expected all three transactions admitted, mempool has %d", pair.proposer.MempoolSize())
	}

	evictionsBefore := livenessMetricValue(t, "nhb_mempool_evictions_total", map[string]string{"reason": "strikes"})
	block := pair.build() // must not error: this is the halt
	if !livenessBlockContains(t, block, honest) {
		t.Fatalf("the honest transfer must be in the block")
	}
	if !livenessBlockContains(t, block, refund) || livenessBlockContains(t, block, release) {
		t.Fatalf("expected the first-admitted Refund to win and the Release to be excluded, got %d transactions", len(block.Transactions))
	}
	if !livenessResident(t, pair.proposer, release) {
		t.Fatalf("the losing Release must stay resident")
	}
	if rec, ok := livenessStrikes(t, pair.proposer, release); !ok || rec.strikes != 1 {
		t.Fatalf("the losing Release must carry exactly one strike, got %+v ok=%v", rec, ok)
	}
	if n := livenessInFlight(pair.proposer); n != 2 {
		t.Fatalf("expected only the two included transactions leased, got %d", n)
	}
	pair.commit(block) // the second validator re-derives the identical state root

	// The chain keeps producing blocks and the dead Release is evicted after a
	// bounded number of them, confirmed by a solo dry run that also fails.
	requireBlockBuildsUntil(t, "eviction of the losing Release", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, release) })
	if got := livenessMetricValue(t, "nhb_mempool_evictions_total", map[string]string{"reason": "strikes"}); got <= evictionsBefore {
		t.Fatalf("expected the eviction to be counted")
	}
	if pair.proposer.MempoolSize() != 0 {
		t.Fatalf("expected an empty mempool at the end, got %d", pair.proposer.MempoolSize())
	}
}

// The delegated variant: a relayer's DelegatedRelease (authorised by the
// payee's embedded signature) races the payer's own Refund.
func TestConflictDelegatedEscrowReleaseAndRefund(t *testing.T) {
	payerKey, payeeKey, relayerKey := livenessKey(t), livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:             []*crypto.PrivateKey{payerKey, payeeKey, relayerKey},
		deterministicClock: true,
	})
	payer, payee := payerKey.PubKey().Address().Bytes(), payeeKey.PubKey().Address().Bytes()
	id := escrowID(payer, payee, 7)
	deadline := pair.clock.Add(2 * time.Hour).Unix()

	pair.submit(livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0, escrowCreatePayload(t, payee, 100, deadline, 7), nil, 0))
	pair.mine()
	pair.submit(livenessSign(t, payerKey, types.TxTypeLockEscrow, 1, id[:], nil, 0))
	pair.mine()

	actionPayload, actionSig := signEscrowActionEnvelope(t, id, "release", "", payeeKey.PrivateKey)
	delegatedData, err := rlp.EncodeToBytes(struct {
		EscrowID  string `json:"escrowId"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{EscrowID: "0x" + hexEncode(id[:]), Payload: actionPayload, Signature: actionSig})
	if err != nil {
		t.Fatalf("encode delegated release: %v", err)
	}
	refund := livenessSign(t, payerKey, types.TxTypeRefundEscrow, 2, id[:], nil, 0)
	delegated := livenessSign(t, relayerKey, types.TxTypeDelegatedReleaseEscrow, 0, delegatedData, nil, 0)
	pair.submit(refund)
	pair.submit(delegated)

	block := pair.build()
	if !livenessBlockContains(t, block, refund) || livenessBlockContains(t, block, delegated) {
		t.Fatalf("expected the Refund to win and the delegated Release to be excluded, got %d transactions", len(block.Transactions))
	}
	pair.commit(block)
	requireBlockBuildsUntil(t, "eviction of the delegated Release", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, delegated) })
}

// ---- (b) wall-clock reads ----------------------------------------------------
//
// Escrow and trade timestamps come from the block time, so no shipped
// transaction type reads the wall clock; the tests for the detector that guards
// against one (and for the escrow transactions it no longer holds out) are in
// proposal_clock_test.go.

// A deterministic clock (a test or harness that installs its own) is not a
// hazard: every re-execution sees the same values, so nothing is held out and
// every block validates however long after it was built.
func TestDeterministicClockDoesNotTriggerTheDetector(t *testing.T) {
	payerKey, payeeKey := livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:             []*crypto.PrivateKey{payerKey, payeeKey},
		deterministicClock: true,
	})
	create := livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0,
		escrowCreatePayload(t, payeeKey.PubKey().Address().Bytes(), 100, pair.clock.Add(2*time.Hour).Unix(), 1), nil, 0)
	pair.submit(create)
	block := pair.build()
	if !livenessBlockContains(t, block, create) {
		t.Fatalf("with a deterministic clock the escrow create is proposable")
	}
	time.Sleep(1100 * time.Millisecond)
	pair.commit(block)
}

// Refund compares the block time to the escrow's deadline. Admitted just before
// the deadline and built just after, it fails with a bare error.
func TestConflictEscrowRefundPastDeadlineDoesNotHalt(t *testing.T) {
	payerKey, payeeKey, honestKey := livenessKey(t), livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:             []*crypto.PrivateKey{payerKey, payeeKey, honestKey},
		deterministicClock: true,
	})
	payer, payee := payerKey.PubKey().Address().Bytes(), payeeKey.PubKey().Address().Bytes()
	id := escrowID(payer, payee, 3)
	deadline := pair.clock.Add(5 * time.Second).Unix()

	pair.submit(livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0, escrowCreatePayload(t, payee, 100, deadline, 3), nil, 0))
	pair.mine()
	pair.submit(livenessSign(t, payerKey, types.TxTypeLockEscrow, 1, id[:], nil, 0))
	pair.mine()

	refund := livenessSign(t, payerKey, types.TxTypeRefundEscrow, 2, id[:], nil, 0)
	honest := livenessTransfer(t, honestKey, 0)
	pair.submit(refund) // admitted while the deadline is still ahead
	pair.submit(honest)
	pair.advance(30 * time.Second) // ... and the deadline passes before it is built

	block := pair.build()
	if livenessBlockContains(t, block, refund) {
		t.Fatalf("a refund past the escrow deadline cannot apply and must be excluded")
	}
	if !livenessBlockContains(t, block, honest) {
		t.Fatalf("the honest transfer must be included")
	}
	pair.commit(block)
	requireBlockBuildsUntil(t, "eviction of the late refund", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, refund) })
}

// ---- (c) governance permissionless triggers ---------------------------------

// Two accounts finalize the same proposal in the same block. Finalize is a
// permissionless trigger with no caller identity, so both are admitted; the
// second finds the proposal already decided.
func TestConflictGovernanceDoubleFinalizeDoesNotHalt(t *testing.T) {
	proposer, validator, now := newGovernanceConsensusHarness(t)
	proposerKey := livenessKey(t)

	mineIdenticalBlock(t, proposer, validator) // height 1: committed headers to build on
	proposeTx := signedGovTx(t, proposerKey, 0, types.TxTypeGovPropose, govProposePayload{
		Kind: governance.ProposalKindParamUpdate, Payload: `{"fees.baseFee":1000}`, Deposit: big.NewInt(0),
	})
	if err := proposer.AddTransaction(proposeTx); err != nil {
		t.Fatalf("add propose: %v", err)
	}
	mineIdenticalBlock(t, proposer, validator)
	proposals, _, err := proposer.GovernanceListProposals(0, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("expected one proposal, got %d (err=%v)", len(proposals), err)
	}
	proposalID := proposals[0].ID
	*now = now.Add(200 * time.Second) // past VotingEnd on both nodes

	finalizeA := signedGovTx(t, livenessKey(t), 0, types.TxTypeGovFinalize, govProposalIDPayload{ProposalID: proposalID})
	finalizeB := signedGovTx(t, livenessKey(t), 0, types.TxTypeGovFinalize, govProposalIDPayload{ProposalID: proposalID})
	if err := proposer.AddTransaction(finalizeA); err != nil {
		t.Fatalf("add finalize A: %v", err)
	}
	if err := proposer.AddTransaction(finalizeB); err != nil {
		t.Fatalf("add finalize B: %v (both must pass admission)", err)
	}

	block, err := proposer.CreateBlock(proposer.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must not halt on a double finalize: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected exactly one of the two finalizations in the block, got %d", len(block.Transactions))
	}
	commitBlockOnBoth(t, proposer, validator, block)
	proposal, ok, err := proposer.GovernanceProposal(proposalID)
	if err != nil || !ok || proposal.Status != governance.ProposalStatusPassed {
		t.Fatalf("expected the proposal Passed, got ok=%v err=%v", ok, err)
	}

	loser := finalizeB
	if livenessBlockContains(t, block, finalizeB) {
		loser = finalizeA
	}
	if !livenessResident(t, proposer, loser) {
		t.Fatalf("the losing finalize stays resident")
	}
	requireBlockBuildsUntil(t, "eviction of the duplicate finalize", 12,
		func() {
			block, err := proposer.CreateBlock(proposer.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock: %v", err)
			}
			commitBlockOnBoth(t, proposer, validator, block)
		},
		func() bool { return !livenessResident(t, proposer, loser) })
}

// ---- (d) market fill/cancel --------------------------------------------------

// A seller cancels a listing in the same block a buyer fills it.
func TestConflictMarketFillAndCancelOnTheSameListing(t *testing.T) {
	sellerKey, buyerKey, honestKey := livenessKey(t), livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:   []*crypto.PrivateKey{sellerKey, buyerKey, honestKey},
		znhbEach: new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000_000_000_000_000)),
	})
	create := marketCreateListingTx(t, 0, big.NewInt(1_000_000_000), big.NewInt(1), big.NewInt(1), false)
	if err := create.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create listing: %v", err)
	}
	pair.submit(create)
	pair.mine()

	listings := openMarketListingIDs(t, pair.proposer)
	if len(listings) != 1 {
		t.Fatalf("expected one open listing, got %d", len(listings))
	}
	cancel := marketCancelListingTx(t, 1, listings[0])
	if err := cancel.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign cancel: %v", err)
	}
	fill := marketFillListingTx(t, 0, listings[0], big.NewInt(1_000_000_000))
	if err := fill.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign fill: %v", err)
	}
	honest := livenessTransfer(t, honestKey, 0)
	pair.submit(cancel)
	pair.submit(fill)
	pair.submit(honest)

	block := pair.build()
	if !livenessBlockContains(t, block, honest) {
		t.Fatalf("the honest transfer must be included")
	}
	if livenessBlockContains(t, block, cancel) == livenessBlockContains(t, block, fill) {
		t.Fatalf("exactly one of fill/cancel may apply, got %d transactions", len(block.Transactions))
	}
	pair.commit(block)
	loser := fill
	if livenessBlockContains(t, block, fill) {
		loser = cancel
	}
	requireBlockBuildsUntil(t, "eviction of the losing market transaction", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, loser) })
}

// ---- (e) BuyZNHB shared curve ------------------------------------------------

// Two buyers each hold a quote that is valid on its own; buying moves the curve,
// so the second buyer's maximum is now too low ("price moved").
func TestConflictBuyZNHBSharedCurveDoesNotHalt(t *testing.T) {
	adminKey, buyerA, buyerB := livenessKey(t), livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{
		funded:   []*crypto.PrivateKey{buyerA, buyerB},
		adminKey: adminKey,
	})
	pair.mine() // block 1 bootstraps the ZNHB sale and reward pools

	// The curve is priced in tranches of 50,000 ZNHB, so a purchase of exactly
	// one tranche moves the price for whoever buys next.
	amount := new(big.Int).Mul(big.NewInt(50_000), big.NewInt(1_000_000_000_000_000_000))
	quote, err := pair.proposer.QuoteBuyZNHB(amount)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	cost, ok := new(big.Int).SetString(quote.NHBCostWei, 10)
	if !ok {
		t.Fatalf("unparseable quote cost %q", quote.NHBCostWei)
	}
	txA := buyZNHBTx(t, 0, amount, cost)
	txB := buyZNHBTx(t, 0, amount, cost)
	if err := txA.Sign(buyerA.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := txB.Sign(buyerB.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	pair.submit(txA)
	pair.submit(txB) // both quotes are exact for committed state

	block := pair.build()
	if len(block.Transactions) != 1 {
		t.Fatalf("expected exactly one of the two purchases in the block, got %d", len(block.Transactions))
	}
	pair.commit(block)
	loser := txB
	if livenessBlockContains(t, block, txB) {
		loser = txA
	}
	if !livenessResident(t, pair.proposer, loser) {
		t.Fatalf("the buyer whose price moved keeps the transaction resident (they may refresh)")
	}
	requireBlockBuildsUntil(t, "eviction of the stale-quote purchase", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, loser) })
}

// ---- (f) lending caps ---------------------------------------------------------

// Two borrowers each stay under the per-block borrow cap alone; together they
// exceed it. The engine's sentinel (errBorrowCapPerBlock) is unexported, so the
// classifier in package core cannot recognise it: it is exactly the kind of
// error that used to halt the chain.
func TestConflictLendingPerBlockBorrowCapDoesNotHalt(t *testing.T) {
	node := newTestNode(t)
	node.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
		BorrowCaps:           lending.BorrowCaps{PerBlock: mustBigInt(t, "300000000000000000000")},
	})
	node.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	supplier, borrowerA, borrowerB := livenessKey(t), livenessKey(t), livenessKey(t)
	livenessFundBig(t, node, supplier.PubKey().Address().Bytes(), mustBigInt(t, "10000000000000000000000"), big.NewInt(0))
	for _, key := range []*crypto.PrivateKey{borrowerA, borrowerB} {
		livenessFundBig(t, node, key.PubKey().Address().Bytes(), mustBigInt(t, "1000000000000000000000"), mustBigInt(t, "50000000000000000000000"))
	}
	for _, tx := range []*types.Transaction{
		mustSignLendingTx(t, supplier, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "5000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerA, types.TxTypeLendingDepositZNHB, 0, mustBigInt(t, "50000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerB, types.TxTypeLendingDepositZNHB, 0, mustBigInt(t, "50000000000000000000000"), lendingNativePayload{PoolID: "default"}),
	} {
		if err := node.AddTransaction(tx); err != nil {
			t.Fatalf("admit setup transaction: %v", err)
		}
	}
	livenessMine(t, node)

	borrowA := mustSignLendingTx(t, borrowerA, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "200000000000000000000"), lendingNativePayload{PoolID: "default"})
	borrowB := mustSignLendingTx(t, borrowerB, types.TxTypeLendingBorrowNHB, 1, mustBigInt(t, "200000000000000000000"), lendingNativePayload{PoolID: "default"})
	if err := node.AddTransaction(borrowA); err != nil {
		t.Fatalf("admit borrow A: %v", err)
	}
	if err := node.AddTransaction(borrowB); err != nil {
		t.Fatalf("admit borrow B: %v (each is under the cap on its own)", err)
	}

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must not halt when two borrows together exceed the per-block cap: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected exactly one of the two borrows in the block, got %d", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit: %v", err)
	}
	loser := borrowB
	if livenessBlockContains(t, block, borrowB) {
		loser = borrowA
	}
	if !livenessResident(t, node, loser) {
		t.Fatalf("the borrower who lost the block-cap race keeps the transaction resident")
	}
}

// ---- (g) POS authorisation state ------------------------------------------------

// The POS capture/void race. native/pos returns unexported sentinels
// ("pos: authorization already captured", "pos: authorization voided",
// "pos: authorization expired") that package core cannot classify.
//
// This one is INJECTED rather than driven by real transactions, and the reason
// is worth recording: applyPOSCapture/applyPOSVoid take the authorisation id as
// a protobuf STRING field but the id is 32 arbitrary bytes, and their decoding
// (a 64-character string is passed to go-ethereum's ParseHexOrString, which only
// decodes a "0x"-prefixed string and otherwise returns the raw ASCII) can never
// reproduce a real id -- an honest capture or void through the transaction path
// fails with "authorization not found" today. That is a separate defect and is
// not touched here; the point of this test is that whatever native/pos returns
// for the second transaction of a pair, block production survives it.
func TestConflictPOSCaptureAndVoidErrorsDoNotHalt(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  string
	}{
		{"captured then voided", "pos: authorization already captured"},
		{"voided then captured", "pos: authorization voided"},
		{"expired between admission and build", "pos: authorization expired"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			payerKey, merchantKey, honestKey := livenessKey(t), livenessKey(t), livenessKey(t)
			for _, key := range []*crypto.PrivateKey{payerKey, merchantKey, honestKey} {
				livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000, 1_000)
			}
			capture := livenessSign(t, merchantKey, types.TxTypePOSCapture, 0, []byte("capture"), nil, 0)
			void := livenessSign(t, payerKey, types.TxTypePOSVoid, 0, []byte("void"), nil, 0)
			honest := livenessTransfer(t, honestKey, 0)
			livenessInject(node, capture, void, honest)
			node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
				if tx == void {
					return errors.New(tc.err)
				}
				return nil
			})

			block, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock must survive %q: %v", tc.err, err)
			}
			if livenessBlockContains(t, block, void) || !livenessBlockContains(t, block, honest) {
				t.Fatalf("expected the failing transaction excluded and the honest one included, got %d transactions", len(block.Transactions))
			}
			if rec, ok := livenessStrikes(t, node, void); !ok || rec.strikes != 1 {
				t.Fatalf("the failing POS transaction is quarantined (one strike), got %+v ok=%v", rec, ok)
			}
			requireBlockBuildsUntil(t, "eviction of the failing POS transaction", 12,
				func() { livenessMine(t, node) },
				func() bool { return !livenessResident(t, node, void) })
		})
	}
}

// ---- (h) senderless evidence ---------------------------------------------------

// A senderless evidence submission whose window closed between admission and
// building: ValidationError{expired}. The classifier prunes it (height only
// grows), and the build succeeds.
func TestConflictExpiredSenderlessEvidenceIsPrunedNotHalting(t *testing.T) {
	node := newTestNode(t)
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
	transfer := livenessTransfer(t, honest, 0)
	evidenceTx := livenessSign(t, livenessKey(t), types.TxTypeSubmitEvidence, 0, []byte{0x01}, nil, 0)
	livenessInject(node, evidenceTx, transfer)

	// Evidence expiry needs a chain more than DefaultMaxAgeBlocks (8640) tall,
	// so the apply error is injected; the classification of the real error type
	// is pinned by TestClassifyProposalErrorDispositions.
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx == evidenceTx {
			return &evidence.ValidationError{Reason: evidence.RejectReasonExpired, Message: "height 1 exceeds evidence window"}
		}
		return nil
	})
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must not abort on an expired evidence submission: %v", err)
	}
	if !livenessBlockContains(t, block, transfer) || livenessBlockContains(t, block, evidenceTx) {
		t.Fatalf("expected the transfer alone in the block, got %d transactions", len(block.Transactions))
	}
	if livenessResident(t, node, evidenceTx) {
		t.Fatalf("expired evidence can never become valid again and must be pruned at once")
	}
}

// ---- (i) balance drift ---------------------------------------------------------

func TestConflictBalanceDriftForNHBAndZNHBTransfers(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  types.TxType
	}{{"transfer_nhb", types.TxTypeTransfer}, {"transfer_znhb", types.TxTypeTransferZNHB}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			sender, honest := livenessKey(t), livenessKey(t)
			livenessFund(t, node, sender.PubKey().Address().Bytes(), 500, 500)
			livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000, 0)
			to := make([]byte, 20)
			to[19] = 0x77
			spend := livenessSign(t, sender, tc.typ, 0, nil, to, 500)
			ok := livenessTransfer(t, honest, 0)
			if err := node.AddTransaction(spend); err != nil {
				t.Fatalf("admit: %v", err)
			}
			if err := node.AddTransaction(ok); err != nil {
				t.Fatalf("admit honest: %v", err)
			}
			// Something else reduces the balance before the block is built
			// (a lifecycle auto-debit, another transaction, an operator action).
			livenessFund(t, node, sender.PubKey().Address().Bytes(), 100, 100)

			block, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock must not halt when a sender's balance dropped: %v", err)
			}
			if livenessBlockContains(t, block, spend) || !livenessBlockContains(t, block, ok) {
				t.Fatalf("expected only the honest transfer, got %d transactions", len(block.Transactions))
			}
			requireBlockBuildsUntil(t, "eviction of the unaffordable transfer", 12,
				func() { livenessMine(t, node) },
				func() bool { return !livenessResident(t, node, spend) })
		})
	}
}

// ---- (j) price proof (the documented deliberate ABORT) -----------------------

// ErrSwapPriceProofInvalid used to be left at ABORT on purpose: its causes span
// permanent and fixable, and errors.Is cannot tell which fired. Quarantine
// needs no guess.
func TestConflictInvalidSwapPriceProofIsQuarantinedNotAborted(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	recipient := toAddress(livenessKey(t))
	now := time.Now().UTC().Truncate(time.Second)

	// A valid voucher, and one whose price proof names the wrong pair -- a
	// perfectly signed proof for something else.
	good := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-PROOF-GOOD")
	goodProof := signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", now)
	goodTx := swapVoucherTx(t, minterKey, good, "GOOD-1", goodProof)

	bad := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-PROOF-BAD")
	badProof, err := swap.NewPriceProof(swap.PriceProofDomainV1, "nowpayments", "ZNHB/EUR", "0.05", now.Unix(), nil)
	if err != nil {
		t.Fatalf("build price proof: %v", err)
	}
	hash, err := badProof.Hash()
	if err != nil {
		t.Fatalf("hash price proof: %v", err)
	}
	if badProof.Signature, err = ethcrypto.Sign(hash, oracleKey.PrivateKey); err != nil {
		t.Fatalf("sign price proof: %v", err)
	}
	badTx := swapVoucherTx(t, minterKey, bad, "BAD-1", badProof)
	livenessInject(node, goodTx, badTx)

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must not abort on an invalid price proof: %v", err)
	}
	if !livenessBlockContains(t, block, goodTx) || livenessBlockContains(t, block, badTx) {
		t.Fatalf("expected only the valid voucher in the block, got %d transactions", len(block.Transactions))
	}
	if rec, ok := livenessStrikes(t, node, badTx); !ok || rec.strikes != 1 {
		t.Fatalf("the invalid voucher must carry one strike (quarantined, not pruned), got %+v ok=%v", rec, ok)
	}
	requireBlockBuildsUntil(t, "eviction of the invalid voucher", 12,
		func() {
			b, err := node.CreateBlock(node.GetMempool())
			if err != nil {
				t.Fatalf("CreateBlock: %v", err)
			}
			if err := node.CommitBlock(b); err != nil {
				t.Fatalf("CommitBlock: %v", err)
			}
		},
		func() bool { return !livenessResident(t, node, badTx) })
}

func swapVoucherTx(t *testing.T, minterKey *crypto.PrivateKey, voucher swap.VoucherV1, providerTxID string, proof *swap.PriceProof) *types.Transaction {
	t.Helper()
	sig := signSwapVoucherCore(t, minterKey, voucher)
	payload, err := encodeSwapVoucherMintTransaction(&swap.VoucherSubmission{
		Voucher: &voucher, Signature: sig, Provider: "nowpayments", ProviderTxID: providerTxID, PriceProof: proof,
	})
	if err != nil {
		t.Fatalf("encode voucher: %v", err)
	}
	return &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeSwapVoucherMint,
		Data: payload, GasLimit: 0, GasPrice: big.NewInt(0),
	}
}

// ---- (k) lifecycle failures ----------------------------------------------------

// A lifecycle failure that appears only when a particular transaction is in the
// block fails CreateBlock for the whole candidate set. The empty-block fallback
// keeps the chain alive at once; after two consecutive such failures the
// asynchronous isolation bisects out the culprit, and the next build includes
// the rest.
func TestConflictLifecycleFailureIsolatesTheCulprit(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 8)
	culprit := txs[5]
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		for _, tx := range blockTxs {
			if tx == culprit {
				return errFakeLifecycle
			}
		}
		return nil
	})

	// Build 1: the full build fails in the lifecycle stage; an empty block is
	// proposed instead of an error.
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must fall back to an empty block, got: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("expected the empty fallback, got %d transactions", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit fallback block: %v", err)
	}
	if got := node.LivenessSnapshot().ConsecutiveBuildFailures; got != 1 {
		t.Fatalf("expected one consecutive build failure, got %d", got)
	}
	fallbacksBefore := livenessMetricValue(t, "nhb_consensus_empty_block_fallbacks_total", nil)

	// Build 2: the second consecutive failure starts isolation.
	block, err = node.CreateBlock(node.GetMempool())
	if err != nil || len(block.Transactions) != 0 {
		t.Fatalf("second fallback: err=%v txs=%d", err, len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit fallback block: %v", err)
	}
	livenessWaitIsolation(t, node)
	if livenessResident(t, node, culprit) {
		t.Fatalf("isolation must evict the transaction that makes the lifecycle fail")
	}
	for i, tx := range txs {
		if tx != culprit && !livenessResident(t, node, tx) {
			t.Fatalf("isolation evicted an innocent transaction (%d)", i)
		}
	}

	// Build 3: everything else is included and the streak ends.
	block, err = node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock after isolation: %v", err)
	}
	if len(block.Transactions) != 7 {
		t.Fatalf("expected the 7 remaining transactions, got %d", len(block.Transactions))
	}
	if got := node.LivenessSnapshot().ConsecutiveBuildFailures; got != 0 {
		t.Fatalf("a clean build must end the failure streak, got %d", got)
	}
	if got := livenessMetricValue(t, "nhb_consensus_empty_block_fallbacks_total", nil); got < fallbacksBefore {
		t.Fatalf("fallback counter went backwards")
	}
}

type fakeLifecycleError struct{}

func (fakeLifecycleError) Error() string { return "injected lifecycle invariant violation" }

var errFakeLifecycle = fakeLifecycleError{}

// A lifecycle failure that persists on the EMPTY block is not attributable to
// any transaction and cannot be contained proposer-side: CreateBlock returns an
// error (never a panic), it is alarmed, and the node stays usable once the fault
// clears.
func TestConflictLifecycleFailureOnEmptyBlockIsReportedNotHidden(t *testing.T) {
	node := newTestNode(t)
	livenessFundedTransfers(t, node, 3)
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		return errFakeLifecycle
	})
	failuresBefore := livenessMetricValue(t, "nhb_consensus_build_failures_total", map[string]string{"reason": "lifecycle"})
	if _, err := node.CreateBlock(node.GetMempool()); err == nil {
		t.Fatalf("expected an error: even the empty block cannot be built")
	}
	livenessWaitIsolation(t, node)
	if got := livenessMetricValue(t, "nhb_consensus_build_failures_total", map[string]string{"reason": "lifecycle"}); got <= failuresBefore {
		t.Fatalf("the lifecycle failure must be counted")
	}
	if got := node.LivenessSnapshot().ConsecutiveBuildFailures; got < 1 {
		t.Fatalf("the failure streak must be visible, got %d", got)
	}
	requireLocksFree(t, node)

	node.setProposalLifecycleHook(nil)
	if _, err := node.CreateBlock(node.GetMempool()); err != nil {
		t.Fatalf("CreateBlock once the fault cleared: %v", err)
	}
}

// openMarketListingIDs returns the ids of the node's open market listings.
func openMarketListingIDs(t *testing.T, node *Node) [][32]byte {
	t.Helper()
	var ids [][32]byte
	err := node.WithState(func(m *nhbstate.Manager) error {
		listings, err := m.ListOpenMarketListings()
		if err != nil {
			return err
		}
		for _, listing := range listings {
			ids = append(ids, listing.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("list open market listings: %v", err)
	}
	return ids
}
