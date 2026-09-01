package core

import (
	"math/big"
	"testing"

	"google.golang.org/protobuf/proto"

	"nhbchain/core/types"
	"nhbchain/crypto"
	posv1 "nhbchain/proto/pos"
)

// TestPOSAuthorizeCannotBeReplayed is an independent regression test for an
// externally-reported finding (bug bounty submission NHB-TRIAGE-H2, not
// formally submitted for a bounty but present in the same triage test file
// as NHB-TRIAGE-C4/C7): applyPOSAuthorize never advanced the signer's
// account nonce, so validateSenderAccount's exact-match nonce check (which
// gates every transaction type generically, before dispatch) let the exact
// same signed transaction bytes be submitted more than once. Authorize
// derives its authorization ID from its own internal per-payer counter, not
// the transaction nonce, so each replay created a brand new authorization
// and locked the payer's amount again -- a single signature could drain an
// arbitrary amount of the payer's ZNHB into pending holds, one resubmission
// at a time.
func TestPOSAuthorizeCannotBeReplayed(t *testing.T) {
	node := newTestNode(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payer := toAddress(payerKey)
	fundAccount(t, node, payer, big.NewInt(1000))

	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	merchant := toAddress(merchantKey)

	payerAddrStr := crypto.MustNewAddress(crypto.ZNHBPrefix, payer[:]).String()
	merchantAddrStr := crypto.MustNewAddress(crypto.ZNHBPrefix, merchant[:]).String()

	msg := &posv1.MsgAuthorizePayment{
		Payer:    payerAddrStr,
		Merchant: merchantAddrStr,
		Amount:   "400",
		Expiry:   4102444800, // 2100-01-01, far enough out not to expire mid-test
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal authorize msg: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypePOSAuthorize,
		Nonce:    0,
		Data:     payload,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	if err := node.state.ApplyTransaction(tx); err != nil {
		t.Fatalf("first authorize: %v", err)
	}

	account, err := node.state.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get account after first authorize: %v", err)
	}
	if account.Nonce != 1 {
		t.Fatalf("expected payer nonce to advance to 1 after a successful authorize, got %d", account.Nonce)
	}
	if account.LockedZNHB == nil || account.LockedZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected 400 locked after first authorize, got %v", account.LockedZNHB)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected 600 balance remaining after first authorize, got %v", account.BalanceZNHB)
	}

	// Resubmit the exact same signed transaction bytes -- exactly what a
	// malicious relayer or a peer that merely observed the first submission
	// on the wire could do without needing the payer's key at all.
	replayErr := node.state.ApplyTransaction(tx)
	if replayErr == nil {
		t.Fatalf("SECURITY: replaying the identical signed authorize transaction was accepted instead of rejected for a nonce mismatch")
	}

	account, err = node.state.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get account after replay attempt: %v", err)
	}
	if account.Nonce != 1 {
		t.Fatalf("expected payer nonce to remain 1 after the rejected replay, got %d", account.Nonce)
	}
	if account.LockedZNHB == nil || account.LockedZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("SECURITY: replay changed locked balance -- expected still 400, got %v", account.LockedZNHB)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("SECURITY: replay changed spendable balance -- expected still 600, got %v", account.BalanceZNHB)
	}
}
