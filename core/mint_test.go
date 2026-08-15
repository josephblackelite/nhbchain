package core

import (
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func newTestNode(t *testing.T) *Node {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

func assignRole(t *testing.T, node *Node, role string, addr [20]byte) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.SetRole(role, addr[:]); err != nil {
		t.Fatalf("set role %s: %v", role, err)
	}
}

func signVoucher(t *testing.T, key *crypto.PrivateKey, voucher MintVoucher) []byte {
	t.Helper()
	payload, err := voucher.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256(payload), key.PrivateKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

func TestMintWithSignatureInvalidSigner(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))

	rogueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("rogue key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	voucher := MintVoucher{
		InvoiceID: "inv-1",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "100",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, rogueKey, voucher)
	if _, err := node.MintWithSignature(&voucher, sig); err == nil || err != ErrMintInvalidSigner {
		t.Fatalf("expected ErrMintInvalidSigner, got %v", err)
	}
}

func TestMintWithSignatureReplayInvoice(t *testing.T) {
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	voucher := MintVoucher{
		InvoiceID: "inv-2",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "50",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	txHash, err := node.MintWithSignature(&voucher, sig)
	if err != nil {
		t.Fatalf("mint failed: %v", err)
	}
	if txHash == "" {
		t.Fatalf("expected tx hash")
	}
	if _, err := node.MintWithSignature(&voucher, sig); err == nil || err != ErrMintInvoiceUsed {
		t.Fatalf("expected ErrMintInvoiceUsed, got %v", err)
	}
}

func TestMintWithSignatureExpired(t *testing.T) {
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	voucher := MintVoucher{
		InvoiceID: "inv-exp",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "10",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(-time.Minute).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	if _, err := node.MintWithSignature(&voucher, sig); err == nil || err != ErrMintExpired {
		t.Fatalf("expected ErrMintExpired, got %v", err)
	}
}

func TestMintWithSignatureWrongChain(t *testing.T) {
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	voucher := MintVoucher{
		InvoiceID: "inv-chain",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "5",
		ChainID:   999999,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	if _, err := node.MintWithSignature(&voucher, sig); err == nil || err != ErrMintInvalidChainID {
		t.Fatalf("expected ErrMintInvalidChainID, got %v", err)
	}
}

func TestMintWithSignatureExecutesInBlock(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	voucher := MintVoucher{
		InvoiceID: "inv-block",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "125",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)

	txHash, err := node.MintWithSignature(&voucher, sig)
	if err != nil {
		t.Fatalf("mint failed: %v", err)
	}
	if txHash == "" {
		t.Fatalf("expected tx hash")
	}
	if got := len(node.mempool); got != 1 {
		t.Fatalf("expected 1 transaction in mempool, got %d", got)
	}
	hashBytes, err := node.mempool[0].Hash()
	if err != nil {
		t.Fatalf("hash transaction: %v", err)
	}
	expectedHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
	if txHash != expectedHash {
		t.Fatalf("expected tx hash %s, got %s", expectedHash, txHash)
	}

	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected mempool to be empty after commit, got %d", got)
	}

	account, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	amount, _ := voucher.AmountBig()
	if account.BalanceNHB.Cmp(amount) != 0 {
		t.Fatalf("expected NHB balance %s, got %s", amount, account.BalanceNHB)
	}

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	var used bool
	key := nhbstate.MintInvoiceKey(voucher.TrimmedInvoiceID())
	ok, kvErr := manager.KVGet(key, &used)
	node.stateMu.Unlock()
	if kvErr != nil {
		t.Fatalf("kv get: %v", kvErr)
	}
	if !ok || !used {
		t.Fatalf("expected invoice %s marked used", voucher.InvoiceID)
	}
}

func TestMintVoucherExpiresBeforeCommit(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	current := time.Now().UTC().Truncate(time.Second)
	node.SetTimeSource(func() time.Time { return current })
	defer node.SetTimeSource(nil)

	// Expire while waiting to be proposed.
	voucher := MintVoucher{
		InvoiceID: "inv-expire-mempool",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "50",
		ChainID:   MintChainID,
		Expiry:    current.Add(2 * time.Second).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	if _, err := node.MintWithSignature(&voucher, sig); err != nil {
		t.Fatalf("mint submission: %v", err)
	}
	if got := len(node.mempool); got != 1 {
		t.Fatalf("expected 1 transaction in mempool, got %d", got)
	}

	current = current.Add(5 * time.Second)
	proposed := node.GetMempool()
	if len(proposed) != 0 {
		t.Fatalf("expected expired mint to be dropped from proposal, got %d", len(proposed))
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected mempool to prune expired mint, got %d", got)
	}

	// Now include a mint in a block that expires before commit.
	second := MintVoucher{
		InvoiceID: "inv-expire-commit",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "75",
		ChainID:   MintChainID,
		Expiry:    current.Add(20 * time.Second).Unix(),
	}
	sig2 := signVoucher(t, minterKey, second)
	if _, err := node.MintWithSignature(&second, sig2); err != nil {
		t.Fatalf("mint submission (second): %v", err)
	}

	proposed = node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 mint proposal, got %d", len(proposed))
	}

	block, err := node.CreateBlock(append([]*types.Transaction(nil), proposed...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	block.Header.Timestamp = second.Expiry + 1
	current = time.Unix(block.Header.Timestamp, 0)

	err = node.CommitBlock(block)
	if err == nil || !errors.Is(err, ErrMintExpired) {
		t.Fatalf("expected commit error ErrMintExpired, got %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected mempool to drop expired mint after failed commit, got %d", got)
	}

	// Node should still be able to produce blocks after pruning the stale mint.
	// The rollback in CommitBlock resets the ephemeral test state to the
	// parent root, so reapply the minter role to mirror a persisted
	// configuration.
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	current = current.Add(5 * time.Second)
	third := MintVoucher{
		InvoiceID: "inv-success",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "90",
		ChainID:   MintChainID,
		Expiry:    current.Add(time.Hour).Unix(),
	}
	sig3 := signVoucher(t, minterKey, third)
	if _, err := node.MintWithSignature(&third, sig3); err != nil {
		t.Fatalf("mint submission (third): %v", err)
	}

	proposed = node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 mint proposal after recovery, got %d", len(proposed))
	}

	block2, err := node.CreateBlock(append([]*types.Transaction(nil), proposed...))
	if err != nil {
		t.Fatalf("create block (second attempt): %v", err)
	}
	if err := node.CommitBlock(block2); err != nil {
		t.Fatalf("commit block (second attempt): %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected mempool to be empty after successful commit, got %d", got)
	}
}

// TestMintMalformedPayloadPrunesInsteadOfAbortingProposal guards a gap an
// independent reviewer found in classifyProposalError's coverage: MintVoucher
// AmountBig() and CanonicalJSON() failures (empty amount, empty invoiceId,
// etc.) reach applyMintTransaction via node.mempool -> CreateBlock exactly
// like every other TxTypeMint validation error, but were previously returned
// bare (no sentinel), so a malformed mint transaction that somehow bypassed
// MintWithSignature's own cheap pre-validation (e.g. injected directly, or
// gossiped from a peer with transaction simulation disabled) would abort the
// entire block proposal instead of being pruned. Reproduces that exact
// bypass path directly, since MintWithSignature itself already rejects a
// malformed voucher before it ever reaches the mempool.
func TestMintMalformedPayloadPrunesInsteadOfAbortingProposal(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	malformed := MintVoucher{
		InvoiceID: "inv-malformed",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "", // empty -- AmountBig() fails, must be PRUNE not ABORT
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	payload, err := encodeMintTransaction(&malformed, []byte{0x01})
	if err != nil {
		t.Fatalf("encode malformed mint tx: %v", err)
	}
	badTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	if err := node.AddTransaction(badTx); err != nil {
		t.Fatalf("add malformed transaction (simulation disabled): %v", err)
	}

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	good := MintVoucher{
		InvoiceID: "inv-good",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "10",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, good)
	if _, err := node.MintWithSignature(&good, sig); err != nil {
		t.Fatalf("mint submission (good): %v", err)
	}

	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for one malformed mint: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected exactly the valid mint in the block, got %d txs", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected malformed mint pruned from mempool, got %d remaining", got)
	}

	account, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceNHB.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("expected the valid mint to succeed, got balance %s", account.BalanceNHB)
	}
}

// TestMintWithSignatureRejectsZNHB covers the Node-level convenience
// fast-path in MintWithSignature (used by the mint_withSignature RPC
// handler): a ZNHB mint request must be rejected immediately, before ever
// entering the mempool, with the same ErrMintZNHBNotMintable the consensus
// layer enforces. This is a UX/latency optimization layered on top of the
// real enforcement -- see TestApplyMintTransactionRejectsZNHBEvenWithRoleGranted
// for the load-bearing structural guarantee.
func TestMintWithSignatureRejectsZNHB(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_ZNHB", toAddress(minterKey))

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	voucher := MintVoucher{
		InvoiceID: "inv-znhb-early",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "ZNHB",
		Amount:    "10",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	if _, err := node.MintWithSignature(&voucher, sig); !errors.Is(err, ErrMintZNHBNotMintable) {
		t.Fatalf("expected ErrMintZNHBNotMintable, got %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected ZNHB mint to never enter mempool, got %d", got)
	}
}

// TestApplyMintTransactionRejectsZNHBEvenWithRoleGranted is the essential
// regression test for the fixed-supply-ZNHB product rule: a real, validly
// signed TxTypeMint transaction for token="ZNHB" must be rejected by
// applyMintTransaction itself (via StateProcessor.ApplyTransaction, the same
// entry point block execution uses -- not just a unit test on a helper
// function in isolation), even when the signer legitimately holds
// MINTER_ZNHB. Granting the role in setup here is deliberate: it proves the
// rejection is unconditional and structural, not merely "no one currently
// has the role today".
func TestApplyMintTransactionRejectsZNHBEvenWithRoleGranted(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	minterAddr := toAddress(minterKey)
	assignRole(t, node, "MINTER_ZNHB", minterAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	voucher := MintVoucher{
		InvoiceID: "inv-znhb-blocked",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "ZNHB",
		Amount:    "100",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	payload, err := encodeMintTransaction(&voucher, sig)
	if err != nil {
		t.Fatalf("encode mint tx: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()

	if applyErr == nil {
		t.Fatalf("expected ZNHB mint to be rejected, got nil error")
	}
	if !errors.Is(applyErr, ErrMintZNHBNotMintable) {
		t.Fatalf("expected ErrMintZNHBNotMintable, got %v", applyErr)
	}
	// Also confirm it still classifies as a pure-payload (PRUNE-safe) error
	// for classifyProposalError, same as every other unconditional mint
	// rejection in this file.
	if !errors.Is(applyErr, ErrMintInvalidPayload) {
		t.Fatalf("expected error to wrap ErrMintInvalidPayload, got %v", applyErr)
	}

	account, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB != nil && account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected recipient ZNHB balance to remain zero, got %s", account.BalanceZNHB)
	}
}

// TestApplyMintTransactionZNHBUnaffectedByEmissionCapOrPause proves the ZNHB
// rejection fires before ANY other check in applyMintTransaction -- in
// particular, before the token-mint-paused check and the emission-cap
// check -- by leaving the ZNHB token completely unregistered (no Token
// metadata, no emission cap configured) and confirming the error returned
// is still ErrMintZNHBNotMintable, not some other error that would only
// arise from those later checks actually running.
func TestApplyMintTransactionZNHBUnaffectedByEmissionCapOrPause(t *testing.T) {
	node := newTestNode(t)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_ZNHB", toAddress(minterKey))

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	voucher := MintVoucher{
		InvoiceID: "inv-znhb-blocked-2",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "znhb", // lower-case, exercises NormalizedToken()'s case-folding
		Amount:    "1",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	sig := signVoucher(t, minterKey, voucher)
	payload, err := encodeMintTransaction(&voucher, sig)
	if err != nil {
		t.Fatalf("encode mint tx: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()

	if !errors.Is(applyErr, ErrMintZNHBNotMintable) {
		t.Fatalf("expected ErrMintZNHBNotMintable, got %v", applyErr)
	}
}

// TestMintZNHBPrunedNotAborted drives a real ZNHB mint transaction through
// the actual mempool -> CreateBlock -> CommitBlock path (bypassing
// MintWithSignature's own convenience pre-check, the same way
// TestMintMalformedPayloadPrunesInsteadOfAbortingProposal does) to confirm
// the new rejection is classified PRUNE, not ABORT: a validator with a
// stray ZNHB mint transaction in its mempool must still be able to produce
// a block containing every other valid transaction, not have block
// production hang or fail outright.
func TestMintZNHBPrunedNotAborted(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_ZNHB", toAddress(minterKey))
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}

	znhbVoucher := MintVoucher{
		InvoiceID: "inv-znhb-pruned",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "ZNHB",
		Amount:    "500",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	znhbSig := signVoucher(t, minterKey, znhbVoucher)
	znhbPayload, err := encodeMintTransaction(&znhbVoucher, znhbSig)
	if err != nil {
		t.Fatalf("encode znhb mint tx: %v", err)
	}
	znhbTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     znhbPayload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	if err := node.AddTransaction(znhbTx); err != nil {
		t.Fatalf("add znhb transaction (simulation disabled): %v", err)
	}

	nhbVoucher := MintVoucher{
		InvoiceID: "inv-nhb-alongside",
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "20",
		ChainID:   MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	nhbSig := signVoucher(t, minterKey, nhbVoucher)
	if _, err := node.MintWithSignature(&nhbVoucher, nhbSig); err != nil {
		t.Fatalf("mint submission (nhb): %v", err)
	}

	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal for one ZNHB mint attempt: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected exactly the valid NHB mint in the block, got %d txs", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if got := len(node.mempool); got != 0 {
		t.Fatalf("expected ZNHB mint pruned from mempool, got %d remaining", got)
	}

	account, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceNHB.Cmp(big.NewInt(20)) != 0 {
		t.Fatalf("expected the NHB mint to succeed, got balance %s", account.BalanceNHB)
	}
	if account.BalanceZNHB != nil && account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected the ZNHB mint to be rejected, got balance %s", account.BalanceZNHB)
	}
}

func toAddress(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}
