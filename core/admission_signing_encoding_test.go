package core

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
)

// A transaction whose Value is longer than 32 bytes is refused where it enters
// the node (the pool and peer gossip both go through addTransaction), so the
// 16-bit length wrap in the V3 signing text (PL-EXT-15) cannot be used to get a
// reused signature into a block this node proposes. The same transaction is not
// judged any differently once it is inside a block: ValidateBasic and Hash are
// unchanged.
func TestAdmissionRefusesAV3ValueLongerThan32Bytes(t *testing.T) {
	node := newTestNode(t)
	key := livenessKey(t)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       bytes.Repeat([]byte{0x44}, 20),
		Value:    new(big.Int).SetBytes(bytes.Repeat([]byte{0x01}, 65537)),
		GasLimit: 100_000,
		GasPrice: big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}

	err := node.validateTransaction(tx)
	if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "value must not exceed") {
		t.Fatalf("expected an ErrInvalidTransaction about the value length, got %v", err)
	}
	if err := node.AddTransaction(tx); err == nil || !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("AddTransaction must refuse it as well, got %v", err)
	}

	// Block execution does not apply the admission check: Hash() still accepts the
	// same transaction, so what a block already contains is judged as before.
	if _, err := tx.Hash(); err != nil {
		t.Fatalf("Hash() must be unchanged by the admission guard: %v", err)
	}
}
