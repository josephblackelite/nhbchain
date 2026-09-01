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
