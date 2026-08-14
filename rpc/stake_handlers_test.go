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
)

// TestStakeClaimRewardsRPCDisabled replaces TestStakeClaimRPC_Success/
// TestStakeClaimRPC_NotDue/TestStakeClaimRewardsPaused, which drove real
// reward-payout behavior through handleStakeClaimRewards. That handler used
// to call s.node.StakeClaimRewards(addr) directly under n.stateMu.Lock(),
// mutating live state completely outside CreateBlock/ApplyTransaction/
// ValidateBlock -- the same consensus-bypass bug already fixed for
// lend_createPool, governance, and POTSO stake lock/unbond/withdraw. It is
// now permanently disabled (mirroring handleStakeDelegate/Undelegate/Claim
// just above); real reward claims go through a signed TxTypeStakeClaimRewards
// transaction instead (see core/state_transition.go's applyStakeClaimRewards
// and core/staking_claim_rewards_tx_test.go for the real functional/
// consensus coverage of that path).
func TestStakeClaimRewardsRPCDisabled(t *testing.T) {
	env := newTestEnv(t)

	claimReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, "nhb1invalidplaceholderaddressvalue0000000")}}
	rec := httptest.NewRecorder()
	env.server.handleStakeClaimRewards(rec, env.newRequest(), claimReq)

	if rec.Code != http.StatusGone {
		t.Fatalf("unexpected HTTP status: got %d want %d", rec.Code, http.StatusGone)
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled-method error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("unexpected error code: got %d want %d", rpcErr.Code, codeMethodDisabled)
	}
	if rpcErr.Message != stakeRPCDisabledMessage {
		t.Fatalf("unexpected error message: got %q want %q", rpcErr.Message, stakeRPCDisabledMessage)
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
