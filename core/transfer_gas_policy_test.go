package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestTransferGasPolicyFreeTierAndThreshold(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()
	var collector [20]byte
	collector[19] = 0x55

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(1000),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(50_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	first := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(400),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := first.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign first transfer: %v", err)
	}
	if err := sp.ApplyTransaction(first); err != nil {
		t.Fatalf("apply first transfer: %v", err)
	}

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender after first transfer: %v", err)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(49_600)) != 0 {
		t.Fatalf("expected sender balance 49600 after free-tier transfer, got %s", updatedSender.BalanceNHB)
	}
	collectorAcc, err := sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector after first transfer: %v", err)
	}
	if collectorAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected collector to remain 0 during free tier, got %s", collectorAcc.BalanceNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var senderWallet [20]byte
	copy(senderWallet[:], senderAddr)
	status, err := manager.TransferGasSpendStatus(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(1000))
	if err != nil {
		t.Fatalf("load spend status after first transfer: %v", err)
	}
	if status.Spent.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected recorded spend 400, got %s", status.Spent)
	}
	if !status.Eligible {
		t.Fatalf("expected sender to remain eligible below threshold")
	}

	if _, err := manager.TransferGasSpendAdd(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(600), big.NewInt(1000)); err != nil {
		t.Fatalf("prime sender spend to threshold: %v", err)
	}

	second := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    1,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(200),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := second.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign second transfer: %v", err)
	}
	if err := sp.ApplyTransaction(second); err != nil {
		t.Fatalf("apply second transfer: %v", err)
	}

	updatedSender, err = sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender after second transfer: %v", err)
	}
	expectedSender := big.NewInt(49_600 - 200 - 21_000)
	if updatedSender.BalanceNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("expected sender balance %s after paid transfer, got %s", expectedSender, updatedSender.BalanceNHB)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient after second transfer: %v", err)
	}
	if updatedRecipient.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected recipient balance 600, got %s", updatedRecipient.BalanceNHB)
	}
	collectorAcc, err = sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector after second transfer: %v", err)
	}
	if collectorAcc.BalanceNHB.Cmp(big.NewInt(21_000)) != 0 {
		t.Fatalf("expected collector balance 21000, got %s", collectorAcc.BalanceNHB)
	}
}

func TestTransferGasPolicyThresholdCrossingTransferRemainsFree(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()
	var collector [20]byte
	collector[19] = 0x77

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(1000),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(5_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var senderWallet [20]byte
	copy(senderWallet[:], senderAddr)
	if _, err := manager.TransferGasSpendAdd(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(900), big.NewInt(1000)); err != nil {
		t.Fatalf("prime spend status: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(200),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(4_800)) != 0 {
		t.Fatalf("expected threshold-crossing transfer to stay free, got %s", updatedSender.BalanceNHB)
	}
	status, err := manager.TransferGasSpendStatus(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(1000))
	if err != nil {
		t.Fatalf("load final spend status: %v", err)
	}
	if status.Spent.Cmp(big.NewInt(1_100)) != 0 {
		t.Fatalf("expected recorded spend 1100, got %s", status.Spent)
	}
	if status.Eligible {
		t.Fatalf("expected sender to become ineligible after crossing threshold")
	}
}
