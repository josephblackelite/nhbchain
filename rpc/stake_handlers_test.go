package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

func TestStakeClaimRewardsFlow(t *testing.T) {
	env := newTestEnv(t)

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	delegator := delegatorKey.PubKey().Address()
	var delegatorBytes [20]byte
	copy(delegatorBytes[:], delegator.Bytes())

	shares := big.NewInt(5_000)
	globalIndex := big.NewInt(1_500)
	now := time.Now().UTC()
	payoutPeriod := 30 * 24 * time.Hour
	lastPayout := now.Add(-2 * payoutPeriod)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.StakeShares = new(big.Int).Set(shares)
		account.StakeLastIndex = big.NewInt(0)
		account.StakeLastPayoutTs = uint64(lastPayout.Unix())
		account.BalanceZNHB = big.NewInt(0)
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(globalIndex)
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())

	previewReq := &RPCRequest{ID: 1, Params: []json.RawMessage{addrParam}}
	previewRec := httptest.NewRecorder()
	env.server.handleStakePreviewClaim(previewRec, env.newRequest(), previewReq)
	previewResult, rpcErr := decodeRPCResponse(t, previewRec)
	if rpcErr != nil {
		t.Fatalf("preview error: %+v", rpcErr)
	}
	var previewResp stakePreviewClaimResult
	if err := json.Unmarshal(previewResult, &previewResp); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	payable := new(big.Int)
	if _, ok := payable.SetString(previewResp.Payable, 10); !ok {
		t.Fatalf("invalid payable amount: %s", previewResp.Payable)
	}
	if payable.Sign() <= 0 {
		t.Fatalf("expected positive payable reward, got %s", payable)
	}

	positionRec := httptest.NewRecorder()
	env.server.handleStakeGetPosition(positionRec, env.newRequest(), previewReq)
	positionResult, rpcErr := decodeRPCResponse(t, positionRec)
	if rpcErr != nil {
		t.Fatalf("position error: %+v", rpcErr)
	}
	var positionResp stakePositionResult
	if err := json.Unmarshal(positionResult, &positionResp); err != nil {
		t.Fatalf("decode position: %v", err)
	}
	if positionResp.Shares != shares.String() {
		t.Fatalf("unexpected shares: got %s want %s", positionResp.Shares, shares)
	}
	if positionResp.LastPayoutTs != uint64(lastPayout.Unix()) {
		t.Fatalf("unexpected last payout ts: got %d want %d", positionResp.LastPayoutTs, uint64(lastPayout.Unix()))
	}

	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), previewReq)
	claimResult, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr != nil {
		t.Fatalf("claim error: %+v", rpcErr)
	}
	var claimResp stakeClaimRewardsResult
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	minted := new(big.Int)
	if _, ok := minted.SetString(claimResp.Minted, 10); !ok {
		t.Fatalf("invalid minted amount: %s", claimResp.Minted)
	}
	if minted.Cmp(payable) != 0 {
		t.Fatalf("minted mismatch: got %s want %s", minted, payable)
	}
	if claimResp.Balance.BalanceZNHB == nil {
		t.Fatalf("expected balance payload")
	}
	if claimResp.Balance.BalanceZNHB.String() == "0" {
		t.Fatalf("expected ZNHB balance to increase")
	}
	if claimResp.NextPayoutTs <= uint64(time.Now().Unix()) {
		t.Fatalf("expected next payout to be in the future, got %d", claimResp.NextPayoutTs)
	}

	postPreviewRec := httptest.NewRecorder()
	env.server.handleStakePreviewClaim(postPreviewRec, env.newRequest(), previewReq)
	postPreviewResult, rpcErr := decodeRPCResponse(t, postPreviewRec)
	if rpcErr != nil {
		t.Fatalf("post-claim preview error: %+v", rpcErr)
	}
	var postPreview stakePreviewClaimResult
	if err := json.Unmarshal(postPreviewResult, &postPreview); err != nil {
		t.Fatalf("decode post preview: %v", err)
	}
	if postPreview.Payable != "0" {
		t.Fatalf("expected zero payable after claim, got %s", postPreview.Payable)
	}
}

func TestStakeClaimRewardsEarly(t *testing.T) {
	env := newTestEnv(t)

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
		account.StakeShares = big.NewInt(1_000)
		account.StakeLastIndex = big.NewInt(0)
		account.StakeLastPayoutTs = uint64(time.Now().UTC().Unix())
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(big.NewInt(2_000))
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())
	claimReq := &RPCRequest{ID: 2, Params: []json.RawMessage{addrParam}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	_, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr == nil {
		t.Fatalf("expected error for early claim")
	}
	if rpcErr.Message != "failed to claim staking rewards" {
		t.Fatalf("unexpected error message: %+v", rpcErr)
	}
	if rpcErr.Data == nil {
		t.Fatalf("expected rejection details in error data")
	}
}

func TestStakeClaimRewardsPaused(t *testing.T) {
	env := newTestEnv(t)

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
		account.StakeShares = big.NewInt(2_000)
		account.StakeLastIndex = big.NewInt(0)
		account.StakeLastPayoutTs = uint64(time.Now().Add(-60 * 24 * time.Hour).Unix())
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(big.NewInt(3_000))
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
	var claimResp stakeClaimRewardsResult
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if claimResp.Minted == "0" {
		t.Fatalf("expected positive minted rewards after unpause")
	}
}

func TestStakeHandlersResumeAfterUnpause(t *testing.T) {
	env := newTestEnv(t)

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
