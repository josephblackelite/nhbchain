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

// TestV3HashLengthWrapAndAdmissionGuard documents the signing-text ambiguity
// reported on 2026-11-02 (PL-EXT-15) and checks the admission guard that closes
// it. Hash() writes uint16(len(Value)), so a Value of 65,537 bytes gets the same
// two-byte prefix as a Value of 1 byte: the bytes of the first transaction's
// Value, Data length and Data can be re-cut into a different Value and Data
// without changing the signing text. Both transactions below hash identically;
// the guard refuses the one with the huge Value and accepts the honest one.
func TestV3HashLengthWrapAndAdmissionGuard(t *testing.T) {
	forged := []byte("a different payload the payer never saw")

	// The first transaction's Data: padding, then the 4-byte length of the forged
	// Data, then the forged Data.
	padded := bytes.Repeat([]byte{'p'}, 65532)
	padded = append(padded, byte(len(forged)>>24), byte(len(forged)>>16), byte(len(forged)>>8), byte(len(forged)))
	padded = append(padded, forged...)
	if len(padded) != 65536+len(forged) {
		t.Fatalf("test setup: padded data is %d bytes", len(padded))
	}
	lenPadded := []byte{byte(len(padded) >> 24), byte(len(padded) >> 16), byte(len(padded) >> 8), byte(len(padded))}

	// The second transaction's Value: the first Value byte, the first Data
	// length and the first 65,532 bytes of the first Data (65,537 bytes in all).
	wrapped := append([]byte{0x01}, lenPadded...)
	wrapped = append(wrapped, padded[:65532]...)
	if len(wrapped) != 65537 {
		t.Fatalf("test setup: wrapped value is %d bytes", len(wrapped))
	}

	base := Transaction{
		ChainID:  NHBChainID(),
		Type:     TxTypePOSAuthorize,
		Nonce:    3,
		To:       bytes.Repeat([]byte{0x33}, 20),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	honest := base
	honest.Value = big.NewInt(1)
	honest.Data = padded
	attack := base
	attack.Value = new(big.Int).SetBytes(wrapped)
	attack.Data = forged

	hHonest, err := honest.Hash()
	if err != nil {
		t.Fatalf("hash honest: %v", err)
	}
	hAttack, err := attack.Hash()
	if err != nil {
		t.Fatalf("hash attack: %v", err)
	}
	if !bytes.Equal(hHonest, hAttack) {
		t.Fatalf("test setup is wrong: the two transactions were meant to share a signing text")
	}

	if err := honest.ValidateSigningEncoding(); err != nil {
		t.Fatalf("the honest transaction must pass the guard: %v", err)
	}
	if err := attack.ValidateSigningEncoding(); err == nil || !strings.Contains(err.Error(), "value must not exceed") {
		t.Fatalf("the transaction with a 65,537-byte Value must be refused, got %v", err)
	}

	// The same bound applies to the gas price, and a legacy (type 0) transaction
	// is hashed as JSON, which has no such ambiguity.
	longPrice := base
	longPrice.Value = big.NewInt(1)
	longPrice.GasPrice = new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 33))
	if err := longPrice.ValidateSigningEncoding(); err == nil || !strings.Contains(err.Error(), "gasPrice must not exceed") {
		t.Fatalf("a 33-byte gas price must be refused, got %v", err)
	}
	edge := base
	edge.Value = new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32))
	edge.GasPrice = new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32))
	if err := edge.ValidateSigningEncoding(); err != nil {
		t.Fatalf("32-byte numbers must pass: %v", err)
	}
	legacy := attack
	legacy.Type = 0
	if err := legacy.ValidateSigningEncoding(); err != nil {
		t.Fatalf("a legacy transaction is not covered by the guard: %v", err)
	}
	var nilTx *Transaction
	if err := nilTx.ValidateSigningEncoding(); err == nil {
		t.Fatalf("a nil transaction must be refused")
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

// TestFromRejectsOversizedRInsteadOfPanicking is a regression test for an
// externally-reported vulnerability (bug bounty submission NHB-TRIAGE-C2,
// not formally submitted for a bounty but present in the same triage test
// file as NHB-TRIAGE-C4/C7): ValidateBasic checked R/S/V for being negative
// but never checked their byte length. From() and PaymasterSponsor() both
// do copy(sig[32-len(x.Bytes()):32], x.Bytes()) against a fixed 65-byte
// buffer with no length guard of their own -- an R value whose byte
// representation exceeds 32 bytes (trivially reachable: any value >= 2^256,
// e.g. 2^300) makes 32-len(...) negative, which is an out-of-range slice
// index and panics rather than returning an error.
//
// This is reachable from a single, ordinary P2P transaction gossip message
// with no other exploit chaining required, and nothing in the P2P
// message-receive path (p2p/peer.go's per-peer readLoop goroutine) recovers
// a panic -- an unrecovered panic in any goroutine crashes the entire Go
// process, so this was a trivial, unauthenticated remote denial-of-service
// against any node that accepts P2P connections, not merely a single
// rejected transaction.
func TestFromRejectsOversizedRInsteadOfPanicking(t *testing.T) {
	hugeR := new(big.Int).Exp(big.NewInt(2), big.NewInt(300), nil) // 38 bytes
	tx := &Transaction{
		ChainID:  NHBChainID(),
		Type:     TxTypeTransfer,
		Nonce:    0,
		To:       make([]byte, 20),
		Value:    big.NewInt(1),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
		R:        hugeR,
		S:        big.NewInt(1),
		V:        big.NewInt(27),
	}

	if err := tx.ValidateBasic(); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("expected ValidateBasic to reject the oversized R, got %v", err)
	}

	// From() must return this same validation error, not panic -- this is
	// the actual call site a real transaction-ingestion path invokes.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("SECURITY: tx.From() panicked instead of returning an error for an oversized R: %v", r)
			}
		}()
		if _, err := tx.From(); err == nil {
			t.Fatalf("expected From() to return an error for the oversized R")
		}
	}()
}
