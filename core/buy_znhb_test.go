package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func newBuyZNHBStateProcessor(t *testing.T) (*StateProcessor, [20]byte) {
	t.Helper()
	sp := newStakingStateProcessor(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	sp.SetAdminWallet(adminAddr, true)

	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(1_000_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	return sp, adminAddr
}

func buyZNHBTx(t *testing.T, nonce uint64, nhbAmount, znhbAmount *big.Int) *types.Transaction {
	t.Helper()
	payload := struct {
		NHBAmount  *big.Int `json:"nhbAmount"`
		ZNHBAmount *big.Int `json:"znhbAmount"`
		QuoteID    string   `json:"quoteId,omitempty"`
	}{NHBAmount: nhbAmount, ZNHBAmount: znhbAmount}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeBuyZNHB,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
}

func TestApplyBuyZNHB_MovesBothLegsAtomically(t *testing.T) {
	sp, adminAddr := newBuyZNHBStateProcessor(t)

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, big.NewInt(400), big.NewInt(400))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	if buyer.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected buyer NHB 600, got %s", buyer.BalanceNHB)
	}
	if buyer.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected buyer ZNHB 400, got %s", buyer.BalanceZNHB)
	}
	if buyer.Nonce != 1 {
		t.Fatalf("expected buyer nonce 1, got %d", buyer.Nonce)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if admin.BalanceNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected admin NHB 400 (revenue), got %s", admin.BalanceNHB)
	}
	if admin.BalanceZNHB.Cmp(big.NewInt(999_600)) != 0 {
		t.Fatalf("expected admin ZNHB 999600, got %s", admin.BalanceZNHB)
	}
}

func TestApplyBuyZNHB_InsufficientNHBRejected(t *testing.T) {
	sp, _ := newBuyZNHBStateProcessor(t)

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  big.NewInt(100),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, big.NewInt(400), big.NewInt(400))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection for insufficient NHB balance")
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	if buyer.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("buyer NHB balance must be unchanged on rejection, got %s", buyer.BalanceNHB)
	}
	if buyer.BalanceZNHB.Sign() != 0 {
		t.Fatalf("buyer must not receive ZNHB on rejection, got %s", buyer.BalanceZNHB)
	}
}

func TestApplyBuyZNHB_InsufficientAdminZNHBRejected(t *testing.T) {
	sp, adminAddr := newBuyZNHBStateProcessor(t)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(10),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("underfund admin wallet: %v", err)
	}

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, big.NewInt(400), big.NewInt(400))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection when admin wallet lacks sufficient ZNHB")
	}
}

func TestApplySwapMintAndBurn_Disabled(t *testing.T) {
	sp, _ := newBuyZNHBStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()
	if err := sp.setAccount(senderAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	burnPayload := struct {
		Amount           *big.Int `json:"amount"`
		TargetStablecoin string   `json:"targetStablecoin"`
		RecipientAddress string   `json:"recipientAddress"`
	}{Amount: big.NewInt(500)}
	data, err := rlp.EncodeToBytes(burnPayload)
	if err != nil {
		t.Fatalf("encode burn payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapBurn,
		Nonce:    0,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected disabled TxTypeSwapBurn to be rejected")
	}

	sender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if sender.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("disabled swap burn must not destroy funds, got NHB balance %s", sender.BalanceNHB)
	}
}
