package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestApplyStakeClaimRewards_TxDispatch drives a real signed
// TxTypeStakeClaimRewards transaction through sp.ApplyTransaction -- the
// standard consensus tx-dispatch path (handleNativeTransaction's
// TxTypeStakeClaimRewards case, applying via applyStakeClaimRewards) -- in
// place of the old rpc/stake_handlers.go handleStakeClaimRewards, which
// mutated state directly under n.stateMu.Lock() completely outside
// CreateBlock/ApplyTransaction/ValidateBlock. This proves the claimant is
// always the cryptographically recovered tx signer (never a client-supplied
// address param), the payout event fires, and the sender's nonce advances
// so the transaction cannot be replayed.
func TestApplyStakeClaimRewards_TxDispatch(t *testing.T) {
	sp := newStakingStateProcessor(t)

	start := time.Unix(1_750_000_000, 0).UTC()
	sp.nowFunc = func() time.Time { return start }
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var delegator [20]byte
	copy(delegator[:], delegatorKey.PubKey().Address().Bytes())

	// sp.PutAccount (not the writeAccount test helper) is required here: it
	// is the real setAccount path that actually persists StakeShares/
	// StakeLastIndex/StakeLastPayoutTs into account metadata --
	// writeAccount's lower-level accountMetadata literal omits those three
	// fields entirely, matching Test_StakeEvents_EmitOnTransitions'
	// PutAccount usage for the same reason.
	lastPayout := start.Add(-2 * time.Duration(stakePayoutPeriodSeconds) * time.Second)
	if err := sp.PutAccount(delegator[:], &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       rewards.IndexUnit(),
		StakeLastIndex:    big.NewInt(0),
		StakeLastPayoutTs: uint64(lastPayout.Unix()),
	}); err != nil {
		t.Fatalf("seed delegator account: %v", err)
	}

	boostedIndex := new(big.Int).Mul(rewards.IndexUnit(), big.NewInt(1_000))
	if err := sp.writeBigInt(nhbstate.StakingGlobalIndexKey(), boostedIndex); err != nil {
		t.Fatalf("seed global index: %v", err)
	}

	before, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("load delegator: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStakeClaimRewards,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(delegatorKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply stake claim rewards tx: %v", err)
	}

	after, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("reload delegator: %v", err)
	}
	if after.BalanceZNHB.Cmp(before.BalanceZNHB) <= 0 {
		t.Fatalf("expected rewards minted, before=%s after=%s", before.BalanceZNHB, after.BalanceZNHB)
	}
	if after.Nonce != 1 {
		t.Fatalf("expected sender nonce to advance to 1, got %d", after.Nonce)
	}

	found := false
	claimantAddr := delegatorKey.PubKey().Address().String()
	for _, evt := range sp.Events() {
		if evt.Type == events.TypeStakeRewardsClaimed {
			found = true
			if evt.Attributes["addr"] != claimantAddr {
				t.Fatalf("unexpected claimant in event: got %s want %s", evt.Attributes["addr"], claimantAddr)
			}
		}
	}
	if !found {
		t.Fatalf("expected a StakeRewardsClaimed event")
	}

	// Immediately replaying at the next nonce, with no elapsed payout
	// period, must be rejected as not-due -- matching sp.StakeClaimRewards'
	// own behavior, now reachable only through a real signed transaction.
	tx2 := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStakeClaimRewards,
		Nonce:    1,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx2.Sign(delegatorKey.PrivateKey); err != nil {
		t.Fatalf("sign second tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx2); err == nil {
		t.Fatalf("expected immediate re-claim to be rejected as not due")
	}

	// The stale (already-consumed) nonce must now be rejected by the
	// standard account-nonce check -- there is no separate bespoke replay
	// guard left to duplicate/stale-check, same as the other stake tx types.
	tx3 := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStakeClaimRewards,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx3.Sign(delegatorKey.PrivateKey); err != nil {
		t.Fatalf("sign replay tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx3); err == nil {
		t.Fatalf("expected stale nonce rejection on replay")
	}
}

// TestApplyStakeClaimRewards_PausedModuleRejected confirms the module pause
// guard inside sp.StakeClaimRewards is honored on the transaction-dispatch
// path, and that no nonce is consumed for a rejected (paused) attempt.
func TestApplyStakeClaimRewards_PausedModuleRejected(t *testing.T) {
	sp := newStakingStateProcessor(t)

	start := time.Unix(1_750_100_000, 0).UTC()
	sp.nowFunc = func() time.Time { return start }
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	delegatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var delegator [20]byte
	copy(delegator[:], delegatorKey.PubKey().Address().Bytes())

	// sp.PutAccount (not the writeAccount test helper) is required here: it
	// is the real setAccount path that actually persists StakeShares/
	// StakeLastIndex/StakeLastPayoutTs into account metadata --
	// writeAccount's lower-level accountMetadata literal omits those three
	// fields entirely, matching Test_StakeEvents_EmitOnTransitions'
	// PutAccount usage for the same reason.
	lastPayout := start.Add(-2 * time.Duration(stakePayoutPeriodSeconds) * time.Second)
	if err := sp.PutAccount(delegator[:], &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       rewards.IndexUnit(),
		StakeLastIndex:    big.NewInt(0),
		StakeLastPayoutTs: uint64(lastPayout.Unix()),
	}); err != nil {
		t.Fatalf("seed delegator account: %v", err)
	}

	sp.SetPauseView(staticPauseView{moduleStaking: true})

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStakeClaimRewards,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(delegatorKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected paused staking module to reject the claim")
	}

	after, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("reload delegator: %v", err)
	}
	if after.Nonce != 0 {
		t.Fatalf("expected nonce untouched on rejected claim, got %d", after.Nonce)
	}
}
