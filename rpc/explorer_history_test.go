package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
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
		Address      string                      `json:"address"`
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

func fetchHistory(t *testing.T, server *Server, address string) []ExplorerTransactionResult {
	t.Helper()
	param, err := json.Marshal(address)
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
		Address      string                      `json:"address"`
		Transactions []ExplorerTransactionResult `json:"transactions"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result.Transactions
}

// TestGetTransactionHistoryDirectionField is the regression test for a real
// production bug: ExplorerTransactionResult had no Direction field at all,
// forcing the frontend to guess incoming/outgoing from the transaction type
// string -- a guess that silently defaulted to "receive" (positive amount)
// for any type it didn't recognize, which is how a real outgoing ZNHB
// transfer or NHB redemption could render as money coming IN. Direction is
// now computed server-side, where From/To/the queried address are all
// unambiguously known.
func TestGetTransactionHistoryDirectionField(t *testing.T) {
	node, txHash, senderAddr, recipientAddr := buildTestChainWithTransfer(t, 5)
	server := newTestServer(t, node, nil, ServerConfig{})

	senderCrypto, err := crypto.NewAddress(crypto.NHBPrefix, senderAddr[:])
	if err != nil {
		t.Fatalf("build bech32 sender address: %v", err)
	}
	recipientCrypto, err := crypto.NewAddress(crypto.NHBPrefix, recipientAddr[:])
	if err != nil {
		t.Fatalf("build bech32 recipient address: %v", err)
	}

	senderHistory := fetchHistory(t, server, senderCrypto.String())
	if len(senderHistory) != 1 || senderHistory[0].Hash != ensureHexPrefix(txHash) {
		t.Fatalf("unexpected sender history: %+v", senderHistory)
	}
	if senderHistory[0].Direction != "outgoing" {
		t.Fatalf("expected sender direction outgoing, got %q", senderHistory[0].Direction)
	}

	recipientHistory := fetchHistory(t, server, recipientCrypto.String())
	if len(recipientHistory) != 1 || recipientHistory[0].Hash != ensureHexPrefix(txHash) {
		t.Fatalf("unexpected recipient history: %+v", recipientHistory)
	}
	if recipientHistory[0].Direction != "incoming" {
		t.Fatalf("expected recipient direction incoming, got %q", recipientHistory[0].Direction)
	}
}

// lendingTestPayload mirrors lendingNativePayload (core/lending_native.go,
// unexported) closely enough to round-trip through
// StateProcessor.decodeLendingPayload's json.Unmarshal -- this package can't
// import that unexported type directly, so it's redefined locally the same
// way TestGetTransactionHistoryBuyZNHBShowsAdminCredit above redefines
// BuyZNHB's payload shape.
type lendingTestPayload struct {
	PoolID string `json:"poolId,omitempty"`
}

// buildLendingTx signs a lending-pool transaction (Supply/Withdraw/Deposit/
// Borrow/Repay) the same way the real wallet does: `to` is the zero address
// (there is no real third-party counterparty -- the pool interaction is
// entirely encoded in the tx type + JSON payload), and the payload is plain
// JSON, not RLP (see core/lending_native.go's decodeLendingPayload).
func buildLendingTx(t *testing.T, key *crypto.PrivateKey, txType types.TxType, nonce uint64, value *big.Int) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(lendingTestPayload{PoolID: "default"})
	if err != nil {
		t.Fatalf("marshal lending payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    new(big.Int).Set(value),
		Data:     data,
		GasLimit: 200_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign lending tx: %v", err)
	}
	return tx
}

// TestGetTransactionHistoryLendingDirectionField is the regression test for
// a real, live-reported production bug: a successful $150 borrow's
// transaction-detail view showed "Amount -150.00 NHB / Direction Outgoing"
// even though the funds landed IN the borrower's own balance. The root
// cause was buildAddressActivity's direction heuristic assuming
// From==queried-address always means "outgoing", which is true for an
// ordinary transfer but wrong for a lending-pool interaction where the
// caller is always the tx's own signer regardless of which way the money
// actually moves. This exercises the full lending lifecycle (supply,
// deposit collateral, borrow, repay, withdraw collateral, withdraw
// liquidity) through real blocks and checks each leg's Direction from the
// acting account's own history.
func TestGetTransactionHistoryLendingDirectionField(t *testing.T) {
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
	node.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
	})
	node.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	supplierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate supplier key: %v", err)
	}
	borrowerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate borrower key: %v", err)
	}
	var supplierAddr, borrowerAddr [20]byte
	copy(supplierAddr[:], supplierKey.PubKey().Address().Bytes())
	copy(borrowerAddr[:], borrowerKey.PubKey().Address().Bytes())

	supplyAmount, _ := new(big.Int).SetString("1500000000000000000000", 10)      // 1500 NHB
	collateralAmount, _ := new(big.Int).SetString("50000000000000000000000", 10) // 50000 ZNHB
	borrowAmount, _ := new(big.Int).SetString("150000000000000000000", 10)       // 150 NHB
	// A small NHB buffer on the borrower, on top of what borrowing credits
	// them: interest accrues per block (even over the couple of one-second
	// blocks between borrow and repay here), so repaying exactly
	// borrowAmount would leave a wei-scale residual debt and make the
	// subsequent full collateral withdrawal fail its health-factor check.
	// The repay leg below sends borrowAmount+repayBuffer; Engine.Repay caps
	// to the real outstanding debt, so this just needs to comfortably cover
	// whatever interest accrued, not be exact.
	repayBuffer, _ := new(big.Int).SetString("10000000000000000000", 10) // 10 NHB

	if err := node.WithState(func(m *nhbstate.Manager) error {
		if err := m.PutAccount(supplierAddr[:], &types.Account{BalanceNHB: new(big.Int).Set(supplyAmount), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			return err
		}
		return m.PutAccount(borrowerAddr[:], &types.Account{BalanceNHB: new(big.Int).Set(repayBuffer), BalanceZNHB: new(big.Int).Set(collateralAmount), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	type leg struct {
		name    string
		tx      *types.Transaction
		acting  [20]byte
		wantDir string
	}
	legs := []leg{
		{"supply", buildLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, supplyAmount), supplierAddr, "outgoing"},
		{"deposit collateral", buildLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, collateralAmount), borrowerAddr, "outgoing"},
		{"borrow", buildLendingTx(t, borrowerKey, types.TxTypeLendingBorrowNHB, 1, borrowAmount), borrowerAddr, "incoming"},
		{"repay", buildLendingTx(t, borrowerKey, types.TxTypeLendingRepayNHB, 2, new(big.Int).Add(borrowAmount, repayBuffer)), borrowerAddr, "outgoing"},
		{"withdraw collateral", buildLendingTx(t, borrowerKey, types.TxTypeLendingWithdrawZNHB, 3, collateralAmount), borrowerAddr, "incoming"},
		{"withdraw liquidity", buildLendingTx(t, supplierKey, types.TxTypeLendingWithdrawNHB, 1, supplyAmount), supplierAddr, "incoming"},
	}

	txHashes := make(map[string]string, len(legs))
	for _, l := range legs {
		hashBytes, err := l.tx.Hash()
		if err != nil {
			t.Fatalf("%s: hash tx: %v", l.name, err)
		}
		txHashes[l.name] = ensureHexPrefix(hex.EncodeToString(hashBytes))

		block, err := node.CreateBlock([]*types.Transaction{l.tx})
		if err != nil {
			t.Fatalf("%s: create block: %v", l.name, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("%s: commit block: %v", l.name, err)
		}
	}

	server := newTestServer(t, node, nil, ServerConfig{})
	supplierCrypto, err := crypto.NewAddress(crypto.NHBPrefix, supplierAddr[:])
	if err != nil {
		t.Fatalf("build supplier address: %v", err)
	}
	borrowerCrypto, err := crypto.NewAddress(crypto.NHBPrefix, borrowerAddr[:])
	if err != nil {
		t.Fatalf("build borrower address: %v", err)
	}

	supplierHistory := fetchHistory(t, server, supplierCrypto.String())
	borrowerHistory := fetchHistory(t, server, borrowerCrypto.String())

	findByHash := func(history []ExplorerTransactionResult, hash string) *ExplorerTransactionResult {
		for i := range history {
			if history[i].Hash == hash {
				return &history[i]
			}
		}
		return nil
	}

	for _, l := range legs {
		var history []ExplorerTransactionResult
		switch l.acting {
		case supplierAddr:
			history = supplierHistory
		case borrowerAddr:
			history = borrowerHistory
		}
		record := findByHash(history, txHashes[l.name])
		if record == nil {
			t.Fatalf("%s: transaction %s not found in acting account's history", l.name, txHashes[l.name])
		}
		if record.Direction != l.wantDir {
			t.Fatalf("%s: expected direction %q, got %q", l.name, l.wantDir, record.Direction)
		}
	}
}

// TestGetTransactionHistoryBuyZNHBShowsAdminCredit is the regression test
// for the other real production complaint: "wallet correctly credits admin
// wallet but there is no history whatsoever" for a NHB->ZNHB purchase. The
// admin wallet is never a BuyZNHB transaction's own From/To (see
// buildAdminBuyZNHBCreditRecord's doc comment), so before this fix the
// admin's own history query found nothing for it at all. Also confirms
// formatTxType/assetLabel actually resolve BuyZNHB to a friendly name
// instead of falling through to raw hex ("0x19").
//
// Also covers the buyZNHBCostMetaPrefix index (core/blockchain.go,
// core/node.go's commitBlock) end to end: the admin's history for a BuyZNHB
// transaction committed after that index shipped must show BOTH legs --
// outgoing ZNHB (buildAdminBuyZNHBCreditRecord, pre-existing) and incoming
// NHB (buildAdminBuyZNHBDebitRecord, new) -- with the NHB amount checked
// against the buyer's own real BalanceNHB delta read independently via
// node.WithState, not against anything buildAdminBuyZNHBDebitRecord itself
// produced, so this test cannot pass merely by echoing back whatever value
// the code under test happened to persist.
func TestGetTransactionHistoryBuyZNHBShowsAdminCredit(t *testing.T) {
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

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	if err := node.ConfigureAdminWalletForTests(adminAddr); err != nil {
		t.Fatalf("configure admin wallet: %v", err)
	}

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	var buyerAddr [20]byte
	copy(buyerAddr[:], buyerKey.PubKey().Address().Bytes())
	buyerBalance, ok := new(big.Int).SetString("1000000000000000000000", 10) // 1000 NHB
	if !ok {
		t.Fatalf("invalid buyerBalance constant")
	}
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(buyerAddr[:], &types.Account{BalanceNHB: buyerBalance, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	znhbAmount := big.NewInt(1_000_000_000_000_000_000) // 1 ZNHB (18 decimals)
	// Comfortably above the real curve cost for buying 1 ZNHB from the very
	// start of the curve (~0.05 NHB per the test failure this replaced) --
	// just a generous slippage ceiling, not meant to be tight.
	maxNHBAmount, ok := new(big.Int).SetString("100000000000000000000", 10) // 100 NHB
	if !ok {
		t.Fatalf("invalid maxNHBAmount constant")
	}
	payload := struct {
		ZNHBAmount   *big.Int `json:"znhbAmount"`
		MaxNHBAmount *big.Int `json:"maxNHBAmount"`
		QuoteID      string   `json:"quoteId,omitempty"`
	}{ZNHBAmount: znhbAmount, MaxNHBAmount: maxNHBAmount}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeBuyZNHB,
		Nonce:    0,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	txHashBytes, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}
	txHash := ensureHexPrefix(hex.EncodeToString(txHashBytes))

	block, err := node.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("create block with buyZNHB tx: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block with buyZNHB tx: %v", err)
	}

	// Independently-verifiable ground truth for the NHB cost: read the
	// buyer's real post-commit balance straight out of state (not through
	// any of the code paths under test) and diff it against their known
	// starting balance.
	var buyerPostBalance *big.Int
	if err := node.WithState(func(m *nhbstate.Manager) error {
		account, err := m.GetAccount(buyerAddr[:])
		if err != nil {
			return err
		}
		buyerPostBalance = account.BalanceNHB
		return nil
	}); err != nil {
		t.Fatalf("read buyer post-purchase balance: %v", err)
	}
	actualNHBCost := new(big.Int).Sub(buyerBalance, buyerPostBalance)
	if actualNHBCost.Sign() <= 0 {
		t.Fatalf("expected the purchase to have actually cost the buyer NHB, got delta %s", actualNHBCost)
	}

	server := newTestServer(t, node, nil, ServerConfig{})

	buyerCrypto, err := crypto.NewAddress(crypto.NHBPrefix, buyerAddr[:])
	if err != nil {
		t.Fatalf("build bech32 buyer address: %v", err)
	}
	adminCrypto, err := crypto.NewAddress(crypto.NHBPrefix, adminAddr[:])
	if err != nil {
		t.Fatalf("build bech32 admin address: %v", err)
	}

	buyerHistory := fetchHistory(t, server, buyerCrypto.String())
	if len(buyerHistory) != 1 {
		t.Fatalf("expected exactly one buyer history entry, got %d: %+v", len(buyerHistory), buyerHistory)
	}
	if buyerHistory[0].Type != "BuyZNHB" {
		t.Fatalf("expected buyer's entry type BuyZNHB (not raw hex), got %q", buyerHistory[0].Type)
	}
	if buyerHistory[0].Asset != "ZNHB" || buyerHistory[0].Amount != znhbAmount.String() {
		t.Fatalf("unexpected buyer entry asset/amount: %+v", buyerHistory[0])
	}
	if buyerHistory[0].Direction != "outgoing" {
		t.Fatalf("expected buyer direction outgoing (NHB left their balance), got %q", buyerHistory[0].Direction)
	}

	adminHistory := fetchHistory(t, server, adminCrypto.String())
	if len(adminHistory) != 2 {
		t.Fatalf("expected exactly two synthesized admin history entries (outgoing ZNHB + incoming NHB), got %d: %+v", len(adminHistory), adminHistory)
	}
	var znhbLeg, nhbLeg *ExplorerTransactionResult
	for i := range adminHistory {
		switch adminHistory[i].Asset {
		case "ZNHB":
			znhbLeg = &adminHistory[i]
		case "NHB":
			nhbLeg = &adminHistory[i]
		}
	}
	if znhbLeg == nil {
		t.Fatalf("expected an outgoing-ZNHB admin entry, got %+v", adminHistory)
	}
	if nhbLeg == nil {
		t.Fatalf("expected an incoming-NHB admin entry (buyZNHBCostMetaPrefix index), got %+v", adminHistory)
	}

	if znhbLeg.Hash != txHash {
		t.Fatalf("expected admin ZNHB entry to reference the same buyZNHB tx hash: got %s want %s", znhbLeg.Hash, txHash)
	}
	if znhbLeg.Type != "BuyZNHB" {
		t.Fatalf("expected admin's synthesized ZNHB entry type BuyZNHB, got %q", znhbLeg.Type)
	}
	if znhbLeg.Amount != znhbAmount.String() {
		t.Fatalf("unexpected admin ZNHB entry amount: %+v", znhbLeg)
	}
	if znhbLeg.Direction != "outgoing" {
		t.Fatalf("expected admin ZNHB direction outgoing (ZNHB left the admin wallet), got %q", znhbLeg.Direction)
	}
	if znhbLeg.To != buyerCrypto.String() {
		t.Fatalf("expected admin ZNHB entry's To to be the buyer: got %s want %s", znhbLeg.To, buyerCrypto.String())
	}

	if nhbLeg.Hash != txHash {
		t.Fatalf("expected admin NHB entry to reference the same buyZNHB tx hash: got %s want %s", nhbLeg.Hash, txHash)
	}
	if nhbLeg.Type != "BuyZNHB" {
		t.Fatalf("expected admin's synthesized NHB entry type BuyZNHB, got %q", nhbLeg.Type)
	}
	if nhbLeg.Direction != "incoming" {
		t.Fatalf("expected admin NHB direction incoming (NHB entered the admin wallet), got %q", nhbLeg.Direction)
	}
	if nhbLeg.From != buyerCrypto.String() {
		t.Fatalf("expected admin NHB entry's From to be the buyer: got %s want %s", nhbLeg.From, buyerCrypto.String())
	}
	if nhbLeg.To != adminCrypto.String() {
		t.Fatalf("expected admin NHB entry's To to be the admin wallet: got %s want %s", nhbLeg.To, adminCrypto.String())
	}
	// The load-bearing assertion: the persisted/surfaced NHB amount must
	// exactly match what was actually debited from the buyer's own balance,
	// independently read via node.WithState above -- not merely internally
	// self-consistent with whatever buildAdminBuyZNHBDebitRecord computed.
	if nhbLeg.Amount != actualNHBCost.String() {
		t.Fatalf("admin NHB entry amount = %s, want %s (buyer's actual NHB balance delta)", nhbLeg.Amount, actualNHBCost.String())
	}
}
