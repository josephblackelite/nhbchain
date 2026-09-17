package rpc

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

func addressFromKey(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}

// TestPotsoStakeInfoReportsLocks covers the only potso_stake_* RPC method
// that still exists (handlePotsoStakeInfo, read-only) -- lock/unbond/withdraw
// are real signed transactions now (TxTypePotsoStakeLock/Unbond/Withdraw,
// core/potso_stake_tx.go), submitted via nhb_sendTransaction like every
// other signed native transaction type, not bespoke RPC methods; their
// lifecycle (including replay rejection via the standard account nonce) is
// covered at the state-transition level by core/potso_stake_test.go's
// TestPotsoStakeLifecycle. This test seeds lock state directly (the shape a
// real TxTypePotsoStakeLock/Unbond would have produced) purely to verify
// potso_stake_info reads it back correctly.
func TestPotsoStakeInfoReportsLocks(t *testing.T) {
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
		account.BalanceZNHB = big.NewInt(1400)
		if err := manager.PutAccount(ownerBytes[:], account); err != nil {
			return err
		}
		bondedLock := &potso.StakeLock{Owner: ownerBytes, Amount: big.NewInt(600), CreatedAt: 1}
		if err := manager.PotsoStakePutLock(ownerBytes, 1, bondedLock); err != nil {
			return err
		}
		unbondingLock := &potso.StakeLock{Owner: ownerBytes, Amount: big.NewInt(400), CreatedAt: 1, UnbondAt: 2, WithdrawAt: 999999999999}
		if err := manager.PotsoStakePutLock(ownerBytes, 2, unbondingLock); err != nil {
			return err
		}
		if err := manager.PotsoStakePutLockNonces(ownerBytes, []uint64{1, 2}); err != nil {
			return err
		}
		return manager.PotsoStakeSetBondedTotal(ownerBytes, big.NewInt(600))
	}); err != nil {
		t.Fatalf("seed stake state: %v", err)
	}

	infoParams := potsoStakeInfoParams{Owner: owner}
	infoReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, infoParams)}}
	infoRec := httptest.NewRecorder()
	env.server.handlePotsoStakeInfo(infoRec, env.newRequest(), infoReq)
	result, rpcErr := decodeRPCResponse(t, infoRec)
	if rpcErr != nil {
		t.Fatalf("info rpc error: %+v", rpcErr)
	}
	var infoResp potsoStakeInfoResult
	if err := json.Unmarshal(result, &infoResp); err != nil {
		t.Fatalf("decode info response: %v", err)
	}
	if infoResp.Bonded != "600" {
		t.Fatalf("expected bonded 600, got %s", infoResp.Bonded)
	}
	if infoResp.PendingUnbond != "400" {
		t.Fatalf("expected pendingUnbond 400, got %s", infoResp.PendingUnbond)
	}
	if len(infoResp.Locks) != 2 {
		t.Fatalf("expected two locks, got %+v", infoResp.Locks)
	}
}
