package core

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestCreateBlockRespectsConfiguredTransactionCap(t *testing.T) {
	node := newTestNode(t)

	cfg := node.globalConfigSnapshot()
	cfg.Blocks.MaxTxs = 2
	node.SetGlobalConfig(cfg)

	txs := []*types.Transaction{
		buildIdentityRegistrationTx(t, node, "user-0"),
		buildIdentityRegistrationTx(t, node, "user-1"),
		buildIdentityRegistrationTx(t, node, "user-2"),
	}
	node.mempool = append([]*types.Transaction(nil), txs...)

	block, err := node.CreateBlock(node.mempool)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}

	limit := int(cfg.Blocks.MaxTxs)
	if len(block.Transactions) != limit {
		t.Fatalf("unexpected transaction count: got %d want %d", len(block.Transactions), limit)
	}

	for i := range block.Transactions {
		if block.Transactions[i] != txs[i] {
			t.Fatalf("block truncated unexpected transaction at %d", i)
		}
	}

	if len(node.mempool) != len(txs) {
		t.Fatalf("mempool should remain unchanged: got %d want %d", len(node.mempool), len(txs))
	}

	expectedRoot, err := ComputeTxRoot(txs[:limit])
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}
	if !bytes.Equal(expectedRoot, block.Header.TxRoot) {
		t.Fatalf("tx root mismatch after truncation")
	}
}

func TestCreateBlockUsesAllTransactionsWhenCapUnchanged(t *testing.T) {
	node := newTestNode(t)

	snapshot := node.globalConfigSnapshot()
	// Bump the cap so all transactions in this test fit into a single block.
	snapshot.Blocks.MaxTxs = 5
	node.SetGlobalConfig(snapshot)

	txs := []*types.Transaction{
		buildIdentityRegistrationTx(t, node, "under-cap-0"),
		buildIdentityRegistrationTx(t, node, "under-cap-1"),
		buildIdentityRegistrationTx(t, node, "under-cap-2"),
	}

	block, err := node.CreateBlock(txs)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if len(block.Transactions) != len(txs) {
		t.Fatalf("unexpected transaction count: got %d want %d", len(block.Transactions), len(txs))
	}
}

func buildIdentityRegistrationTx(t *testing.T, node *Node, username string) *types.Transaction {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := node.state.setAccount(addr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte(username),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}
