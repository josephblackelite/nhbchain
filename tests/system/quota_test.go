package system

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestQuotaRequestLimitResetsNextEpoch(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}

	sp, err := core.NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	sp.SetQuotaConfig(map[string]nativecommon.Quota{
		"potso": {MaxRequestsPerMin: 2, EpochSeconds: 60},
	})

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address()

	manager := nhbstate.NewManager(sp.Trie)
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	if err := manager.PutAccount(addr.Bytes(), account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	start := time.Unix(1_700_000_000, 0).UTC()

	sp.BeginBlock(1, start)

	nextNonce := uint64(0)
	makeTx := func(nonce uint64, payload types.HeartbeatPayload) *types.Transaction {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeHeartbeat,
			Nonce:    nonce,
			GasLimit: 1,
			GasPrice: big.NewInt(0),
			Data:     data,
		}
		if err := tx.Sign(priv.PrivateKey); err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		return tx
	}

	for i := 0; i < 2; i++ {
		payload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(time.Duration(i+1) * time.Minute).Unix()}
		tx := makeTx(nextNonce, payload)
		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("heartbeat %d: %v", i+1, err)
		}
		nextNonce++
	}

	denyPayload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(3 * time.Minute).Unix()}
	denyTx := makeTx(nextNonce, denyPayload)
	if err := sp.ApplyTransaction(denyTx); err == nil || !errors.Is(err, nativecommon.ErrQuotaRequestsExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}

	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, start.Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}

	sp.BeginBlock(2, start.Add(time.Minute))
	allowPayload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(4 * time.Minute).Unix()}
	allowTx := makeTx(nextNonce, allowPayload)
	if err := sp.ApplyTransaction(allowTx); err != nil {
		t.Fatalf("heartbeat after epoch rollover: %v", err)
	}
	nextNonce++
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(2, start.Add(time.Minute).Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}
}
