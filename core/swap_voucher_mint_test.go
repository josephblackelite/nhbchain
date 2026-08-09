package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// TestSwapVoucherMintTxTypeByteValue pins down fix #1: the transaction type
// byte value must not collide with any other TxType. Round 1 assigned 0x19,
// which collided with the already-live TxTypeBuyZNHB. This test fails loudly
// if that ever regresses (e.g. a future merge reassigns 0x1E to something
// else, or another type reuses it).
func TestSwapVoucherMintTxTypeByteValue(t *testing.T) {
	if types.TxTypeSwapVoucherMint != 0x1E {
		t.Fatalf("expected TxTypeSwapVoucherMint == 0x1E, got 0x%02X", byte(types.TxTypeSwapVoucherMint))
	}
	seen := map[types.TxType]bool{}
	all := []types.TxType{
		types.TxTypeTransfer, types.TxTypeRegisterIdentity, types.TxTypeCreateEscrow,
		types.TxTypeReleaseEscrow, types.TxTypeRefundEscrow, types.TxTypeStake,
		types.TxTypeUnstake, types.TxTypeHeartbeat, types.TxTypeLockEscrow,
		types.TxTypeDisputeEscrow, types.TxTypeArbitrateRelease, types.TxTypeArbitrateRefund,
		types.TxTypeStakeClaim, types.TxTypeMint, types.TxTypeSwapPayoutReceipt,
		types.TxTypeTransferZNHB, types.TxTypeSwapMint, types.TxTypeSwapBurn,
		types.TxTypeLendingSupplyNHB, types.TxTypeLendingWithdrawNHB, types.TxTypeLendingDepositZNHB,
		types.TxTypeLendingWithdrawZNHB, types.TxTypeLendingBorrowNHB, types.TxTypeLendingRepayNHB,
		types.TxTypeBuyZNHB, types.TxTypeSetRewardBeneficiary, types.TxTypeRedeemNHB,
		types.TxTypeAttestRedemption, types.TxTypeLendingLiquidate, types.TxTypeSwapVoucherMint,
		types.TxTypePOSAuthorize, types.TxTypePOSCapture, types.TxTypePOSVoid, types.TxTypePOSRegistry,
	}
	for _, tt := range all {
		if seen[tt] {
			t.Fatalf("duplicate TxType byte value 0x%02X", byte(tt))
		}
		seen[tt] = true
	}
	if types.RequiresSignature(types.TxTypeSwapVoucherMint) {
		t.Fatalf("expected TxTypeSwapVoucherMint to be senderless (RequiresSignature=false), matching TxTypeMint")
	}
}

func swapVoucherTestVoucher(chainID uint64, recipient [20]byte, rate, orderID string) swap.VoucherV1 {
	rat, _ := new(big.Rat).SetString(rate)
	amount, _ := swap.ComputeMintAmount("100.00", rat, 18)
	return swap.VoucherV1{
		Domain:     swap.VoucherDomainV1,
		ChainID:    chainID,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     amount,
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       rate,
		OrderID:    orderID,
		Nonce:      []byte("nonce-" + orderID),
		Expiry:     time.Now().Add(time.Hour).Unix(),
	}
}

func signSwapVoucherCore(t *testing.T, key *crypto.PrivateKey, voucher swap.VoucherV1) []byte {
	t.Helper()
	sig, err := ethcrypto.Sign(voucher.Hash(), key.PrivateKey)
	if err != nil {
		t.Fatalf("sign voucher: %v", err)
	}
	return sig
}

func signedPriceProofCore(t *testing.T, key *crypto.PrivateKey, provider, rate string, ts time.Time) *swap.PriceProof {
	t.Helper()
	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", rate, ts.UTC().Unix(), nil)
	if err != nil {
		t.Fatalf("build price proof: %v", err)
	}
	hash, err := proof.Hash()
	if err != nil {
		t.Fatalf("hash price proof: %v", err)
	}
	sig, err := ethcrypto.Sign(hash, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign price proof: %v", err)
	}
	proof.Signature = sig
	return proof
}

func registerSwapPriceSignerCore(t *testing.T, node *Node, provider string, key *crypto.PrivateKey) {
	t.Helper()
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	err := manager.SwapSetPriceSigner(provider, addr)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("register price signer: %v", err)
	}
}

func setupSwapVoucherTestNode(t *testing.T) (*Node, *crypto.PrivateKey, *crypto.PrivateKey) {
	t.Helper()
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	minterAddr := toAddress(minterKey)
	assignRole(t, node, "MINTER_ZNHB", minterAddr)
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("register token: %v", err)
	}
	if err := manager.SetTokenMintAuthority("ZNHB", minterAddr[:]); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("set mint authority: %v", err)
	}
	node.stateMu.Unlock()
	node.SetSwapConfig(swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
	})
	oracleKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("oracle key: %v", err)
	}
	registerSwapPriceSignerCore(t, node, "nowpayments", oracleKey)
	return node, minterKey, oracleKey
}

// TestSwapVoucherMintExecutesInBlock is the genuine end-to-end test the round
// 1 review found missing: it drives Node.SwapSubmitVoucher -> AddTransaction
// -> mempool -> CreateBlock -> CommitBlock and only then checks the minted
// balance, ledger record, and events -- not a StateProcessor-only unit test.
func TestSwapVoucherMintExecutesInBlock(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-BLOCK")
	sig := signSwapVoucherCore(t, minterKey, voucher)
	proof := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", time.Now())

	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-BLOCK-1",
		PriceProof:   proof,
	}

	txHash, minted, err := node.SwapSubmitVoucher(submission)
	if err != nil {
		t.Fatalf("submit voucher: %v", err)
	}
	if minted {
		t.Fatalf("expected minted=false -- submission only enqueues now, consensus mints")
	}
	if txHash == "" {
		t.Fatalf("expected tx hash")
	}
	if got := len(node.mempool); got != 1 {
		t.Fatalf("expected 1 transaction in mempool, got %d", got)
	}
	if node.mempool[0].Type != types.TxTypeSwapVoucherMint {
		t.Fatalf("expected TxTypeSwapVoucherMint in mempool, got 0x%02X", byte(node.mempool[0].Type))
	}

	// Balance must not move pre-commit.
	preAccount, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if preAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected zero balance before commit, got %s", preAccount.BalanceZNHB)
	}

	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected mempool empty after commit, got %d", got)
	}

	account, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB.Cmp(voucher.Amount) != 0 {
		t.Fatalf("expected ZNHB balance %s, got %s", voucher.Amount, account.BalanceZNHB)
	}

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	ledger := swap.NewLedger(manager)
	record, ok, err := ledger.Get("PROVIDER-BLOCK-1")
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("ledger get: %v", err)
	}
	if !ok {
		t.Fatalf("expected voucher ledger record to exist after commit")
	}
	if record.Status != swap.VoucherStatusMinted {
		t.Fatalf("expected minted status, got %s", record.Status)
	}
	if record.MintAmountWei.Cmp(voucher.Amount) != 0 {
		t.Fatalf("expected ledger mint amount %s, got %s", voucher.Amount, record.MintAmountWei)
	}

	foundMinted := false
	for _, evt := range node.state.events {
		if evt.Type == "swap.minted" {
			foundMinted = true
		}
	}
	if !foundMinted {
		t.Fatalf("expected a swap.minted event to have been recorded during commit")
	}
}

// TestSwapVoucherMintMandatoryPriceProofSignature covers fix #3: submitting
// without ANY registered price signer for the provider must be rejected
// deterministically, even though the general swap.risk.PriceProofSignatureRequired
// toggle defaults to false in setupSwapVoucherTestNode's config -- this
// transaction type requires a valid signed price proof unconditionally.
func TestSwapVoucherMintMandatoryPriceProofSignature(t *testing.T) {
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	minterAddr := toAddress(minterKey)
	assignRole(t, node, "MINTER_ZNHB", minterAddr)
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("register token: %v", err)
	}
	if err := manager.SetTokenMintAuthority("ZNHB", minterAddr[:]); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("set mint authority: %v", err)
	}
	node.stateMu.Unlock()
	// Deliberately do NOT set PriceProofSignatureRequired -- defaults false,
	// matching the real deployed config.toml this fix targets.
	node.SetSwapConfig(swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
	})

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)
	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-NOSIG")
	sig := signSwapVoucherCore(t, minterKey, voucher)

	// No price signer registered at all, and no priceProof supplied -- this
	// is exactly the state of a freshly-deployed production chain today.
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-NOSIG-1",
	}
	if _, _, err := node.SwapSubmitVoucher(submission); !errors.Is(err, ErrSwapPriceProofRequired) {
		t.Fatalf("expected ErrSwapPriceProofRequired with no price proof, got %v", err)
	}

	// Supplying a self-signed, unregistered "oracle" proof must also fail --
	// an attacker (or a caller who never bothered registering a real oracle)
	// cannot simply supply their own arbitrary rate.
	attackerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("attacker key: %v", err)
	}
	forgedProof := signedPriceProofCore(t, attackerKey, "nowpayments", "0.0001", time.Now())
	submission2 := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-NOSIG-2",
		PriceProof:   forgedProof,
	}
	if _, _, err := node.SwapSubmitVoucher(submission2); !errors.Is(err, ErrSwapPriceProofSignerUnknown) {
		t.Fatalf("expected ErrSwapPriceProofSignerUnknown for an unregistered signer, got %v", err)
	}

	// Now register the real signer and confirm a properly signed proof
	// succeeds (enqueues) -- proving the guard is precise, not just
	// permanently closed.
	oracleKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("oracle key: %v", err)
	}
	registerSwapPriceSignerCore(t, node, "nowpayments", oracleKey)
	validProof := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", time.Now())
	submission3 := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-NOSIG-3",
		PriceProof:   validProof,
	}
	if _, _, err := node.SwapSubmitVoucher(submission3); err != nil {
		t.Fatalf("expected success once a real signer is registered, got %v", err)
	}
}

// TestSwapVoucherMintDuplicateDoesNotBlockProposal covers fix #2: a
// TxTypeSwapVoucherMint transaction that becomes permanently unexecutable
// (its providerTxID was already committed by a different transaction --
// simulating the losing side of the two-validators-raced-the-same-voucher
// scenario) must be pruned from the mempool during CreateBlock instead of
// aborting the ENTIRE proposal and blocking every other pending transaction,
// for every subsequent round, until the voucher's own expiry.
func TestSwapVoucherMintDuplicateDoesNotBlockProposal(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	// Voucher A: submit, mine, and commit -- providerTxID "RACE-1" is now
	// durably recorded in the ledger.
	voucherA := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-RACE-A")
	sigA := signSwapVoucherCore(t, minterKey, voucherA)
	proofA := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", time.Now())
	submissionA := &swap.VoucherSubmission{
		Voucher: &voucherA, Signature: sigA, Provider: "nowpayments",
		ProviderTxID: "RACE-1", PriceProof: proofA,
	}
	if _, _, err := node.SwapSubmitVoucher(submissionA); err != nil {
		t.Fatalf("submit voucher A: %v", err)
	}
	blockA, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block A: %v", err)
	}
	if err := node.CommitBlock(blockA); err != nil {
		t.Fatalf("commit block A: %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected empty mempool after committing A, got %d", got)
	}

	// Voucher B: same providerTxID "RACE-1" (simulating the losing side of a
	// cross-validator race -- a distinct transaction, different order/nonce/
	// price proof, that lost the race to be included first), submitted
	// AFTER A already committed, so the local same-mempool dedup check in
	// addTransaction cannot catch it (nothing conflicting is resident in the
	// mempool at submission time). It must still be accepted into the
	// mempool (passes shallow + simulated pre-checks against the current,
	// A-inclusive state -- wait, simulation SHOULD catch this too via
	// validateTransaction; the real race this test targets is the
	// mempool-resident case CreateBlock's ApplyTransaction hits when the
	// state changed since a transaction was accepted. Bypass the
	// synchronous simulation deliberately here to reproduce that scenario:
	// build voucher B's transaction directly and inject it into the mempool
	// alongside a genuinely valid, unrelated voucher C.
	voucherB := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-RACE-B")
	sigB := signSwapVoucherCore(t, minterKey, voucherB)
	proofB := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", time.Now())
	submissionB := &swap.VoucherSubmission{
		Voucher: &voucherB, Signature: sigB, Provider: "nowpayments",
		ProviderTxID: "RACE-1", PriceProof: proofB,
	}
	payloadB, err := encodeSwapVoucherMintTransaction(submissionB)
	if err != nil {
		t.Fatalf("encode voucher B: %v", err)
	}
	txB := &types.Transaction{
		ChainID: types.NHBChainID(), Type: types.TxTypeSwapVoucherMint,
		Data: payloadB, GasLimit: 0, GasPrice: big.NewInt(0),
	}

	voucherC := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-RACE-C")
	sigC := signSwapVoucherCore(t, minterKey, voucherC)
	proofC := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", time.Now())
	submissionC := &swap.VoucherSubmission{
		Voucher: &voucherC, Signature: sigC, Provider: "nowpayments",
		ProviderTxID: "RACE-2", PriceProof: proofC,
	}
	// Submit C through the normal path -- it's genuinely valid.
	if _, _, err := node.SwapSubmitVoucher(submissionC); err != nil {
		t.Fatalf("submit voucher C: %v", err)
	}

	// Inject B directly, bypassing AddTransaction's own simulation/dedup, to
	// reproduce the case where B slipped into the mempool (e.g. arrived via
	// gossip a moment before A's block synced in) and is now stale relative
	// to committed state.
	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, txB)
	node.mempoolMu.Unlock()

	pending := append([]*types.Transaction(nil), node.mempool...)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending transactions (B and C), got %d", len(pending))
	}

	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for one permanently-invalid duplicate: %v", err)
	}
	if block == nil {
		t.Fatalf("expected a block to be produced")
	}

	// C must have been included; B must have been pruned, not included.
	foundC := false
	for _, tx := range block.Transactions {
		submission, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			continue
		}
		if submission.ProviderTxID == "RACE-2" {
			foundC = true
		}
		if submission.ProviderTxID == "RACE-1" && submission.Voucher.OrderID == "ORDER-RACE-B" {
			t.Fatalf("expected the permanently-invalid duplicate (voucher B) to be pruned, not included in the block")
		}
	}
	if !foundC {
		t.Fatalf("expected the unrelated valid transaction (voucher C) to still be included in the block")
	}

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// The pruned duplicate must not be stuck in the mempool either -- it was
	// dropped, not left to be re-proposed and fail identically forever.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	node.mempoolMu.Unlock()
	if stillPending != 0 {
		t.Fatalf("expected mempool empty after commit (pruned duplicate must not linger), got %d", stillPending)
	}
}

// TestSwapVoucherMintStalePriceProofDoesNotBlockProposal closes the round 3
// gap: ErrSwapPriceProofStale has the exact same "monotonic, once-true-
// always-true" property as ErrSwapExpired (already prunable since round 2)
// -- a price proof's timestamp is fixed at signing time and block time only
// ever increases, so once a proof exceeds swap.Config.MaxQuoteAgeSeconds
// (120s here, matching setupSwapVoucherTestNode's config and the real
// production default) it can never become fresh again for that specific
// transaction. An entirely ordinary, non-duplicate, non-adversarial voucher
// that simply sits in the mempool longer than that window -- routine under
// any mempool backlog or round delay, no race required -- must be pruned
// from the proposal like any other permanently-doomed transaction, not
// treated as a hard error that aborts the ENTIRE block and leaves the
// transaction stuck to fail identically every round until the voucher's
// own, much longer, expiry.
func TestSwapVoucherMintStalePriceProofDoesNotBlockProposal(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	current := time.Now().UTC().Truncate(time.Second)
	node.SetTimeSource(func() time.Time { return current })
	defer node.SetTimeSource(nil)

	// Voucher A: submitted now, with a price proof signed now. Its own
	// voucher expiry is a full hour out (swapVoucherTestVoucher's default)
	// -- only the price proof's much shorter freshness window is meant to
	// be exercised here.
	voucherA := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-STALEPROOF-A")
	sigA := signSwapVoucherCore(t, minterKey, voucherA)
	proofA := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", current)
	submissionA := &swap.VoucherSubmission{
		Voucher: &voucherA, Signature: sigA, Provider: "nowpayments",
		ProviderTxID: "STALEPROOF-1", PriceProof: proofA,
	}
	if _, _, err := node.SwapSubmitVoucher(submissionA); err != nil {
		t.Fatalf("submit voucher A: %v", err)
	}
	if got := len(node.mempool); got != 1 {
		t.Fatalf("expected 1 transaction in mempool after submitting A, got %d", got)
	}

	// Advance the node's clock past MaxQuoteAgeSeconds (120s), simulating
	// ordinary mempool residency time under backlog or a slow round --
	// deliberately NOT touching the voucher's own, much longer, expiry.
	current = current.Add(130 * time.Second)

	// Voucher B: a second, entirely unrelated, genuinely valid transaction
	// submitted after the clock advance, with a freshly-signed price proof
	// (as a real caller re-quoting at proposal time would produce). It must
	// still land in the same block as proof that only the stale
	// transaction is pruned, not the whole proposal.
	voucherB := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.10", "ORDER-STALEPROOF-B")
	sigB := signSwapVoucherCore(t, minterKey, voucherB)
	proofB := signedPriceProofCore(t, oracleKey, "nowpayments", "0.10", current)
	submissionB := &swap.VoucherSubmission{
		Voucher: &voucherB, Signature: sigB, Provider: "nowpayments",
		ProviderTxID: "STALEPROOF-2", PriceProof: proofB,
	}
	if _, _, err := node.SwapSubmitVoucher(submissionB); err != nil {
		t.Fatalf("submit voucher B: %v", err)
	}

	pending := append([]*types.Transaction(nil), node.mempool...)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending transactions (A and B), got %d", len(pending))
	}

	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for one stale price proof: %v", err)
	}
	if block == nil {
		t.Fatalf("expected a block to be produced")
	}

	// B must have been included; A (stale price proof) must have been
	// pruned, not included.
	foundB := false
	for _, tx := range block.Transactions {
		submission, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			continue
		}
		if submission.ProviderTxID == "STALEPROOF-2" {
			foundB = true
		}
		if submission.ProviderTxID == "STALEPROOF-1" {
			t.Fatalf("expected the stale-price-proof transaction (voucher A) to be pruned, not included in the block")
		}
	}
	if !foundB {
		t.Fatalf("expected the unrelated valid transaction (voucher B) to still be included in the block")
	}

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// The pruned stale-price-proof transaction must not be stuck in the
	// mempool either -- it was dropped, not left to be re-proposed and fail
	// identically every round until the voucher's own expiry.
	node.mempoolMu.Lock()
	stillPending := len(node.mempool)
	node.mempoolMu.Unlock()
	if stillPending != 0 {
		t.Fatalf("expected mempool empty after commit (pruned stale-price-proof tx must not linger), got %d", stillPending)
	}

	// And the genuinely valid mint (voucher B) must have actually landed.
	account, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB.Cmp(voucherB.Amount) != 0 {
		t.Fatalf("expected ZNHB balance %s from voucher B, got %s", voucherB.Amount, account.BalanceZNHB)
	}
}
