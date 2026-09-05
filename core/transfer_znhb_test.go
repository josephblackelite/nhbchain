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
		// newStakingStateProcessor leaves TransferGasPolicy at its zero
		// value (FeeBps unset), so no fee is charged -- the protocol-
		// enforced fee (docs/issue30.md item 7b) is a percentage of the
		// transfer value that must be explicitly configured, not derived
		// from this transaction's GasLimit/GasPrice (21_000*1), which the
		// old mechanism used regardless of any policy at all.
		if updatedSender.BalanceNHB.Cmp(initialBalance) != 0 {
			t.Fatalf("expected sender balance unchanged at %s with no fee configured, got %s", initialBalance, updatedSender.BalanceNHB)
		}
	})
}

// TestApplyTransferZNHB_FeeUsesZNHBRateNotNHBRate proves applyTransferZNHB
// charges the transfer's own ZNHB-specific rate (FeeBpsZNHB), not NHB's
// FeeBps -- see docs/issue30.md item 7b's NHB/ZNHB fee split. FeeBps is
// deliberately set to an absurd 9999 (99.99%) so that, if applyTransferZNHB
// ever regressed to using the NHB rate for a ZNHB transfer, the transaction
// would fail loudly with "insufficient balance" instead of silently passing
// with a wrong-but-plausible fee amount.
func TestApplyTransferZNHB_FeeUsesZNHBRateNotNHBRate(t *testing.T) {
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
	collector[19] = 0x99

	sp.SetTransferGasPolicy(TransferGasPolicy{
		// Enabled=false forces the fee to always apply regardless of
		// free-tier bookkeeping (freeTransferGas can only become true when
		// Enabled is true), keeping this test focused purely on which rate
		// gets charged.
		Enabled:      false,
		FeeCollector: collector,
		FeeBps:       9_999,
		FeeBpsZNHB:   10,
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceZNHB: big.NewInt(250_250), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(250_000),
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
	if updatedSender.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected sender ZNHB balance 0 (250000 sent + 250 fee from a 250250 balance), got %s", updatedSender.BalanceZNHB)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if updatedRecipient.BalanceZNHB.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("expected recipient ZNHB balance 250000, got %s", updatedRecipient.BalanceZNHB)
	}
	collectorAcc, err := sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector: %v", err)
	}
	if collectorAcc.BalanceZNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected collector to receive 250 ZNHB (10bps of 250000, the ZNHB rate), got %s", collectorAcc.BalanceZNHB)
	}
}

// TestApplyTransferZNHB_SelfCollectionAtZNHBRate proves the fee-collector
// self-collection path (the collector wallet is credited back its own fee
// when it originates the transfer) still works correctly for a ZNHB
// transfer at the new 10bps rate -- the founder's explicit requirement that
// "the admin wallet earns ZNHB fees even when the admin wallet itself
// originates the transfer" keeps working exactly as before.
func TestApplyTransferZNHB_SelfCollectionAtZNHBRate(t *testing.T) {
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
	copy(collector[:], senderAddr) // the fee collector IS the sender

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:      false,
		FeeCollector: collector,
		FeeBps:       9_999,
		FeeBpsZNHB:   10,
	})

	initialBalance := big.NewInt(1_000_000)
	if err := sp.setAccount(senderAddr, &types.Account{BalanceZNHB: new(big.Int).Set(initialBalance), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(250_000),
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
	// The 10bps ZNHB fee (250) is deducted then immediately credited back
	// to the sender, since the sender IS the configured fee collector --
	// net cost is just the 250000 actually transferred out, not
	// 250000+fee. If self-collection were broken, the sender would show
	// initialBalance-250000-250 instead.
	expectedSender := new(big.Int).Sub(initialBalance, big.NewInt(250_000))
	if updatedSender.BalanceZNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("expected sender (fee collector) ZNHB balance %s after self-collected fee, got %s", expectedSender, updatedSender.BalanceZNHB)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if updatedRecipient.BalanceZNHB.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("expected recipient ZNHB balance 250000, got %s", updatedRecipient.BalanceZNHB)
	}
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

// TestApplyTransferZNHB_FeeToAdminWalletKeepsSupplyInvariantSatisfied
// reproduces the 2026-09-05 production chain halt: a plain wallet-to-wallet
// ZNHB transfer whose sender has exhausted the free tier pays a protocol fee
// that lands on the admin/treasury wallet (the live default FeeCollector).
// Before the fix, that credit grew BalanceZNHB with no offsetting change to
// the Sale/Reward Pool ledger, so CheckZNHBSupplyInvariant -- called every
// block -- failed on the very next block and no validator could ever build
// past it, halting the whole chain (see core/state_transition.go's
// applyTransferZNHB fee-routing comment). This proves the fee now lands in
// the Reward Pool too, so the invariant it must satisfy every block still
// holds immediately after the transfer that used to break it.
func TestApplyTransferZNHB_FeeToAdminWalletKeepsSupplyInvariantSatisfied(t *testing.T) {
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
		BalanceZNHB: big.NewInt(996_987_257),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap znhb pools: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold right after bootstrap: %v", err)
	}

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:      false, // force the fee regardless of free-tier bookkeeping
		FeeCollector: adminAddr,
		FeeBps:       9_999,
		FeeBpsZNHB:   10, // matches production's live 10bps ZNHB rate
	})

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

	// 1000 ZNHB at 10bps is exactly a 1 ZNHB fee -- the live incident's
	// exact reproduction amount.
	sendAmount := big.NewInt(1000)
	if err := sp.setAccount(senderAddr, &types.Account{BalanceZNHB: big.NewInt(1_001), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    sendAmount,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated after a ZNHB transfer fee landed on the admin wallet -- this is the exact 2026-09-05 chain-halt bug: %v", err)
	}
}
