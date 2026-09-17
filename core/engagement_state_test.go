package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestEngagementEMAScore(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	cfg := sp.EngagementConfig()
	cfg.HeartbeatWeight = 2
	cfg.TxWeight = 5
	cfg.EscrowWeight = 7
	cfg.GovWeight = 11
	cfg.DailyCap = 1000
	cfg.LambdaNumerator = 1
	cfg.LambdaDenominator = 2
	cfg.MaxMinutesPerHeartbeat = 1
	if err := sp.SetEngagementConfig(cfg); err != nil {
		t.Fatalf("set engagement config: %v", err)
	}

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address()
	initial := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := sp.setAccount(addr.Bytes(), initial); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	device := "device-ema"
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	sendHeartbeat := func(ts time.Time, nonce uint64) {
		payload := types.HeartbeatPayload{DeviceID: device, Timestamp: ts.Unix()}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeHeartbeat,
			Nonce:    nonce,
			Data:     data,
			GasLimit: 21000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(priv.PrivateKey); err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		sender, err := tx.From()
		if err != nil {
			t.Fatalf("derive sender: %v", err)
		}
		account, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		if err := sp.applyHeartbeat(tx, sender, account); err != nil {
			t.Fatalf("apply heartbeat: %v", err)
		}
	}

	// Day 1: 5 minutes + additional activity.
	for i := 0; i < 5; i++ {
		sendHeartbeat(base.Add(time.Duration(i+1)*time.Minute), uint64(i))
	}
	if err := sp.recordEngagementActivity(addr.Bytes(), base.Add(6*time.Minute), 2, 1, 1); err != nil {
		t.Fatalf("record day1 activity: %v", err)
	}
	sendHeartbeat(base.Add(24*time.Hour+time.Minute), 5)

	account, err := sp.getAccount(addr.Bytes())
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if account.EngagementScore != 19 {
		t.Fatalf("expected score 19 after day1, got %d", account.EngagementScore)
	}
	if account.EngagementMinutes != 1 {
		t.Fatalf("expected day2 minutes start at 1, got %d", account.EngagementMinutes)
	}

	// Day 2 contributions.
	if err := sp.recordEngagementActivity(addr.Bytes(), base.Add(24*time.Hour+2*time.Hour), 1, 0, 1); err != nil {
		t.Fatalf("record day2 activity: %v", err)
	}
	sendHeartbeat(base.Add(24*time.Hour+2*time.Minute), 6)
	sendHeartbeat(base.Add(24*time.Hour+3*time.Minute), 7)
	sendHeartbeat(base.Add(48*time.Hour+time.Minute), 8)

	account, err = sp.getAccount(addr.Bytes())
	if err != nil {
		t.Fatalf("load account after day2: %v", err)
	}
	if account.EngagementScore != 20 {
		t.Fatalf("expected score 20 after day2, got %d", account.EngagementScore)
	}
	if account.EngagementMinutes != 1 {
		t.Fatalf("expected new day minutes = 1, got %d", account.EngagementMinutes)
	}
}

func TestEngagementDailyCap(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	cfg := sp.EngagementConfig()
	cfg.HeartbeatWeight = 5
	cfg.TxWeight = 0
	cfg.EscrowWeight = 0
	cfg.GovWeight = 0
	cfg.DailyCap = 100
	cfg.LambdaNumerator = 0
	cfg.LambdaDenominator = 1
	cfg.MaxMinutesPerHeartbeat = 60
	if err := sp.SetEngagementConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address()
	if err := sp.setAccount(addr.Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		payload := types.HeartbeatPayload{DeviceID: "cap-test", Timestamp: base.Add(time.Duration(i+1) * time.Minute).Unix()}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeHeartbeat,
			Nonce:    uint64(i),
			Data:     data,
			GasLimit: 21000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(priv.PrivateKey); err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		sender, err := tx.From()
		if err != nil {
			t.Fatalf("derive sender: %v", err)
		}
		account, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		if err := sp.applyHeartbeat(tx, sender, account); err != nil {
			t.Fatalf("apply heartbeat: %v", err)
		}
	}
	payload := types.HeartbeatPayload{DeviceID: "cap-test", Timestamp: base.Add(24*time.Hour + time.Minute).Unix()}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    30,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(priv.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	var from []byte
	from, err = tx.From()
	if err != nil {
		t.Fatalf("derive sender: %v", err)
	}
	senderAccount, err := sp.getAccount(from)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if err := sp.applyHeartbeat(tx, from, senderAccount); err != nil {
		t.Fatalf("final heartbeat: %v", err)
	}

	account, err := sp.getAccount(addr.Bytes())
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if account.EngagementScore != cfg.DailyCap {
		t.Fatalf("expected capped score %d, got %d", cfg.DailyCap, account.EngagementScore)
	}
}
