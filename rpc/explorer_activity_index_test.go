package rpc

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// buildActivityIndexTestChain seeds a sender with both NHB and ZNHB balance
// and commits two blocks: one plain NHB TxTypeTransfer (payment-like, asset
// "NHB" -- must count toward TotalPayments but NOT TotalZNHBFlow) and one
// TxTypeTransferZNHB (not payment-like, asset "ZNHB" -- must count toward
// TotalZNHBFlow but NOT TotalPayments). This exercises the real asymmetry in
// what these two totals measure (see isPaymentLikeType vs assetLabel):
// they are independent filters over the same transaction stream, not the
// same transaction reclassified two ways.
func buildActivityIndexTestChain(t *testing.T) (node *core.Node, nhbAmount, znhbAmount *big.Int) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err = core.NewNode(db, validatorKey, "", true, false)
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
		return m.PutAccount(senderAddr[:], &types.Account{
			BalanceNHB:  big.NewInt(1_000_000),
			BalanceZNHB: big.NewInt(1_000_000),
			Stake:       big.NewInt(0),
		})
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	nhbAmount = big.NewInt(7_000)
	nhbTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr[:]...),
		Value:    new(big.Int).Set(nhbAmount),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := nhbTx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign nhb transfer: %v", err)
	}
	block, err := node.CreateBlock([]*types.Transaction{nhbTx})
	if err != nil {
		t.Fatalf("create block with nhb transfer: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block with nhb transfer: %v", err)
	}

	znhbAmount = big.NewInt(3_500)
	znhbTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    1,
		To:       append([]byte(nil), recipientAddr[:]...),
		Value:    new(big.Int).Set(znhbAmount),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := znhbTx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign znhb transfer: %v", err)
	}
	block, err = node.CreateBlock([]*types.Transaction{znhbTx})
	if err != nil {
		t.Fatalf("create block with znhb transfer: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block with znhb transfer: %v", err)
	}

	return node, nhbAmount, znhbAmount
}

// TestExplorerActivityIndexCountsPaymentsAndZNHBFlowIndependently is the core
// correctness test: after indexing a chain with one NHB payment and one ZNHB
// transfer, TotalPayments must be exactly 1 (only the NHB transfer is
// payment-like) and TotalZNHBFlow's wei amount must be exactly the ZNHB
// transfer's amount (the NHB payment must NOT contribute to it, even though
// both come from isPaymentLikeType/assetLabel checks over the same two
// transactions) -- and the index must report itself complete once it has
// processed every block the chain has.
func TestExplorerActivityIndexCountsPaymentsAndZNHBFlowIndependently(t *testing.T) {
	node, _, znhbAmount := buildActivityIndexTestChain(t)
	server := newTestServer(t, node, nil, ServerConfig{})

	server.advanceExplorerActivityIndex()

	payments, flow, complete := server.currentExplorerActivityTotals()
	if payments != 1 {
		t.Fatalf("expected exactly 1 payment-like transaction counted, got %d", payments)
	}
	if flow.Cmp(znhbAmount) != 0 {
		t.Fatalf("expected ZNHB flow %s, got %s", znhbAmount.String(), flow.String())
	}
	if !complete {
		t.Fatalf("expected activity index to report complete after processing every block")
	}
}

// TestExplorerActivityIndexBatchesLargeCatchUps confirms a catch-up spanning
// more blocks than explorerActivityBatchSize does NOT process everything in
// one call (the whole point of batching -- see the doc comment on
// explorerActivityBatchSize for the production incident this design avoids)
// and instead converges to the correct totals only after enough calls, each
// one picking up exactly where the last left off (no block skipped, no
// block double-counted).
func TestExplorerActivityIndexBatchesLargeCatchUps(t *testing.T) {
	originalBatchSize := explorerActivityBatchSize
	explorerActivityBatchSize = 1
	t.Cleanup(func() { explorerActivityBatchSize = originalBatchSize })

	node, nhbAmount, znhbAmount := buildActivityIndexTestChain(t)
	_ = nhbAmount
	server := newTestServer(t, node, nil, ServerConfig{})

	server.advanceExplorerActivityIndex()
	_, _, completeAfterFirstCall := server.currentExplorerActivityTotals()
	if completeAfterFirstCall {
		t.Fatalf("expected index to still be catching up after only one batch of size 1 against a 2-block chain")
	}

	server.advanceExplorerActivityIndex()
	payments, flow, complete := server.currentExplorerActivityTotals()
	if !complete {
		t.Fatalf("expected index to be complete after enough batched calls to cover the whole chain")
	}
	if payments != 1 {
		t.Fatalf("expected exactly 1 payment-like transaction counted after full catch-up, got %d", payments)
	}
	if flow.Cmp(znhbAmount) != 0 {
		t.Fatalf("expected ZNHB flow %s after full catch-up, got %s", znhbAmount.String(), flow.String())
	}

	// A further call once already caught up must be a safe no-op, not a
	// re-scan that double-counts the same blocks again.
	server.advanceExplorerActivityIndex()
	paymentsAgain, flowAgain, _ := server.currentExplorerActivityTotals()
	if paymentsAgain != payments || flowAgain.Cmp(flow) != 0 {
		t.Fatalf("expected totals unchanged by a no-op catch-up call, got payments=%d flow=%s", paymentsAgain, flowAgain.String())
	}
}

// TestExplorerActivityIndexPersistsAndResumesAcrossRestarts simulates a node
// restart: build totals with one Server instance, then construct a second
// Server against the SAME underlying node/chain (mirroring how a real
// restart re-opens the same on-disk database) and confirm it recovers the
// persisted watermark and totals via loadExplorerActivityTotals (called from
// NewServer) rather than starting back over from height 0 -- exactly the
// property that makes the one-time historical catch-up genuinely one-time
// rather than repeating on every restart.
func TestExplorerActivityIndexPersistsAndResumesAcrossRestarts(t *testing.T) {
	node, _, znhbAmount := buildActivityIndexTestChain(t)

	firstServer := newTestServer(t, node, nil, ServerConfig{})
	firstServer.advanceExplorerActivityIndex()
	payments, flow, complete := firstServer.currentExplorerActivityTotals()
	if !complete || payments != 1 || flow.Cmp(znhbAmount) != 0 {
		t.Fatalf("unexpected totals before simulated restart: payments=%d flow=%s complete=%v", payments, flow.String(), complete)
	}

	// A brand new Server sharing the same node -- NewServer calls
	// loadExplorerActivityTotals() internally, so this alone should recover
	// the persisted state without any explicit advance call yet.
	secondServer := newTestServer(t, node, nil, ServerConfig{})
	resumedPayments, resumedFlow, resumedComplete := secondServer.currentExplorerActivityTotals()
	if !resumedComplete {
		t.Fatalf("expected the second server to load an already-complete index from persisted state")
	}
	if resumedPayments != payments || resumedFlow.Cmp(flow) != 0 {
		t.Fatalf("expected resumed totals to match pre-restart totals: got payments=%d flow=%s want payments=%d flow=%s",
			resumedPayments, resumedFlow.String(), payments, flow.String())
	}
}
