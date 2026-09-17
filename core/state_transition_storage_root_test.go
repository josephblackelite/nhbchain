package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestApplyTransactionShortStorageRoot(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	account := &types.Account{
		BalanceNHB:  big.NewInt(100_000),
		BalanceZNHB: big.NewInt(1_000),
		Stake:       big.NewInt(0),
		StorageRoot: []byte{0x01, 0x02, 0x03},
		CodeHash:    []byte{0x04},
	}
	if err := sp.setAccount(senderAddr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if got := len(account.StorageRoot); got != common.HashLength {
		t.Fatalf("expected storage root to be canonical length, got %d", got)
	}
	if got := len(account.CodeHash); got != common.HashLength {
		t.Fatalf("expected code hash to be canonical length, got %d", got)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), senderAddr...),
		Value:    big.NewInt(250),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	updated, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if got := len(updated.StorageRoot); got != common.HashLength {
		t.Fatalf("expected stored root length %d, got %d", common.HashLength, got)
	}
	if got := len(updated.CodeHash); got != common.HashLength {
		t.Fatalf("expected stored code hash length %d, got %d", common.HashLength, got)
	}
}
