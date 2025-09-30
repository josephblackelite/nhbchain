package core

import (
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
	node, err := NewNode(db, validatorKey, "", true)
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

func toAddress(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}
