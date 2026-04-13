package types

import (
	"bytes"
	"math/big"
	"strings"
	"testing"
)

func TestTransactionHashBindsIntentRef(t *testing.T) {
	base := &Transaction{
		ChainID:      NHBChainID(),
		Type:         TxTypePOSAuthorize,
		Nonce:        7,
		To:           bytes.Repeat([]byte{0x11}, 20),
		Value:        big.NewInt(1250),
		GasLimit:     21_000,
		GasPrice:     big.NewInt(1),
		IntentExpiry: 123456,
	}

	txA := *base
	txA.IntentRef = []byte("intent-a")
	hashA, err := txA.Hash()
	if err != nil {
		t.Fatalf("hash txA: %v", err)
	}

	txB := *base
	txB.IntentRef = []byte("intent-b")
	hashB, err := txB.Hash()
	if err != nil {
		t.Fatalf("hash txB: %v", err)
	}

	if bytes.Equal(hashA, hashB) {
		t.Fatalf("expected different hashes when intentRef changes")
	}
}

func TestTransactionHashRejectsOversizedAddress(t *testing.T) {
	tx := &Transaction{
		ChainID:  NHBChainID(),
		Type:     TxTypeTransfer,
		To:       bytes.Repeat([]byte{0x22}, 21),
		Value:    big.NewInt(1),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}

	if _, err := tx.Hash(); err == nil || !strings.Contains(err.Error(), "to length") {
		t.Fatalf("expected address length validation error, got %v", err)
	}
}
