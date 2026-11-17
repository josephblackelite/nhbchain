package core

import (
	"errors"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// A borrower liquidating their own position is refused with the lending
// engine's sentinel, not a bare error: the block builder has to recognize it
// as permanently unexecutable (prune) or the transaction would abort every
// proposal that includes it. The refusal leaves the borrower's balances alone.
func TestApplyLendingLiquidate_SelfLiquidationIsPrunedAndMovesNothing(t *testing.T) {
	sp, db, _, borrowerKey, _ := seedUnhealthyBorrowerPosition(t)
	defer db.Close()

	// Top the borrower up in place: replacing the account would reset its
	// nonce and the transaction would fail on the nonce check instead.
	borrowerBytes := borrowerKey.PubKey().Address().Bytes()
	before, err := sp.getAccount(borrowerBytes)
	if err != nil {
		t.Fatalf("load borrower: %v", err)
	}
	before.BalanceNHB = mustBigInt(t, "1000000000000000000000")
	before.BalanceZNHB = mustBigInt(t, "5")
	if err := sp.setAccount(borrowerBytes, before); err != nil {
		t.Fatalf("top up borrower: %v", err)
	}

	borrowerAddr := crypto.MustNewAddress(crypto.NHBPrefix, borrowerBytes)
	tx := mustSignLendingLiquidateTx(t, borrowerKey, before.Nonce, borrowerAddr)
	sp.BeginBlock(4, time.Unix(4, 0).UTC())
	applyErr := sp.ApplyTransaction(tx)
	sp.EndBlock()

	after, err := sp.getAccount(borrowerBytes)
	if err != nil {
		t.Fatalf("reload borrower: %v", err)
	}
	if after.BalanceNHB.Cmp(before.BalanceNHB) != 0 || after.BalanceZNHB.Cmp(before.BalanceZNHB) != 0 {
		t.Fatalf("a refused self-liquidation moved balances: NHB %s -> %s, ZNHB %s -> %s", before.BalanceNHB, after.BalanceNHB, before.BalanceZNHB, after.BalanceZNHB)
	}

	if !errors.Is(applyErr, lending.ErrSelfLiquidation) {
		t.Fatalf("expected ErrSelfLiquidation, got %v", applyErr)
	}
	if got := classifyProposalError(applyErr); got != proposalDispositionPrune {
		t.Fatalf("a self-liquidation can never become valid: want prune, got disposition %d", got)
	}
}
