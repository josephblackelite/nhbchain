package core

import (
	"encoding/hex"
	"errors"
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

func toAddress(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}
