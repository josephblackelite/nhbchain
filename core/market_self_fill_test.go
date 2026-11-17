package core

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// One address lists a single wei of ZNHB at a price close to its whole NHB
// balance and fills its own listing, through real signed transactions and the
// real state adapter (which hands out a fresh account object per read). The
// seller-side credit used to overwrite the buyer-side debit, so the address
// ended with balance + price, the fee still reached the fee collector and the
// escrowed ZNHB vanished. The fill is now refused as permanently dead and no
// balance moves beyond the first transaction's escrow.
func TestApplyMarketFillListing_SelfFillIsRefusedAndConservesValue(t *testing.T) {
	sp := newStakingStateProcessor(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	oneNHB := big.NewInt(1_000_000_000_000_000_000)
	if err := sp.setAccount(addr, &types.Account{BalanceNHB: new(big.Int).Set(oneNHB), BalanceZNHB: big.NewInt(10), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Cost 0.5 NHB for 1 wei ZNHB; the default 0.1 NHB flat fee is on top.
	createTx := marketCreateListingTx(t, 0, big.NewInt(1), big.NewInt(1), big.NewInt(500_000_000_000_000_000), false)
	if err := createTx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}
	listingID := soleOpenListingID(t, sp)

	fillTx := marketFillListingTx(t, 1, listingID, big.NewInt(1))
	if err := fillTx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign fill: %v", err)
	}
	fillErr := sp.ApplyTransaction(fillTx)

	account, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	escrow, err := sp.getAccount(sp.marketEscrowAddr.Bytes())
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	collector, err := sp.getAccount(sp.marketFeeCollectorAddr.Bytes())
	if err != nil {
		t.Fatalf("load fee collector: %v", err)
	}
	totalNHB := new(big.Int).Add(account.BalanceNHB, collector.BalanceNHB)
	if totalNHB.Cmp(oneNHB) != 0 {
		t.Fatalf("NHB across the account and the fee collector is %s, want the original %s", totalNHB, oneNHB)
	}
	totalZNHB := new(big.Int).Add(account.BalanceZNHB, escrow.BalanceZNHB)
	if totalZNHB.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("ZNHB across the account and the escrow is %s, want the original 10", totalZNHB)
	}

	if !errors.Is(fillErr, market.ErrSelfFill) {
		t.Fatalf("expected the self-fill to be refused with ErrSelfFill, got %v", fillErr)
	}
	if got := classifyProposalError(fillErr); got != proposalDispositionPrune {
		t.Fatalf("a self-fill can never become valid: want prune, got disposition %d", got)
	}
}
