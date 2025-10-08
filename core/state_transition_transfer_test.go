package core

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestApplyTransferZNHB(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()

	senderAccount := &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(600),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, senderAccount); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	recipientAccount := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(recipientAddr, recipientAccount); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
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

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}

	if updatedSender.BalanceZNHB.Cmp(big.NewInt(350)) != 0 {
		t.Fatalf("expected sender ZNHB balance 350, got %s", updatedSender.BalanceZNHB)
	}
	if updatedSender.Nonce != 1 {
		t.Fatalf("expected sender nonce 1, got %d", updatedSender.Nonce)
	}
	if updatedRecipient.BalanceZNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected recipient ZNHB balance 250, got %s", updatedRecipient.BalanceZNHB)
	}
	if updatedRecipient.Nonce != 0 {
		t.Fatalf("expected recipient nonce to remain 0, got %d", updatedRecipient.Nonce)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected sender NHB balance unchanged, got %s", updatedSender.BalanceNHB)
	}
}
