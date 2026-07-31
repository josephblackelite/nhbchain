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
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

func TestStakeClaimRPC_Success(t *testing.T) {
	env := newTestEnv(t)

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
	stakeBalance := big.NewInt(1)
	lastPayout := now.Add(-2 * payoutPeriod)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = new(big.Int).Set(stakeBalance)
		account.BalanceZNHB = big.NewInt(0)
		account.StakeShares = new(big.Int).Set(stakeBalance)
		account.StakeLastIndex = rewards.IndexUnit()
		account.StakeLastPayoutTs = uint64(lastPayout.Unix())
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], &nhbstate.AccountSnap{LastPayoutUnix: lastPayout.Unix(), AccruedZNHB: big.NewInt(0)}); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(new(big.Int).Add(rewards.IndexUnit(), accrued))
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
	if claimResp.Minted == "0" {
		t.Fatalf("expected positive paid amount, got %s", claimResp.Minted)
	}
	if claimResp.Periods != 2 {
		t.Fatalf("unexpected period count: got %d want %d", claimResp.Periods, 2)
	}
	if claimResp.NextEligible <= uint64(now.Unix()) {
		t.Fatalf("expected future next eligibility, got %d", claimResp.NextEligible)
	}
}

func TestStakeClaimRPC_NotDue(t *testing.T) {
	env := newTestEnv(t)

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

	stakeBalance := big.NewInt(1)

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = new(big.Int).Set(stakeBalance)
		account.StakeShares = new(big.Int).Set(stakeBalance)
		account.StakeLastIndex = rewards.IndexUnit()
		account.StakeLastPayoutTs = uint64(now.Add(time.Second).Unix())
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], &nhbstate.AccountSnap{LastPayoutUnix: now.Add(time.Second).Unix(), AccruedZNHB: big.NewInt(0)}); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(new(big.Int).Add(rewards.IndexUnit(), big.NewInt(2_000)))
	}); err != nil {
		t.Fatalf("prepare account: %v", err)
	}

	addrParam := marshalParam(t, delegator.String())
	claimReq := &RPCRequest{ID: 2, Params: []json.RawMessage{addrParam}}
	claimRec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(claimRec, env.newRequest(), claimReq)
	if claimRec.Code == http.StatusConflict {
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
		nextEligible, exists := data["nextEligibleTs"]
		if !exists {
			t.Fatalf("expected next_eligible hint in error data")
		}
		expectedNext := float64(now.Add(payoutPeriod).Unix())
		if value, ok := nextEligible.(float64); !ok || value != expectedNext {
			t.Fatalf("unexpected next_eligible hint: got %v want %v", nextEligible, expectedNext)
		}
		return
	}
	if claimRec.Code != http.StatusOK {
		t.Fatalf("unexpected HTTP status: got %d want %d or %d", claimRec.Code, http.StatusConflict, http.StatusOK)
	}
	claimResult, rpcErr := decodeRPCResponse(t, claimRec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var claimResp stakeClaimRewardsResponse
	if err := json.Unmarshal(claimResult, &claimResp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if claimResp.NextEligible <= uint64(now.Unix()) {
		t.Fatalf("expected future next eligibility hint, got %d", claimResp.NextEligible)
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

	payoutPeriod := 30 * 24 * time.Hour
	now := time.Unix(1_700_200_000, 0).UTC()
	env.node.SetTimeSource(func() time.Time { return now })
	t.Cleanup(func() { env.node.SetTimeSource(nil) })

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		account.LockedZNHB = big.NewInt(1)
		account.StakeShares = new(big.Int).Set(account.LockedZNHB)
		account.StakeLastIndex = rewards.IndexUnit()
		account.StakeLastPayoutTs = uint64(now.Add(-2 * payoutPeriod).Unix())
		if err := manager.PutAccount(delegatorBytes[:], account); err != nil {
			return err
		}
		if err := manager.PutStakingSnap(delegatorBytes[:], &nhbstate.AccountSnap{LastPayoutUnix: now.Add(-2 * payoutPeriod).Unix(), AccruedZNHB: big.NewInt(0)}); err != nil {
			return err
		}
		return manager.SetStakingGlobalIndex(new(big.Int).Add(rewards.IndexUnit(), big.NewInt(5_000)))
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
	if claimResp.Minted == "0" {
		t.Fatalf("expected positive minted rewards after unpause")
	}
}

// TestStakeHandlersResumeAfterUnpause exercises the pause guard plus the
// full delegate -> undelegate -> claim lifecycle. It previously drove this
// through handleStakeDelegate/handleStakeUndelegate/handleStakeClaim, which
// are now disabled (docs/issue30.md item 3) since they let an
// unauthenticated caller move funds for any address. It now calls the same
// underlying Node methods directly -- the exact path nhbportal actually
// uses (via a signed TxTypeStake/TxTypeUnstake/TxTypeStakeClaim
// transaction) -- so this test still verifies the real lifecycle and pause
// behavior without depending on the removed RPC surface.
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

	if _, err := env.node.StakeDelegate(delegatorBytes, big.NewInt(500), nil); err == nil {
		t.Fatalf("expected delegate to be rejected while paused")
	} else if !errors.Is(err, core.ErrStakePaused) && !errors.Is(err, stakeerrors.ErrStakingPaused) {
		t.Fatalf("unexpected delegate-while-paused error: %v", err)
	}

	env.node.SetModulePaused("staking", false)

	delegateAccount, err := env.node.StakeDelegate(delegatorBytes, big.NewInt(500), nil)
	if err != nil {
		t.Fatalf("delegate error: %v", err)
	}
	if delegateAccount.Stake == nil || delegateAccount.Stake.String() != "500" {
		t.Fatalf("unexpected stake balance: %+v", delegateAccount.Stake)
	}
	if delegateAccount.BalanceZNHB == nil || delegateAccount.BalanceZNHB.String() != "1500" {
		t.Fatalf("unexpected liquid balance: %+v", delegateAccount.BalanceZNHB)
	}

	unbond, err := env.node.StakeUndelegate(delegatorBytes, big.NewInt(200))
	if err != nil {
		t.Fatalf("undelegate error: %v", err)
	}
	if unbond.Amount == nil || unbond.Amount.String() != "200" {
		t.Fatalf("unexpected unbond amount: %+v", unbond.Amount)
	}

	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(delegatorBytes[:])
		if err != nil {
			return err
		}
		for i := range account.PendingUnbonds {
			if account.PendingUnbonds[i].ID == unbond.ID {
				account.PendingUnbonds[i].ReleaseTime = uint64(time.Now().Add(-time.Hour).Unix())
			}
		}
		return manager.PutAccount(delegatorBytes[:], account)
	}); err != nil {
		t.Fatalf("mature unbond: %v", err)
	}

	claimed, err := env.node.StakeClaim(delegatorBytes, unbond.ID)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if claimed.ID != unbond.ID {
		t.Fatalf("unexpected claimed id: got %d want %d", claimed.ID, unbond.ID)
	}
	claimedAccount, err := env.node.GetAccount(delegatorBytes[:])
	if err != nil {
		t.Fatalf("load post-claim account: %v", err)
	}
	if claimedAccount.BalanceZNHB == nil || claimedAccount.BalanceZNHB.String() != "1700" {
		t.Fatalf("unexpected post-claim balance: %+v", claimedAccount.BalanceZNHB)
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
