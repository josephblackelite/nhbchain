package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	coreevents "nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

type pauseViewStub struct {
	modules map[string]bool
}

func (s pauseViewStub) IsPaused(module string) bool {
	if len(s.modules) == 0 {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(module))
	return s.modules[normalized]
}

func TestApplyTransferZNHB(t *testing.T) {
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

	senderAccount := &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(600),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, senderAccount); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	recipientAccount := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(recipientAddr, recipientAccount); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(250),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}

	if updatedSender.BalanceZNHB.Cmp(big.NewInt(350)) != 0 {
		t.Fatalf("expected sender ZNHB balance 350, got %s", updatedSender.BalanceZNHB)
	}
	if updatedSender.Nonce != 1 {
		t.Fatalf("expected sender nonce 1, got %d", updatedSender.Nonce)
	}
	if updatedRecipient.BalanceZNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected recipient ZNHB balance 250, got %s", updatedRecipient.BalanceZNHB)
	}
	if updatedRecipient.Nonce != 0 {
		t.Fatalf("expected recipient nonce to remain 0, got %d", updatedRecipient.Nonce)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected sender NHB balance unchanged, got %s", updatedSender.BalanceNHB)
	}
}

func TestApplyTransferZNHB_SelfTransfer(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	initialBalance := big.NewInt(1_000)
	account := &types.Account{
		BalanceNHB:  big.NewInt(500),
		BalanceZNHB: new(big.Int).Set(initialBalance),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), senderAddr...),
		Value:    big.NewInt(250),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	updated, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if updated.BalanceZNHB.Cmp(initialBalance) != 0 {
		t.Fatalf("expected ZNHB balance %s, got %s", initialBalance, updated.BalanceZNHB)
	}
	if updated.Nonce != 1 {
		t.Fatalf("expected nonce 1, got %d", updated.Nonce)
	}
	events := sp.Events()
	if len(events) == 0 {
		t.Fatalf("expected transfer event, got none")
	}
	last := events[len(events)-1]
	if last.Type != "transfer.native" {
		t.Fatalf("expected transfer.native event, got %s", last.Type)
	}
	if last.Attributes["asset"] != "ZNHB" {
		t.Fatalf("expected ZNHB asset, got %s", last.Attributes["asset"])
	}
	if got := last.Attributes["from"]; !strings.EqualFold(got, last.Attributes["to"]) {
		t.Fatalf("expected self-transfer, from %s to %s", got, last.Attributes["to"])
	}
}

func TestApplyTransferZNHB_InsufficientBalance(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(100),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       recipientKey.PubKey().Address().Bytes(),
		Value:    big.NewInt(101),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err == nil || !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("expected insufficient balance error, got %v", err)
	}
}

func TestApplyTransferZNHB_ZeroValueRejected(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(50),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       recipientKey.PubKey().Address().Bytes(),
		Value:    big.NewInt(0),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err == nil || !strings.Contains(err.Error(), "amount must be positive") {
		t.Fatalf("expected positive amount error, got %v", err)
	}
}

func TestApplyTransferZNHB_Paused(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetPauseView(pauseViewStub{modules: map[string]bool{moduleTransferZNHB: true}})

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

	if err := sp.setAccount(senderAddr, &types.Account{BalanceZNHB: big.NewInt(1_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	tx := &types.Transaction{
		ChainID: types.NHBChainID(),
		Type:    types.TxTypeTransferZNHB,
		To:      append([]byte(nil), recipientAddr...),
		Value:   big.NewInt(100),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	err = sp.ApplyTransaction(tx)
	if !errors.Is(err, ErrTransferZNHBPaused) {
		t.Fatalf("expected ErrTransferZNHBPaused, got %v", err)
	}

	gotEvents := sp.Events()
	if len(gotEvents) == 0 {
		t.Fatalf("expected transfer pause event, got none")
	}
	evt := gotEvents[len(gotEvents)-1]
	if evt.Type != coreevents.TypeTransferZNHBBlocked {
		t.Fatalf("expected %s event, got %s", coreevents.TypeTransferZNHBBlocked, evt.Type)
	}
	if asset := evt.Attributes["asset"]; asset != "ZNHB" {
		t.Fatalf("expected asset ZNHB, got %s", asset)
	}
	if reason := evt.Attributes["reason"]; !strings.Contains(reason, "paused") {
		t.Fatalf("expected pause reason, got %s", reason)
	}
}

func TestApplyTransferNHB_Paused(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetPauseView(pauseViewStub{modules: map[string]bool{moduleTransferNHB: true}})

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

	senderAccount := &types.Account{BalanceNHB: big.NewInt(50_000_000_000_000)}
	if err := sp.setAccount(senderAddr, senderAccount); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit state: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(1_000),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1_000_000_000),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	err = sp.ApplyTransaction(tx)
	if !errors.Is(err, ErrTransferNHBPaused) {
		t.Fatalf("expected ErrTransferNHBPaused, got %v", err)
	}

	events := sp.Events()
	if len(events) == 0 {
		t.Fatalf("expected transfer pause event, got none")
	}
	evt := events[len(events)-1]
	if evt.Type != coreevents.TypeTransferNHBBlocked {
		t.Fatalf("expected %s event, got %s", coreevents.TypeTransferNHBBlocked, evt.Type)
	}
	if asset := evt.Attributes["asset"]; asset != "NHB" {
		t.Fatalf("expected asset NHB, got %s", asset)
	}
	if reason := evt.Attributes["reason"]; !strings.Contains(reason, "paused") {
		t.Fatalf("expected pause reason, got %s", reason)
	}
}

func TestTransferNHBNotAffectedByZNHBPause(t *testing.T) {
	t.Run("SucceedsWhenZNHBPaused", func(t *testing.T) {
		sp := newStakingStateProcessor(t)
		sp.SetPauseView(pauseViewStub{modules: map[string]bool{moduleTransferZNHB: true}})

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

		senderAccount := &types.Account{BalanceNHB: big.NewInt(50_000_000_000_000)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}
		if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
			t.Fatalf("seed recipient: %v", err)
		}

		if _, err := sp.Commit(0); err != nil {
			t.Fatalf("commit state: %v", err)
		}

		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    0,
			To:       append([]byte(nil), recipientAddr...),
			Value:    big.NewInt(1_000),
			GasLimit: 21_000,
			GasPrice: big.NewInt(1_000_000_000),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign transaction: %v", err)
		}

		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("apply NHB transfer: %v", err)
		}

		recipientAccount, err := sp.getAccount(recipientAddr)
		if err != nil {
			t.Fatalf("load recipient: %v", err)
		}
		if recipientAccount.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
			t.Fatalf("expected recipient to receive 1000 NHB, got %s", recipientAccount.BalanceNHB)
		}
		updatedSender, err := sp.getAccount(senderAddr)
		if err != nil {
			t.Fatalf("load sender: %v", err)
		}
		if updatedSender.Nonce != 1 {
			t.Fatalf("expected sender nonce incremented, got %d", updatedSender.Nonce)
		}
	})

	t.Run("ZeroValueRejected", func(t *testing.T) {
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

		senderAccount := &types.Account{BalanceNHB: big.NewInt(1_000_000)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}
		if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
			t.Fatalf("seed recipient: %v", err)
		}

		if _, err := sp.Commit(0); err != nil {
			t.Fatalf("commit state: %v", err)
		}

		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    0,
			To:       append([]byte(nil), recipientAddr...),
			Value:    big.NewInt(0),
			GasLimit: 21_000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign transaction: %v", err)
		}

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected zero-value transfer to fail")
		}
		if !errors.Is(err, ErrInvalidTransaction) {
			t.Fatalf("expected ErrInvalidTransaction, got %v", err)
		}
		if !strings.Contains(err.Error(), "amount must be positive") {
			t.Fatalf("expected amount validation error, got %v", err)
		}
	})

	t.Run("ZeroAddressRejected", func(t *testing.T) {
		sp := newStakingStateProcessor(t)

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}

		senderAddr := senderKey.PubKey().Address().Bytes()

		senderAccount := &types.Account{BalanceNHB: big.NewInt(1_000_000)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}

		zeroAddr := make([]byte, common.AddressLength)
		if _, err := sp.Commit(0); err != nil {
			t.Fatalf("commit state: %v", err)
		}

		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    0,
			To:       zeroAddr,
			Value:    big.NewInt(1_000),
			GasLimit: 21_000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign transaction: %v", err)
		}

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected zero-address transfer to fail")
		}
		if !errors.Is(err, ErrInvalidTransaction) {
			t.Fatalf("expected ErrInvalidTransaction, got %v", err)
		}
		if !strings.Contains(err.Error(), "recipient address invalid") {
			t.Fatalf("expected recipient address validation error, got %v", err)
		}
	})

	t.Run("SelfTransferAllowed", func(t *testing.T) {
		sp := newStakingStateProcessor(t)

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()

		initialBalance := big.NewInt(1_000_000)
		senderAccount := &types.Account{BalanceNHB: new(big.Int).Set(initialBalance)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}

		if _, err := sp.Commit(0); err != nil {
			t.Fatalf("commit state: %v", err)
		}

		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    0,
			To:       append([]byte(nil), senderAddr...),
			Value:    big.NewInt(500),
			GasLimit: 21_000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign transaction: %v", err)
		}

		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("self-transfer should succeed, got %v", err)
		}

		updatedSender, err := sp.getAccount(senderAddr)
		if err != nil {
			t.Fatalf("load sender: %v", err)
		}
		if updatedSender.Nonce != 1 {
			t.Fatalf("expected sender nonce incremented, got %d", updatedSender.Nonce)
		}
		expectedBalance := new(big.Int).Sub(initialBalance, big.NewInt(21_000))
		if updatedSender.BalanceNHB.Cmp(expectedBalance) != 0 {
			t.Fatalf("expected sender balance %s after gas, got %s", expectedBalance, updatedSender.BalanceNHB)
		}
	})
}

func TestApplyTransferZNHB_InvalidRecipient(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(25),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(senderAddr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       make([]byte, common.AddressLength),
		Value:    big.NewInt(10),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err == nil || !strings.Contains(err.Error(), "recipient address invalid") {
		t.Fatalf("expected recipient address invalid error, got %v", err)
	}
}
