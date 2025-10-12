package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/core"
	stakeerrors "nhbchain/core/errors"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"

	"github.com/ethereum/go-ethereum/common"
)

func TestStakeClaim_NotReady(t *testing.T) {
	env := newTestEnv(t)

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := delegatorKey.PubKey().Address().String()

	claimReq := &RPCRequest{ID: 99, Params: []json.RawMessage{marshalParam(t, addr)}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)

	if claimRec.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected HTTP status: got %d want %d", claimRec.Code, http.StatusNotImplemented)
	}
	_, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr == nil {
		t.Fatalf("expected staking not ready error")
	}
	if rpcErr.Message != "staking not ready" {
		t.Fatalf("unexpected error message: %+v", rpcErr)
	}
}

func TestStakeClaimRPC_Success(t *testing.T) {
	env := newTestEnv(t)

	if _, _, _, err := env.node.StakeClaimRewards(common.Address{}); errors.Is(err, core.ErrStakingNotReady) {
		t.Skip("staking rewards claim not yet available")
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	delegator := delegatorKey.PubKey().Address()
	var delegatorBytes [20]byte
	copy(delegatorBytes[:], delegator.Bytes())

	payoutPeriod := 30 * 24 * time.Hour
	now := time.Unix(1_700_000_000, 0).UTC()
	env.node.SetTimeSource(func() time.Time { return now })
	t.Cleanup(func() { env.node.SetTimeSource(nil) })

	accrued := big.NewInt(1_000)
	stakeBalance := big.NewInt(1_000_000_000_000_000_000)
	lastPayout := now.Add(-2 * payoutPeriod)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = new(big.Int).Set(stakeBalance)
		account.BalanceZNHB = big.NewInt(0)
		account.StakeShares = new(big.Int).Set(stakeBalance)
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		snap := &nhbstate.AccountSnap{
			AccruedZNHB:    new(big.Int).Set(accrued),
			LastPayoutUnix: lastPayout.Unix(),
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], snap); err != nil {
			return err
		}
		return manager.PutGlobalIndex(&nhbstate.GlobalIndex{LastUpdateUnix: now.Unix()})
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())
	claimReq := &RPCRequest{ID: 1, Params: []json.RawMessage{addrParam}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	claimResult, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr != nil {
		t.Fatalf("claim error: %+v", rpcErr)
	}
	var claimResp stakeClaimRewardsResponse
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claimResp.Paid != accrued.String() {
		t.Fatalf("unexpected paid amount: got %s want %s", claimResp.Paid, accrued.String())
	}
	if claimResp.Periods != 2 {
		t.Fatalf("unexpected period count: got %d want %d", claimResp.Periods, 2)
	}
	expectedNext := uint64(now.Add(payoutPeriod).Unix())
	if claimResp.NextEligible != expectedNext {
		t.Fatalf("unexpected next eligibility: got %d want %d", claimResp.NextEligible, expectedNext)
	}
}

func TestStakeClaimRPC_NotDue(t *testing.T) {
	env := newTestEnv(t)

	if _, _, _, err := env.node.StakeClaimRewards(common.Address{}); errors.Is(err, core.ErrStakingNotReady) {
		t.Skip("staking rewards claim not yet available")
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	delegator := delegatorKey.PubKey().Address()
	var delegatorBytes [20]byte
	copy(delegatorBytes[:], delegator.Bytes())

	payoutPeriod := 30 * 24 * time.Hour
	now := time.Unix(1_700_050_000, 0).UTC()
	env.node.SetTimeSource(func() time.Time { return now })
	t.Cleanup(func() { env.node.SetTimeSource(nil) })

	stakeBalance := big.NewInt(500_000_000_000_000_000)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = new(big.Int).Set(stakeBalance)
		account.StakeShares = new(big.Int).Set(stakeBalance)
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		snap := &nhbstate.AccountSnap{
			AccruedZNHB:    big.NewInt(2_000),
			LastPayoutUnix: now.Unix(),
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], snap); err != nil {
			return err
		}
		return manager.PutGlobalIndex(&nhbstate.GlobalIndex{LastUpdateUnix: now.Unix()})
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())
	claimReq := &RPCRequest{ID: 2, Params: []json.RawMessage{addrParam}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	if claimRec.Code != http.StatusConflict {
		t.Fatalf("unexpected HTTP status: got %d want %d", claimRec.Code, http.StatusConflict)
	}
	_, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr == nil {
		t.Fatalf("expected error for early claim")
	}
	if rpcErr.Message != stakeerrors.ErrNotDue.Error() {
		t.Fatalf("unexpected error message: %+v", rpcErr)
	}
	data, ok := rpcErr.Data.(map[string]interface{})
	if !ok || data == nil {
		t.Fatalf("expected rejection details in error data")
	}
	nextEligible, exists := data["next_eligible"]
	if !exists {
		t.Fatalf("expected next_eligible hint in error data")
	}
	expectedNext := float64(now.Add(payoutPeriod).Unix())
	if value, ok := nextEligible.(float64); !ok || value != expectedNext {
		t.Fatalf("unexpected next_eligible hint: got %v want %v", nextEligible, expectedNext)
	}
}

func TestStakeClaimRewardsPaused(t *testing.T) {
	env := newTestEnv(t)

	if _, _, _, err := env.node.StakeClaimRewards(common.Address{}); errors.Is(err, core.ErrStakingNotReady) {
		t.Skip("staking rewards claim not yet available")
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	delegator := delegatorKey.PubKey().Address()
	var delegatorBytes [20]byte
	copy(delegatorBytes[:], delegator.Bytes())

	payoutPeriod := 30 * 24 * time.Hour
	now := time.Unix(1_700_200_000, 0).UTC()
	env.node.SetTimeSource(func() time.Time { return now })
	t.Cleanup(func() { env.node.SetTimeSource(nil) })

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = big.NewInt(3_000_000_000_000_000_000)
		account.StakeShares = new(big.Int).Set(account.LockedZNHB)
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		snap := &nhbstate.AccountSnap{
			AccruedZNHB:    big.NewInt(5_000),
			LastPayoutUnix: now.Add(-2 * payoutPeriod).Unix(),
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], snap); err != nil {
			return err
		}
		return manager.PutGlobalIndex(&nhbstate.GlobalIndex{LastUpdateUnix: now.Unix()})
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	env.node.SetModulePaused("staking", true)

	addrParam := marshalParam(t, delegator.String())
	claimReq := &RPCRequest{ID: 3, Params: []json.RawMessage{addrParam}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	_, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr == nil {
		t.Fatalf("expected pause rejection")
	}
	if rpcErr.Message != "staking module paused" {
		t.Fatalf("unexpected pause error: %+v", rpcErr)
	}
	if rpcErr.Code != codeModulePaused {
		t.Fatalf("unexpected pause error code: got %d want %d", rpcErr.Code, codeModulePaused)
	}

	env.node.SetModulePaused("staking", false)
	claimRec = httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	claimResult, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr != nil {
		t.Fatalf("claim error after unpause: %+v", rpcErr)
	}
	var claimResp stakeClaimRewardsResponse
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if claimResp.Paid == "0" {
		t.Fatalf("expected positive minted rewards after unpause")
	}
}

func TestStakeHandlersResumeAfterUnpause(t *testing.T) {
	env := newTestEnv(t)

	if _, _, _, err := env.node.StakeClaimRewards(common.Address{}); errors.Is(err, core.ErrStakingNotReady) {
		t.Skip("staking rewards claim not yet available")
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	delegator := delegatorKey.PubKey().Address()
	var delegatorBytes [20]byte
	copy(delegatorBytes[:], delegator.Bytes())

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.BalanceZNHB = big.NewInt(2_000)
		account.LockedZNHB = big.NewInt(0)
		account.Stake = big.NewInt(0)
		account.PendingUnbonds = nil
		return manager.PutAccount(delegatorBytes[:], account)
	}); err != nil {
		t.Fatalf("prepare delegator: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())
	previewReq := &RPCRequest{ID: 1, Params: []json.RawMessage{addrParam}}

	env.node.SetModulePaused("staking", true)
	previewRec := httptest.NewRecorder()
	env.server.handleStakePreviewClaim(previewRec, env.newRequest(), previewReq)
	_, rpcErr := decodeRPCResponse(t, previewRec)
	if rpcErr == nil {
		t.Fatalf("expected guard rejection while paused")
	}
	if rpcErr.Code != codeModulePaused {
		t.Fatalf("unexpected pause code: got %d want %d", rpcErr.Code, codeModulePaused)
	}

	env.node.SetModulePaused("staking", false)

	delegateReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, stakeDelegateParams{
		Caller: delegator.String(),
		Amount: "500",
	})}}
	delegateRec := httptest.NewRecorder()
	env.server.handleStakeDelegate(delegateRec, env.newRequest(), delegateReq)
	delegateResult, rpcErr := decodeRPCResponse(t, delegateRec)
	if rpcErr != nil {
		t.Fatalf("delegate error: %+v", rpcErr)
	}
	var delegateResp BalanceResponse
	if err := json.Unmarshal(delegateResult, &delegateResp); err != nil {
		t.Fatalf("decode delegate response: %v", err)
	}
	if delegateResp.Stake == nil || delegateResp.Stake.String() != "500" {
		t.Fatalf("unexpected stake balance: %+v", delegateResp.Stake)
	}
	if delegateResp.BalanceZNHB == nil || delegateResp.BalanceZNHB.String() != "1500" {
		t.Fatalf("unexpected liquid balance: %+v", delegateResp.BalanceZNHB)
	}

	undelegateReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, stakeUndelegateParams{
		Caller: delegator.String(),
		Amount: "200",
	})}}
	undelegateRec := httptest.NewRecorder()
	env.server.handleStakeUndelegate(undelegateRec, env.newRequest(), undelegateReq)
	undelegateResult, rpcErr := decodeRPCResponse(t, undelegateRec)
	if rpcErr != nil {
		t.Fatalf("undelegate error: %+v", rpcErr)
	}
	var unbondResp StakeUnbondResponse
	if err := json.Unmarshal(undelegateResult, &unbondResp); err != nil {
		t.Fatalf("decode undelegate response: %v", err)
	}
	if unbondResp.Amount == nil || unbondResp.Amount.String() != "200" {
		t.Fatalf("unexpected unbond amount: %+v", unbondResp.Amount)
	}

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		for i := range account.PendingUnbonds {
			if account.PendingUnbonds[i].ID == unbondResp.ID {
				account.PendingUnbonds[i].ReleaseTime = uint64(time.Now().Add(-time.Hour).Unix())
			}
		}
		return manager.PutAccount(delegatorBytes[:], account)
	}); err != nil {
		t.Fatalf("mature unbond: %v", err)
	}

	claimReq := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, stakeClaimParams{
		Caller:      delegator.String(),
		UnbondingID: unbondResp.ID,
	})}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaim(claimRec, env.newRequest(), claimReq)
	claimResult, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr != nil {
		t.Fatalf("claim error: %+v", rpcErr)
	}
	var claimPayload struct {
		Claimed StakeUnbondResponse `json:"claimed"`
		Balance BalanceResponse     `json:"balance"`
	}
	if err := json.Unmarshal(claimResult, &claimPayload); err != nil {
		t.Fatalf("decode claim payload: %v", err)
	}
	if claimPayload.Claimed.ID != unbondResp.ID {
		t.Fatalf("unexpected claimed id: got %d want %d", claimPayload.Claimed.ID, unbondResp.ID)
	}
	if claimPayload.Balance.BalanceZNHB == nil || claimPayload.Balance.BalanceZNHB.String() != "1700" {
		t.Fatalf("unexpected post-claim balance: %+v", claimPayload.Balance.BalanceZNHB)
	}

	previewRec = httptest.NewRecorder()
	env.server.handleStakePreviewClaim(previewRec, env.newRequest(), previewReq)
	if _, rpcErr = decodeRPCResponse(t, previewRec); rpcErr != nil {
		t.Fatalf("preview error after unpause: %+v", rpcErr)
	}

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		if len(account.PendingUnbonds) != 0 {
			return fmt.Errorf("pending unbonds not cleared")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify pending unbonds: %v", err)
	}
}
