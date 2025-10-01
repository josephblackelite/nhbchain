package core

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestTransactionKeyDiffersByPaymaster(t *testing.T) {
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}

	paymasterA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster A: %v", err)
	}

	paymasterB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster B: %v", err)
	}

	newTx := func(paymaster []byte) *types.Transaction {
		to := make([]byte, 20)
		copy(to, []byte{0x01})
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    1,
			To:       to,
			GasLimit: 21_000,
			GasPrice: big.NewInt(1_000_000_000),
			Value:    big.NewInt(10),
		}
		if len(paymaster) > 0 {
			tx.Paymaster = append([]byte(nil), paymaster...)
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		return tx
	}

	txWithA := newTx(paymasterA.PubKey().Address().Bytes())
	keyA, err := transactionKey(txWithA)
	if err != nil {
		t.Fatalf("transactionKey A: %v", err)
	}

	txWithB := newTx(paymasterB.PubKey().Address().Bytes())
	keyB, err := transactionKey(txWithB)
	if err != nil {
		t.Fatalf("transactionKey B: %v", err)
	}

	if keyA == keyB {
		t.Fatalf("expected different keys for different paymasters")
	}

	txWithAResigned := newTx(paymasterA.PubKey().Address().Bytes())
	keyAResigned, err := transactionKey(txWithAResigned)
	if err != nil {
		t.Fatalf("transactionKey resigned: %v", err)
	}

	if keyA != keyAResigned {
		t.Fatalf("expected identical keys for identical paymaster submissions")
	}
}
