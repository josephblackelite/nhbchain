package core

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/syndtr/goleveldb/leveldb"

	"nhbchain/config"
	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
	"nhbchain/native/loyalty"
	"nhbchain/native/market"
	nativeparams "nhbchain/native/params"
	"nhbchain/native/pos"
	"nhbchain/native/subscriptions"
	swap "nhbchain/native/swap"
)

// infraErr is an error that opts in to infrastructure classification through
// the Infrastructure() marker interface, the way another package can without
// core importing it.
type infraErr struct{}

func (infraErr) Error() string        { return "storage backend unavailable" }
func (infraErr) Infrastructure() bool { return true }

// TestClassifyProposalErrorDispositions pins down classifyProposalError's
// verdict for every sentinel it is documented to recognize, plus the default
// (unclassified -> QUARANTINE) behavior for anything it doesn't, and the
// infrastructure-only ABORT class. Each case is also
// checked wrapped one level deeper (fmt.Errorf("...: %w", sentinel)) to
// confirm the classifier relies on errors.Is semantics, not direct equality
// -- exactly how every real call site in applySwapVoucherMintTransaction,
// applyMintTransaction, applyQuota, and the various pause guards actually
// returns these errors (wrapped, not bare).
func TestClassifyProposalErrorDispositions(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want proposalTxDisposition
	}{
		// PRUNE: pure function of the tx's own immutable payload, or of
		// monotonic state that can never revert once true.
		{"nonce too low", ErrNonceTooLow, proposalDispositionPrune},
		{"heartbeat too soon", ErrHeartbeatTooSoon, proposalDispositionPrune},
		{"unknown transaction type", ErrUnknownTransactionType, proposalDispositionPrune},
		{"swap duplicate provider tx", ErrSwapDuplicateProviderTx, proposalDispositionPrune},
		{"swap provider tx id collision", ErrSwapProviderTxIDCollision, proposalDispositionPrune},
		{"swap nonce used", ErrSwapNonceUsed, proposalDispositionPrune},
		{"swap expired", ErrSwapExpired, proposalDispositionPrune},
		{"swap voucher invalid payload", ErrSwapVoucherInvalidPayload, proposalDispositionPrune},
		{"swap invalid domain", ErrSwapInvalidDomain, proposalDispositionPrune},
		{"swap invalid chain id", ErrSwapInvalidChainID, proposalDispositionPrune},
		{"swap invalid token", ErrSwapInvalidToken, proposalDispositionPrune},
		{"swap invalid signature", ErrSwapInvalidSignature, proposalDispositionPrune},
		{"swap price proof required", ErrSwapPriceProofRequired, proposalDispositionPrune},
		{"swap price proof stale", ErrSwapPriceProofStale, proposalDispositionPrune},
		{"mint invoice used", ErrMintInvoiceUsed, proposalDispositionPrune},
		{"mint invalid chain id", ErrMintInvalidChainID, proposalDispositionPrune},
		{"mint expired", ErrMintExpired, proposalDispositionPrune},
		{"mint invalid payload", ErrMintInvalidPayload, proposalDispositionPrune},
		{"buyback ref price already recorded", ErrBuybackRefPriceAlreadyRecorded, proposalDispositionPrune},
		{"buyback ref price stale epoch", ErrBuybackRefPriceStaleEpoch, proposalDispositionPrune},
		{"lending ref price stale timestamp", ErrLendingRefPriceStaleTimestamp, proposalDispositionPrune},

		// PRUNE, added with the value branch: the signer and the counterparty
		// recorded in committed state are one address, which never changes.
		{"self subscription", subscriptions.ErrSelfSubscription, proposalDispositionPrune},
		{"self fill", market.ErrSelfFill, proposalDispositionPrune},
		{"self liquidation", lending.ErrSelfLiquidation, proposalDispositionPrune},
		// PRUNE, added with the POS branch: a pure function of the payload.
		{"pos invalid authorization id", ErrPOSInvalidAuthorizationID, proposalDispositionPrune},
		// PRUNE: a registry transaction that is invalid on its own bytes, or
		// whose nonce is not above its authority's last (the nonce only grows).
		{"pos registry invalid payload", ErrPOSRegistryInvalidPayload, proposalDispositionPrune},
		{"pos registry invalid request", pos.ErrInvalidRequest, proposalDispositionPrune},
		{"pos registry stale nonce", pos.ErrStaleNonce, proposalDispositionPrune},
		// PRUNE: a buyback ask under the minimum is decided by its own amount.
		{"buyback ask too small", ErrBuybackAskTooSmall, proposalDispositionPrune},
		// PRUNE: plan terms outside the fixed bounds are decided by the payload.
		{"subscription plan invalid", subscriptions.ErrInvalidPlan, proposalDispositionPrune},
		// PRUNE: a reversed voucher never becomes reconcilable.
		{"swap voucher not reconcilable", swap.ErrVoucherNotReconcilable, proposalDispositionPrune},
		// PRUNE, added with the loyalty and invariant branches: an owner naming
		// a wallet other than its own can never succeed for that signer.
		{"loyalty paymaster consent required", loyalty.ErrPaymasterConsentRequired, proposalDispositionPrune},
		// PRUNE, added with the evidence branch: decided by the report itself.
		{"evidence invalid payload", ErrEvidenceInvalidPayload, proposalDispositionPrune},
		{"evidence reporter is not the signer", ErrEvidenceReporterMismatch, proposalDispositionPrune},
		{"evidence already recorded", ErrEvidenceAlreadyRecorded, proposalDispositionPrune},
		{"evidence oversized", &evidence.ValidationError{Reason: evidence.RejectReasonOversized}, proposalDispositionPrune},
		{"evidence negative timestamp", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidTimestamp}, proposalDispositionPrune},
		{"evidence invalid type", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidType}, proposalDispositionPrune},
		{"evidence invalid offender", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidOffender}, proposalDispositionPrune},
		{"evidence invalid reporter", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidReporter}, proposalDispositionPrune},
		{"evidence unsorted heights", &evidence.ValidationError{Reason: evidence.RejectReasonUnsortedHeights}, proposalDispositionPrune},

		// SKIP: depends on mutable state shared across transactions in this
		// attempt, or on ordering within this attempt.
		{"nonce too high", ErrNonceTooHigh, proposalDispositionSkip},
		{"swap daily cap exceeded", ErrSwapDailyCapExceeded, proposalDispositionSkip},
		{"swap monthly cap exceeded", ErrSwapMonthlyCapExceeded, proposalDispositionSkip},
		{"swap velocity exceeded", ErrSwapVelocityExceeded, proposalDispositionSkip},
		{"swap price proof deviation", ErrSwapPriceProofDeviation, proposalDispositionSkip},
		{"swap slippage exceeded", ErrSwapSlippageExceeded, proposalDispositionSkip},
		{"swap invalid signer", ErrSwapInvalidSigner, proposalDispositionSkip},
		{"swap price proof signer unknown", ErrSwapPriceProofSignerUnknown, proposalDispositionSkip},
		{"swap mint paused", ErrSwapMintPaused, proposalDispositionSkip},
		{"swap unsupported fiat", ErrSwapUnsupportedFiat, proposalDispositionSkip},
		{"swap provider not allowed", ErrSwapProviderNotAllowed, proposalDispositionSkip},
		{"swap amount below minimum", ErrSwapAmountBelowMinimum, proposalDispositionSkip},
		{"swap amount above maximum", ErrSwapAmountAboveMaximum, proposalDispositionSkip},
		{"swap sanctioned", ErrSwapSanctioned, proposalDispositionSkip},
		{"generic module paused", nativecommon.ErrModulePaused, proposalDispositionSkip},
		{"stake paused", ErrStakePaused, proposalDispositionSkip},
		{"transfer nhb paused", ErrTransferNHBPaused, proposalDispositionSkip},
		{"transfer znhb paused", ErrTransferZNHBPaused, proposalDispositionSkip},
		{"quota requests exceeded", nativecommon.ErrQuotaRequestsExceeded, proposalDispositionSkip},
		{"quota nhb cap exceeded", nativecommon.ErrQuotaNHBCapExceeded, proposalDispositionSkip},
		{"quota counter overflow", nativecommon.ErrQuotaCounterOverflow, proposalDispositionSkip},
		{"mint paused", ErrMintPaused, proposalDispositionSkip},
		{"mint invalid signer", ErrMintInvalidSigner, proposalDispositionSkip},
		{"mint emission cap exceeded", ErrMintEmissionCapExceeded, proposalDispositionSkip},
		{"mint recipient unresolved", ErrMintRecipientUnresolved, proposalDispositionSkip},
		{"identity username taken", ErrIdentityUsernameTaken, proposalDispositionSkip},
		{"buyback ref price future epoch", ErrBuybackRefPriceFutureEpoch, proposalDispositionSkip},
		// SKIP: the registry role can be granted later, and a device can still
		// be registered.
		{"pos registry unauthorized", ErrPOSRegistryUnauthorized, proposalDispositionSkip},
		{"pos registry device not registered", pos.ErrDeviceNotRegistered, proposalDispositionSkip},
		// SKIP: the epoch's ask list is full, and the count starts again next epoch.
		{"buyback ask limit", ErrBuybackAskLimit, proposalDispositionSkip},
		// SKIP: the voucher a reconciliation names can still be minted.
		{"swap voucher not found", swap.ErrVoucherNotFound, proposalDispositionSkip},
		// SKIP, added with the loyalty branch: the named wallet can still opt
		// in, after which the same assignment succeeds.
		{"loyalty paymaster consent", loyalty.ErrPaymasterConsent, proposalDispositionSkip},
		// SKIP, added with the invariant branch: later inflows refill the pool.
		{"treasury reward pool insufficient", ErrTreasuryRewardPoolInsufficient, proposalDispositionSkip},
		// SKIP, added with the evidence branch: a reporter can bond more, and
		// the index frees up as records age out of the evidence window.
		{"evidence reporter not bonded", ErrEvidenceReporterNotBonded, proposalDispositionSkip},
		{"evidence index full", evidence.ErrIndexFull, proposalDispositionSkip},
		{"evidence reporter quota", evidence.ErrReporterQuota, proposalDispositionSkip},

		// PRUNE (added with the block-production hardening): a pure function
		// of the transaction's own immutable payload, or of a height that
		// only grows.
		{"invalid chain id", ErrInvalidChainID, proposalDispositionPrune},
		{"evidence expired", &evidence.ValidationError{Reason: evidence.RejectReasonExpired}, proposalDispositionPrune},
		{"evidence invalid signature", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidSignature}, proposalDispositionPrune},
		{"evidence invalid equivocation proof", &evidence.ValidationError{Reason: evidence.RejectReasonInvalidEquivocationProof}, proposalDispositionPrune},
		{"evidence empty heights", &evidence.ValidationError{Reason: evidence.RejectReasonEmptyHeights}, proposalDispositionPrune},

		// SKIP (added with the hardening): becomes valid once the chain
		// reaches the claimed height.
		{"evidence future height", &evidence.ValidationError{Reason: evidence.RejectReasonFutureHeight}, proposalDispositionSkip},

		// QUARANTINE: the default for everything nobody classified. These
		// rows used to be ABORT, which failed the whole proposal on every
		// round -- the halt this design closes.
		{"swap price proof invalid is quarantined", ErrSwapPriceProofInvalid, proposalDispositionQuarantine},
		{"generic error is quarantined", errors.New("boom"), proposalDispositionQuarantine},
		{"bare escrow status race is quarantined", errors.New("escrow: cannot release in status 3"), proposalDispositionQuarantine},
		{"evidence unknown reason is quarantined", &evidence.ValidationError{Reason: evidence.RejectReasonUnknown}, proposalDispositionQuarantine},
		{"evidence unknown height is quarantined", &evidence.ValidationError{Reason: evidence.RejectReasonUnknownHeight}, proposalDispositionQuarantine},
		{"recovered apply panic", &txPanicError{Value: "boom"}, proposalDispositionQuarantine},

		// ABORT: only infrastructure errors -- this node cannot read or write
		// state; excluding transactions cannot fix that and nothing may be
		// struck for it.
		{"context deadline exceeded", context.DeadlineExceeded, proposalDispositionAbort},
		{"context canceled", context.Canceled, proposalDispositionAbort},
		{"missing trie node", &gethtrie.MissingNodeError{NodeHash: common.HexToHash("0x01")}, proposalDispositionAbort},
		{"leveldb closed", leveldb.ErrClosed, proposalDispositionAbort},
		{"disk full", syscall.ENOSPC, proposalDispositionAbort},
		{"opt-in infrastructure error", infraErr{}, proposalDispositionAbort},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProposalError(tc.err); got != tc.want {
				t.Fatalf("classifyProposalError(%v) = %v, want %v", tc.err, got, tc.want)
			}
			wrapped := fmt.Errorf("wrapped context: %w", tc.err)
			if got := classifyProposalError(wrapped); got != tc.want {
				t.Fatalf("classifyProposalError(wrapped %v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestSwapVoucherMintDailyCapExceededSkipsNotAborts covers the originally
// reported liveness bug: two entirely ordinary, honestly-signed vouchers to
// the SAME recipient, submitted close enough together that both are pending
// when CreateBlock runs, where the second exceeds the recipient's daily mint
// allowance once the first is (speculatively) applied within the same
// attempt. Before this fix, ErrSwapDailyCapExceeded was unclassified and hit
// CreateBlock's default ABORT path, failing the ENTIRE proposal (not just
// the second voucher) every round until one of the two vouchers expired --
// blocking every other pending transaction too. This test proves: (1) the
// block still gets built and committed successfully, containing the first
// voucher, (2) the second voucher is excluded from the block but left
// resident in the mempool (not pruned, not lost), and (3) a later,
// independent CreateBlock attempt -- after headroom frees up -- can still
// mint the previously-skipped voucher, demonstrating the "reconsidered next
// round" property end-to-end, not just "doesn't abort".
func TestSwapVoucherMintDailyCapExceededSkipsNotAborts(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	voucherA := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-CAP-A")

	// Configure a daily cap that admits exactly one voucher's worth (1.0x)
	// but not two (2.0x) -- 1.5x headroom.
	dailyCap := new(big.Int).Add(voucherA.Amount, new(big.Int).Div(voucherA.Amount, big.NewInt(2)))
	node.SetSwapConfig(swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk: swap.RiskConfig{
			PerAddressDailyCapWei: dailyCap.String(),
		},
	})

	sigA := signSwapVoucherCore(t, minterKey, voucherA)
	proofA := signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now())
	submissionA := &swap.VoucherSubmission{
		Voucher: &voucherA, Signature: sigA, Provider: "nowpayments",
		ProviderTxID: "CAP-1", PriceProof: proofA,
	}
	if _, _, err := node.SwapSubmitVoucher(submissionA); err != nil {
		t.Fatalf("submit voucher A: %v", err)
	}

	// Voucher B: a second, independently valid voucher to the SAME
	// recipient. Constructed directly and injected into the mempool
	// (bypassing AddTransaction's own admission-time simulation), matching
	// the established pattern in TestSwapVoucherMintDuplicateDoesNotBlockProposal
	// and TestSwapVoucherMintStalePriceProofDoesNotBlockProposal, so this
	// test exercises exactly what CreateBlock's own sequential,
	// same-attempt application does -- independent of whatever the
	// admission-time simulation would separately decide.
	voucherB := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-CAP-B")
	sigB := signSwapVoucherCore(t, minterKey, voucherB)
	proofB := signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now())
	submissionB := &swap.VoucherSubmission{
		Voucher: &voucherB, Signature: sigB, Provider: "nowpayments",
		ProviderTxID: "CAP-2", PriceProof: proofB,
	}
	payloadB, err := encodeSwapVoucherMintTransaction(submissionB)
	if err != nil {
		t.Fatalf("encode voucher B: %v", err)
	}
	txB := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeSwapVoucherMint,
		Data: payloadB, GasLimit: 0, GasPrice: big.NewInt(0),
	}
	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, txB)
	node.mempoolMu.Unlock()

	pending := append([]*types.Transaction(nil), node.mempool...)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending transactions (A and B), got %d", len(pending))
	}

	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for a same-attempt daily-cap collision: %v", err)
	}
	if block == nil {
		t.Fatalf("expected a block to be produced")
	}

	foundA := false
	for _, tx := range block.Transactions {
		submission, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			continue
		}
		if submission.ProviderTxID == "CAP-1" {
			foundA = true
		}
		if submission.ProviderTxID == "CAP-2" {
			t.Fatalf("expected the daily-cap-exceeding voucher B to be skipped, not included in the block")
		}
	}
	if !foundA {
		t.Fatalf("expected voucher A to be included in the block")
	}

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// Voucher B must still be resident in the mempool after commit --
	// skipped, not pruned, not silently lost.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	var stillPendingProviderTxID string
	if stillPending == 1 {
		if sub, decErr := decodeSwapVoucherMintTransaction(node.mempool[0].Data); decErr == nil && sub != nil {
			stillPendingProviderTxID = sub.ProviderTxID
		}
	}
	node.mempoolMu.Unlock()
	if stillPending != 1 {
		t.Fatalf("expected voucher B still resident in mempool after commit (skipped, not pruned/lost), got %d pending", stillPending)
	}
	if stillPendingProviderTxID != "CAP-2" {
		t.Fatalf("expected the still-pending transaction to be voucher B (CAP-2), got %q", stillPendingProviderTxID)
	}

	// Prove the "reconsidered on a later, independent attempt" property:
	// raise the cap (simulating either the day boundary rolling over, or an
	// operator widening the limit) and confirm a SECOND, independent
	// CreateBlock call now succeeds in minting the previously-skipped
	// voucher.
	widerCap := new(big.Int).Mul(voucherA.Amount, big.NewInt(3))
	node.SetSwapConfig(swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk: swap.RiskConfig{
			PerAddressDailyCapWei: widerCap.String(),
		},
	})

	node.mempoolMu.Lock()
	pending2 := append([]*types.Transaction(nil), node.mempool...)
	node.mempoolMu.Unlock()
	if len(pending2) != 1 {
		t.Fatalf("expected exactly voucher B pending for the follow-up attempt, got %d", len(pending2))
	}

	block2, err := node.CreateBlock(pending2)
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	if err := node.CommitBlock(block2); err != nil {
		t.Fatalf("commit block 2: %v", err)
	}

	node.mempoolMu.Lock()
	finalPending := len(node.mempool)
	node.mempoolMu.Unlock()
	if finalPending != 0 {
		t.Fatalf("expected mempool empty after voucher B is finally minted, got %d", finalPending)
	}

	account, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	expectedTotal := new(big.Int).Add(voucherA.Amount, voucherB.Amount)
	if account.BalanceZNHB.Cmp(expectedTotal) != 0 {
		t.Fatalf("expected combined ZNHB balance %s (A+B both minted), got %s", expectedTotal, account.BalanceZNHB)
	}
}

// TestCreateBlockAllSkippableTransactionsProducesEmptyBlockNotHang is the
// pathological-input termination proof required alongside the ordinary
// SKIP-mechanism tests: EVERY transaction offered to CreateBlock hits a
// SKIP-classified error (a governance pause on TxTypeTransferZNHB, hitting
// every single one of a large batch of otherwise-independent, otherwise-
// valid transfers from distinct senders -- exactly the highest-severity
// gap identified during the classifier sweep, since an ordinary transfer
// pause is a routine governance action, not a rare edge case).
//
// Per classifyProposalError's termination argument: CreateBlock's outer
// retry loop only continues when the candidate set strictly shrank, so it
// terminates in at most len(original txs)+1 iterations; when every
// candidate is SKIP-classified, the very first buildProposalState pass
// already skips all of them together (the per-tx loop does not stop
// scanning on a SKIP verdict), so the very next retry runs on an empty
// candidate set, which trivially succeeds via
// computeDependencyGraph(nil) and an empty-block finalize. This test proves
// that mechanically, with a real (if synthetic) large batch, and with an
// explicit wall-clock deadline enforced from OUTSIDE CreateBlock -- so a
// regression that reintroduces a hang fails this test via timeout rather
// than blocking the whole test binary.
func TestCreateBlockAllSkippableTransactionsProducesEmptyBlockNotHang(t *testing.T) {
	node := newTestNode(t)

	// Pause TxTypeTransferZNHB via the governance-backed parameter store (not
	// just Node.SetModulePaused) so it survives buildProposalState's
	// n.refreshModulePauses() call, which re-reads pause state from chain
	// state on every single retry and would otherwise clobber a
	// SetModulePaused-only override on the very first attempt.
	node.stateMu.Lock()
	store := nativeparams.NewStore(nhbstate.NewManager(node.state.Trie))
	pauseErr := store.SetPauses(config.Pauses{TransferZNHB: true})
	node.stateMu.Unlock()
	if pauseErr != nil {
		t.Fatalf("persist governance pause: %v", pauseErr)
	}

	const txCount = 250
	recipient := make([]byte, 20)
	recipient[19] = 0x01
	txs := make([]*types.Transaction, 0, txCount)
	for i := 0; i < txCount; i++ {
		priv, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key %d: %v", i, err)
		}
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransferZNHB,
			Nonce:    0,
			To:       append([]byte(nil), recipient...),
			Value:    big.NewInt(1),
			GasLimit: 25_000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(priv.PrivateKey); err != nil {
			t.Fatalf("sign tx %d: %v", i, err)
		}
		txs = append(txs, tx)
	}

	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, txs...)
	node.mempoolMu.Unlock()

	pending := append([]*types.Transaction(nil), txs...)

	type result struct {
		block *types.Block
		err   error
	}
	done := make(chan result, 1)
	go func() {
		block, err := node.CreateBlock(pending)
		done <- result{block: block, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("CreateBlock must not abort when every transaction is SKIP-classified: %v", res.err)
		}
		if res.block == nil {
			t.Fatalf("expected an (empty) block to be produced")
		}
		if got := len(res.block.Transactions); got != 0 {
			t.Fatalf("expected an empty block (every transaction paused/skipped), got %d transactions", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("CreateBlock hung: did not return within 20s for %d all-skippable transactions", txCount)
	}

	// None of the 250 transactions may have been pruned from the mempool --
	// they were all SKIPPED (module paused, not permanently invalid), so
	// they must still be physically present, and none should still be
	// marked "in-flight" (n.proposedTxs), or they would never be offered to
	// a future GetMempool() call again.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	stillInFlight := len(node.proposedTxs)
	node.mempoolMu.Unlock()
	if stillPending != txCount {
		t.Fatalf("expected all %d transactions still resident in mempool (skipped, not pruned), got %d", txCount, stillPending)
	}
	if stillInFlight != 0 {
		t.Fatalf("expected 0 transactions still marked in-flight after the skip release, got %d", stillInFlight)
	}
}

// TestModulePauseSkipsTransactionInsteadOfAbortingProposal guards against
// re-regressing the highest-severity finding from the classifier sweep: an
// ordinary governance pause on a single module used to abort the ENTIRE
// proposal (every pending transaction from every sender, for every
// subsequent round) the instant even one matching transaction sat in the
// mempool, because module-pause errors were entirely unclassified before
// this fix. This test pauses the swap module, submits one swap-voucher-mint
// transaction (now expected to SKIP) alongside one ordinary, unrelated,
// unpaused TxTypeTransferZNHB transaction, and confirms the unrelated
// transaction still lands while the paused one is skipped, not pruned.
func TestModulePauseSkipsTransactionInsteadOfAbortingProposal(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-PAUSED")
	sig := signSwapVoucherCore(t, minterKey, voucher)
	proof := signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now())
	submission := &swap.VoucherSubmission{
		Voucher: &voucher, Signature: sig, Provider: "nowpayments",
		ProviderTxID: "PAUSED-1", PriceProof: proof,
	}
	if _, _, err := node.SwapSubmitVoucher(submission); err != nil {
		t.Fatalf("submit voucher: %v", err)
	}

	// An ordinary, unrelated, funded TxTypeTransferZNHB from a distinct
	// sender -- swap being paused must not affect this at all.
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()
	node.stateMu.Lock()
	if err := node.state.setAccount(senderAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(1_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed sender: %v", err)
	}
	node.stateMu.Unlock()

	recipientAddr := make([]byte, 20)
	recipientAddr[19] = 0x02
	transferTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       recipientAddr,
		Value:    big.NewInt(100),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := transferTx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, transferTx)
	node.mempoolMu.Unlock()

	// Now pause the swap module via the governance-backed store, persisting
	// so refreshModulePauses picks it up on every retry.
	node.stateMu.Lock()
	store := nativeparams.NewStore(nhbstate.NewManager(node.state.Trie))
	pauseErr := store.SetPauses(config.Pauses{Swap: true})
	node.stateMu.Unlock()
	if pauseErr != nil {
		t.Fatalf("persist governance pause: %v", pauseErr)
	}

	node.mempoolMu.Lock()
	pending := append([]*types.Transaction(nil), node.mempool...)
	node.mempoolMu.Unlock()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending transactions (voucher and transfer), got %d", len(pending))
	}

	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal because one module is paused: %v", err)
	}
	if got := len(block.Transactions); got != 1 {
		t.Fatalf("expected exactly 1 transaction in the block (the unrelated transfer), got %d", got)
	}
	if block.Transactions[0].Type != types.TxTypeTransferZNHB {
		t.Fatalf("expected the unrelated transfer to be the one included transaction, got type 0x%02X", byte(block.Transactions[0].Type))
	}

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// The paused voucher must still be resident in the mempool -- skipped,
	// not pruned.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	node.mempoolMu.Unlock()
	if stillPending != 1 {
		t.Fatalf("expected the paused voucher still resident in mempool after commit, got %d pending", stillPending)
	}
}

// TestNonceTooHighClassifiesSkipNotPrune exercises the ErrNonceTooHigh
// sub-case directly at the StateProcessor level (bypassing addTransaction's
// admission-time strict-sequencing, which makes this case believed
// unreachable via the real mempool today -- see classifyProposalError's doc
// comment). This confirms the split sentinel actually distinguishes the two
// directions correctly: ErrNonceTooLow (already covered by
// TestApplyTransactionRejectsNativeNonceReplay in
// state_transition_nonce_test.go, unmodified) classifies PRUNE, while
// ErrNonceTooHigh classifies SKIP.
func TestNonceTooHighClassifiesSkipNotPrune(t *testing.T) {
	node := newTestNode(t)
	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address().Bytes()

	node.stateMu.Lock()
	if err := node.state.setAccount(addr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(1_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed sender: %v", err)
	}
	node.stateMu.Unlock()

	recipient := make([]byte, 20)
	recipient[19] = 0x03
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    5, // account nonce is 0 -- this is "too high", not "too low"
		To:       recipient,
		Value:    big.NewInt(1),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(priv.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(applyErr, ErrNonceMismatch) {
		t.Fatalf("expected ErrNonceMismatch, got %v", applyErr)
	}
	if !errors.Is(applyErr, ErrNonceTooHigh) {
		t.Fatalf("expected ErrNonceTooHigh specifically, got %v", applyErr)
	}
	if errors.Is(applyErr, ErrNonceTooLow) {
		t.Fatalf("did not expect ErrNonceTooLow for a too-high nonce, got %v", applyErr)
	}
	if got := classifyProposalError(applyErr); got != proposalDispositionSkip {
		t.Fatalf("expected ErrNonceTooHigh to classify as SKIP, got %v", got)
	}
}

// TestUsernameCollisionSkipsNotAbortsProposal is the direct regression test
// for NHB-AUDIT-C2: two ordinary, unrelated accounts both submitting a
// RegisterIdentity transaction claiming the SAME username in the same
// CreateBlock attempt (an entirely ordinary race, no malicious coordination
// required). Before this fix, applyRegisterIdentity's collision error was a
// bare, unwrapped fmt.Errorf, so classifyProposalError always fell through
// to its default ABORT case -- failing the ENTIRE candidate block, every
// round, until one of the two colliding transactions expired, and blocking
// every other unrelated pending transaction too. This proves: (1) the block
// still builds and commits, containing exactly one winner, (2) the loser is
// excluded from the block but left resident in the mempool (skipped, not
// pruned -- it's a same-attempt ordering artifact, not a permanently
// malformed transaction), and (3) an unrelated third transaction in the same
// attempt is unaffected.
func TestUsernameCollisionSkipsNotAbortsProposal(t *testing.T) {
	node := newTestNode(t)

	keyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	keyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key B: %v", err)
	}
	addrA := toAddress(keyA)
	addrB := toAddress(keyB)

	for _, addr := range [][20]byte{addrA, addrB} {
		node.stateMu.Lock()
		err := node.state.setAccount(addr[:], &types.Account{
			BalanceNHB:  big.NewInt(0),
			BalanceZNHB: big.NewInt(0),
			Stake:       big.NewInt(0),
		})
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	const contestedUsername = "SameUsername"
	txA := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeRegisterIdentity,
		Nonce: 0, GasLimit: 21_000, GasPrice: big.NewInt(1), Value: big.NewInt(0),
		Data: []byte(contestedUsername),
	}
	if err := txA.Sign(keyA.PrivateKey); err != nil {
		t.Fatalf("sign tx A: %v", err)
	}
	txB := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeRegisterIdentity,
		Nonce: 0, GasLimit: 21_000, GasPrice: big.NewInt(1), Value: big.NewInt(0),
		Data: []byte(contestedUsername),
	}
	if err := txB.Sign(keyB.PrivateKey); err != nil {
		t.Fatalf("sign tx B: %v", err)
	}

	// An unrelated, unaffected third transaction from a distinct sender in
	// the same attempt -- the abort bug would have taken this down too.
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()
	node.stateMu.Lock()
	err = node.state.setAccount(senderAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(1_000),
		Stake:       big.NewInt(0),
	})
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	recipient := make([]byte, 20)
	recipient[19] = 0x09
	transferTx := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeTransferZNHB,
		Nonce: 0, To: recipient, Value: big.NewInt(100),
		GasLimit: 25_000, GasPrice: big.NewInt(1),
	}
	if err := transferTx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}

	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, txA, txB, transferTx)
	node.mempoolMu.Unlock()

	pending := append([]*types.Transaction(nil), node.mempool...)
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for a username collision: %v", err)
	}
	if block == nil {
		t.Fatalf("expected a block to be produced")
	}

	var winners int
	var sawTransfer bool
	for _, tx := range block.Transactions {
		switch tx.Type {
		case types.TxTypeRegisterIdentity:
			winners++
		case types.TxTypeTransferZNHB:
			sawTransfer = true
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one RegisterIdentity winner in the block, got %d", winners)
	}
	if !sawTransfer {
		t.Fatalf("expected the unrelated transfer to still be included in the block")
	}

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// The losing RegisterIdentity must still be resident in the mempool --
	// skipped, not pruned, not silently lost.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	var stillPendingIsIdentity bool
	if stillPending == 1 {
		stillPendingIsIdentity = node.mempool[0].Type == types.TxTypeRegisterIdentity
	}
	node.mempoolMu.Unlock()
	if stillPending != 1 {
		t.Fatalf("expected exactly the losing RegisterIdentity still resident in mempool after commit, got %d pending", stillPending)
	}
	if !stillPendingIsIdentity {
		t.Fatalf("expected the still-pending transaction to be the losing RegisterIdentity")
	}

	// Exactly one of A/B actually won the username on-chain.
	accountA, err := node.GetAccount(addrA[:])
	if err != nil {
		t.Fatalf("get account A: %v", err)
	}
	accountB, err := node.GetAccount(addrB[:])
	if err != nil {
		t.Fatalf("get account B: %v", err)
	}
	gotUsername := accountA.Username
	if gotUsername == "" {
		gotUsername = accountB.Username
	}
	if accountA.Username != "" && accountB.Username != "" {
		t.Fatalf("SAFETY REGRESSION: both colliding transactions claimed the username (A=%q B=%q)", accountA.Username, accountB.Username)
	}
	if gotUsername == "" {
		t.Fatalf("expected exactly one of A/B to have claimed the username, neither did")
	}
}
