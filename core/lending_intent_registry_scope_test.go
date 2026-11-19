package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/core/types"
)

// PL-R5T-48: the intent registry is one global namespace ("pos/intent/<ref>"), and
// ApplyTransaction validates and consumes an IntentRef for ANY transaction type that
// carries one. On the public main branch two senderless lending fee-withdrawal types
// used their public signing hash as the IntentRef and relied on the registry as their
// only replay guard, so any signed transaction carrying that reference could kill the
// administrator's authorisation. The finding is therefore not applicable to this line
// as long as no lending transaction depends on the registry for its validity or its
// replay protection. These tests fail the day one does.

func TestNoLendingCodeDependsOnTheIntentRegistry(t *testing.T) {
	var files []string
	for _, pattern := range []string{"lending*.go", filepath.Join("..", "native", "lending", "*.go")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) < 10 {
		t.Fatalf("found only %d lending source files, the scan is looking in the wrong place", len(files))
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"IntentRef", "IntentExpiry", "IntentRegistry", "pos/intent"} {
			if strings.Contains(string(data), word) {
				t.Errorf("%s mentions %q: a lending transaction that depends on the intent registry brings back the global-namespace griefing of PL-R5T-48 (and needs its own namespace)", file, word)
			}
		}
	}
}

// Every lending transaction type is an ordinary signed transaction: replay protection is
// the sender's nonce, never a reference the registry could be made to consume.
func TestEveryLendingTransactionTypeIsOrdinarilySigned(t *testing.T) {
	lendingTypes := []types.TxType{
		types.TxTypeLendingSupplyNHB, types.TxTypeLendingWithdrawNHB, types.TxTypeLendingDepositZNHB,
		types.TxTypeLendingWithdrawZNHB, types.TxTypeLendingBorrowNHB, types.TxTypeLendingRepayNHB,
		types.TxTypeLendingLiquidate, types.TxTypeLendingCreatePool, types.TxTypeLendingBorrowFixedTerm,
		types.TxTypeLendingRepayFixedTerm, types.TxTypeLendingSupplyFixedTerm, types.TxTypeLendingWithdrawProtocolFees,
	}
	for _, typ := range lendingTypes {
		if !types.RequiresSignature(typ) {
			t.Errorf("%s is senderless: its authorisation would have to be embedded, and its replay guard would be the global intent registry", types.TxTypeName(typ))
		}
	}
	// The one lending type that is senderless carries its own signer quorum and a
	// monotonic timestamp (PL-EXT-13), not a registry reference.
	if types.RequiresSignature(types.TxTypeLendingRefPrice) {
		t.Errorf("the lending reference price is expected to be the one senderless lending type")
	}
}
