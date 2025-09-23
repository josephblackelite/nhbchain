package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func TestCommitBlockRollsBackOnApplyError(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	parentRoot := node.state.CurrentRoot()
	parentPending := node.state.PendingRoot()

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("ab"),
	}
	if err := tx.Sign(validatorKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: time.Now().Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	block := types.NewBlock(header, []*types.Transaction{tx})

	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected commit error for invalid transaction")
	}

	if got := node.state.CurrentRoot(); got != parentRoot {
		t.Fatalf("current root changed on failed commit: got %x want %x", got.Bytes(), parentRoot.Bytes())
	}
	if got := node.state.PendingRoot(); got != parentPending {
		t.Fatalf("pending root changed on failed commit: got %x want %x", got.Bytes(), parentPending.Bytes())
	}
	if height := node.chain.GetHeight(); height != 0 {
		t.Fatalf("unexpected chain height after failed commit: got %d want 0", height)
	}
}
