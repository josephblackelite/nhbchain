package core

import (
	"errors"
	"testing"
	"time"

	"nhbchain/core/types"
)

// NHB-AUDIT-R2: MaxBlockHeight/IntentExpiry used to be checked only once,
// at mempool admission (core/node.go's addTransaction) -- never at actual
// execution. A transaction sitting in the mempool past its declared
// expiry, or one that skipped/never went through the admission check
// (e.g. gossiped in from a peer that didn't enforce it), still executed
// normally once a proposer included it. These tests exercise
// executeTransaction directly, proving the deterministic execution-time
// gate now catches both cases.

func TestExecuteTransactionRejectsExpiredMaxBlockHeight(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	sp.BeginBlock(100, time.Unix(1_700_000_000, 0))

	tx := &types.Transaction{
		ChainID:        types.NHBChainID(),
		Type:           types.TxTypeTransfer,
		Nonce:          0,
		MaxBlockHeight: 50, // already in the past relative to height 100
	}

	_, err := sp.executeTransaction(tx)
	if err == nil {
		t.Fatalf("expected an error for an expired MaxBlockHeight")
	}
	if !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("expected ErrTransactionExpired, got %v", err)
	}
	if classifyProposalError(err) != proposalDispositionPrune {
		t.Fatalf("expected an expired transaction to be prune-safe, got disposition %v", classifyProposalError(err))
	}
}

func TestExecuteTransactionRejectsExpiredIntentExpiryWithoutIntentRef(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	blockTime := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(1, blockTime)

	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeTransfer,
		Nonce:        0,
		IntentExpiry: uint64(blockTime.Unix()) - 1, // already expired
	}

	_, err := sp.executeTransaction(tx)
	if err == nil {
		t.Fatalf("expected an error for an expired IntentExpiry")
	}
	if !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("expected ErrTransactionExpired, got %v", err)
	}
}

func TestExecuteTransactionAllowsUnexpiredMaxBlockHeightAndIntentExpiry(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	blockTime := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(100, blockTime)

	tx := &types.Transaction{
		ChainID:        types.NHBChainID(),
		Type:           types.TxTypeTransfer,
		Nonce:          0,
		MaxBlockHeight: 200,                        // still in the future
		IntentExpiry:   uint64(blockTime.Unix()) + 1, // still in the future
	}

	_, err := sp.executeTransaction(tx)
	// The transaction will still fail later validation (no real sender
	// signature was ever set up), but it must NOT fail on the expiry gate
	// this test is targeting.
	if errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("did not expect ErrTransactionExpired for an unexpired transaction, got %v", err)
	}
}

// NHB-AUDIT-R2: an intent-bearing transaction's expiry is already validated
// deterministically via IntentRegistryValidate -- the general IntentExpiry
// check added above must defer to it rather than double-checking (and
// potentially conflicting with its TTL-extension semantics) when IntentRef
// is set.
func TestExecuteTransactionSkipsGeneralIntentExpiryCheckWhenIntentRefIsSet(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	blockTime := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(1, blockTime)

	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeTransfer,
		Nonce:        0,
		IntentRef:    []byte("some-intent-ref"),
		IntentExpiry: uint64(blockTime.Unix()) - 1, // would be "expired" under the general check
	}

	_, err := sp.executeTransaction(tx)
	// Whatever IntentRegistryValidate itself decides is out of scope for
	// this test -- the point is that the NEW general check must not be
	// what rejects it.
	if errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("expected the intent-specific validation path to handle this, not the general ErrTransactionExpired check, got %v", err)
	}
}
