package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

func signStake(t *testing.T, key *crypto.PrivateKey, action, owner string, amount *big.Int) string {
	t.Helper()
	digest := potsoStakeDigest(action, owner, amount)
	sig, err := ethcrypto.Sign(digest, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign stake action: %v", err)
	}
	return "0x" + hex.EncodeToString(sig)
}

func addressFromKey(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}

func TestPotsoStakeHandlersFlow(t *testing.T) {
	env := newTestEnv(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := ownerKey.PubKey().Address().String()
	ownerBytes := addressFromKey(ownerKey)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(ownerBytes[:])
		if err != nil {
			return err
		}
		account.BalanceZNHB = big.NewInt(1000)
		return manager.PutAccount(ownerBytes[:], account)
	}); err != nil {
		t.Fatalf("fund owner: %v", err)
	}

	lockParams := potsoStakeLockParams{Owner: owner, Amount: "600", Signature: signStake(t, ownerKey, "lock", owner, big.NewInt(600))}
	lockReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, lockParams)}}
	rec := httptest.NewRecorder()
	env.server.handlePotsoStakeLock(rec, env.newRequest(), lockReq)
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("lock rpc error: %+v", rpcErr)
	}
	var lockResp potsoStakeLockResult
	if err := json.Unmarshal(result, &lockResp); err != nil {
		t.Fatalf("decode lock response: %v", err)
	}
	if !lockResp.OK || lockResp.Nonce == 0 {
		t.Fatalf("unexpected lock response: %+v", lockResp)
	}

	unbondParams := potsoStakeUnbondParams{Owner: owner, Amount: "400", Signature: signStake(t, ownerKey, "unbond", owner, big.NewInt(400))}
	unbondReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, unbondParams)}}
	unbondRec := httptest.NewRecorder()
	env.server.handlePotsoStakeUnbond(unbondRec, env.newRequest(), unbondReq)
	result, rpcErr = decodeRPCResponse(t, unbondRec)
	if rpcErr != nil {
		t.Fatalf("unbond rpc error: %+v", rpcErr)
	}
	var unbondResp potsoStakeUnbondResult
	if err := json.Unmarshal(result, &unbondResp); err != nil {
		t.Fatalf("decode unbond response: %v", err)
	}
	if !unbondResp.OK || unbondResp.Amount != "400" || unbondResp.WithdrawAt == 0 {
		t.Fatalf("unexpected unbond response: %+v", unbondResp)
	}

	withdrawParams := potsoStakeWithdrawParams{Owner: owner, Signature: signStake(t, ownerKey, "withdraw", owner, nil)}
	withdrawReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, withdrawParams)}}
	earlyRec := httptest.NewRecorder()
	env.server.handlePotsoStakeWithdraw(earlyRec, env.newRequest(), withdrawReq)
	if _, rpcErr = decodeRPCResponse(t, earlyRec); rpcErr == nil {
		t.Fatalf("expected early withdraw to fail")
	}

	past := uint64(time.Now().Add(-time.Hour).Unix())
	originalDay := potso.WithdrawDay(unbondResp.WithdrawAt)
	newDay := potso.WithdrawDay(past)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		entries, err := manager.PotsoStakeQueueEntries(originalDay)
		if err != nil {
			return err
		}
		if err := manager.PotsoStakePutQueueEntries(originalDay, nil); err != nil {
			return err
		}
		for _, entry := range entries {
			lock, ok, getErr := manager.PotsoStakeGetLock(ownerBytes, entry.Nonce)
			if getErr != nil {
				return getErr
			}
			if !ok {
				continue
			}
			lock.WithdrawAt = past
			if err := manager.PotsoStakePutLock(ownerBytes, entry.Nonce, lock); err != nil {
				return err
			}
			entry.Amount = new(big.Int).Set(lock.Amount)
			if err := manager.PotsoStakeQueueAppend(newDay, entry); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("adjust queue: %v", err)
	}

	withdrawRec := httptest.NewRecorder()
	env.server.handlePotsoStakeWithdraw(withdrawRec, env.newRequest(), withdrawReq)
	result, rpcErr = decodeRPCResponse(t, withdrawRec)
	if rpcErr != nil {
		t.Fatalf("withdraw matured error: %+v", rpcErr)
	}
	var withdrawResp potsoStakeWithdrawResult
	if err := json.Unmarshal(result, &withdrawResp); err != nil {
		t.Fatalf("decode withdraw response: %v", err)
	}
	if len(withdrawResp.Withdrawn) == 0 {
		t.Fatalf("expected payouts, got none")
	}
	if withdrawResp.Withdrawn[0].Amount == "" {
		t.Fatalf("missing amount in payout")
	}

	infoParams := potsoStakeInfoParams{Owner: owner}
	infoReq := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, infoParams)}}
	infoRec := httptest.NewRecorder()
	env.server.handlePotsoStakeInfo(infoRec, env.newRequest(), infoReq)
	result, rpcErr = decodeRPCResponse(t, infoRec)
	if rpcErr != nil {
		t.Fatalf("info rpc error: %+v", rpcErr)
	}
	var infoResp potsoStakeInfoResult
	if err := json.Unmarshal(result, &infoResp); err != nil {
		t.Fatalf("decode info response: %v", err)
	}
	if infoResp.Bonded == "" || infoResp.PendingUnbond == "" || infoResp.Withdrawable == "" {
		t.Fatalf("unexpected info payload: %+v", infoResp)
	}
}
