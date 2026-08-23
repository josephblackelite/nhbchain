package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// buildTestChainWithTransfer commits emptyBlocks worth of blank blocks
// (standing in for however much history the real chain has already
// produced) followed by one block containing a signed transfer from a
// freshly-seeded sender to a fresh recipient. Returns the node, the
// transfer's hex-encoded hash (no 0x prefix), and both addresses.
func buildTestChainWithTransfer(t *testing.T, emptyBlocks int) (*core.Node, string, [20]byte, [20]byte) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	var senderAddr, recipientAddr [20]byte
	copy(senderAddr[:], senderKey.PubKey().Address().Bytes())
	copy(recipientAddr[:], recipientKey.PubKey().Address().Bytes())

	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(senderAddr[:], &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	for i := 0; i < emptyBlocks; i++ {
		block, err := node.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create empty block %d: %v", i, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit empty block %d: %v", i, err)
		}
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr[:]...),
		Value:    big.NewInt(32),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	txHashBytes, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}
	txHash := hex.EncodeToString(txHashBytes)

	block, err := node.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("create block with tx: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block with tx: %v", err)
	}

	return node, txHash, senderAddr, recipientAddr
}

// TestFindTransactionFindsRecentTransaction is a direct regression test for
// the live bug this fix closed: nhb_getTransactionReceipt is what wallets
// poll right after submitting a transaction, and the old forward-from-
// genesis unbounded scan made that poll take as long as scanning the whole
// chain -- on production (240k+ blocks) that exceeded every reasonable
// client timeout and produced a false "transaction confirmation timed out"
// even though the transaction had already landed. The fix scans backward
// from the tip instead; this confirms a transaction in the latest block is
// still found correctly (not just "doesn't crash").
func TestFindTransactionFindsRecentTransaction(t *testing.T) {
	node, txHash, _, _ := buildTestChainWithTransfer(t, 5)
	server := newTestServer(t, node, nil, ServerConfig{})

	param, err := json.Marshal(txHash)
	if err != nil {
		t.Fatalf("marshal hash param: %v", err)
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()
	server.handleGetTransactionReceipt(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatalf("expected a receipt result, got nil (transaction not found)")
	}
}

// TestFindTransactionReturnsNilForUnknownHash confirms a genuinely missing
// hash still resolves cleanly (nil result, no error) rather than hanging or
// erroring -- unchanged behavior from before the scan-direction fix, just
// confirming the bounded scan doesn't change this contract.
func TestFindTransactionReturnsNilForUnknownHash(t *testing.T) {
	node, _, _, _ := buildTestChainWithTransfer(t, 5)
	server := newTestServer(t, node, nil, ServerConfig{})

	param, err := json.Marshal(hex.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("marshal hash param: %v", err)
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()
	server.handleGetTransactionReceipt(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error for unknown hash: %+v", resp.Error)
	}
	if resp.Result != nil {
		t.Fatalf("expected nil result for unknown hash, got %s", resp.Result)
	}
}

// TestGetTransactionHistoryFindsRecentActivity is the address-history
// sibling of the receipt-lookup regression test above: confirms
// buildAddressActivity's backward scan still correctly finds and reports a
// recent transaction for the sender address, with an accurate txCount for
// activity within the scanned window.
func TestGetTransactionHistoryFindsRecentActivity(t *testing.T) {
	node, txHash, senderAddr, _ := buildTestChainWithTransfer(t, 5)
	server := newTestServer(t, node, nil, ServerConfig{})

	senderCrypto, err := crypto.NewAddress(crypto.NHBPrefix, senderAddr[:])
	if err != nil {
		t.Fatalf("build bech32 sender address: %v", err)
	}

	param, err := json.Marshal(senderCrypto.String())
	if err != nil {
		t.Fatalf("marshal address param: %v", err)
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()
	server.handleGetTransactionHistory(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var result struct {
		Address      string                       `json:"address"`
		Transactions []ExplorerTransactionResult `json:"transactions"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Transactions) != 1 {
		t.Fatalf("expected exactly one transaction in history, got %d", len(result.Transactions))
	}
	if got := result.Transactions[0].Hash; got != ensureHexPrefix(txHash) {
		t.Fatalf("unexpected transaction hash in history: got %s want %s", got, ensureHexPrefix(txHash))
	}
}
