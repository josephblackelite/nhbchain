package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// withRewardPoolAdminWallet bootstraps a real ZNHB admin wallet (and thus a
// real, live Sale/Reward Pool split) on top of newRewardTestState's already
// -configured reward schedule, so settleEpochRewards's Reward Pool
// debit/clamp logic actually has a pool to check against -- newRewardTestState
// alone deliberately has no admin wallet (see its own tests), matching every
// other pre-existing reward test in this package that doesn't care about the
// ZNHB Reward Pool at all.
func withRewardPoolAdminWallet(t *testing.T, sp *StateProcessor) [20]byte {
	t.Helper()
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap znhb pools: %v", err)
	}
	return adminAddr
}

func rewardPoolBalance(t *testing.T, sp *StateProcessor) *big.Int {
	t.Helper()
	balance, err := nhbstate.NewManager(sp.Trie).ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool balance: %v", err)
	}
	return balance
}

// drainRewardPool simulates prior epochs having already paid out most of
// the Reward Pool: it sets the pool's remaining balance to v AND reduces
// the admin wallet's own ZNHB balance by the same delta, so
// CheckZNHBSupplyInvariant (SalePoolBalance + RewardPoolBalance ==
// adminAccount.BalanceZNHB, enforced every block) stays satisfied -- a
// real payout debits the admin's conceptual pool exactly like this, it
// just also credits a validator account elsewhere.
func drainRewardPool(t *testing.T, sp *StateProcessor, adminAddr [20]byte, v *big.Int) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	before, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool balance: %v", err)
	}
	delta := new(big.Int).Sub(before, v)
	if delta.Sign() < 0 {
		t.Fatalf("drainRewardPool only supports lowering the balance, got before=%s target=%s", before, v)
	}
	if err := manager.ZNHBSetRewardPoolBalance(v); err != nil {
		t.Fatalf("set reward pool balance: %v", err)
	}
	adminAccount, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	adminAccount.BalanceZNHB = new(big.Int).Sub(adminAccount.BalanceZNHB, delta)
	if err := sp.setAccount(adminAddr[:], adminAccount); err != nil {
		t.Fatalf("persist admin account: %v", err)
	}
}

func TestSettleEpochRewards_DebitsRewardPoolByExactPaidTotal(t *testing.T) {
	sp := newRewardTestState(t)
	withRewardPoolAdminWallet(t, sp)
	before := rewardPoolBalance(t, sp)

	seedEligibleValidator(t, sp, 6000, 10)
	seedEligibleValidator(t, sp, 4000, 5)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.Sign() <= 0 {
		t.Fatalf("expected a positive paid total to exercise the debit path, got %s", settlement.PaidTotal)
	}

	after := rewardPoolBalance(t, sp)
	want := new(big.Int).Sub(before, settlement.PaidTotal)
	if after.Cmp(want) != 0 {
		t.Fatalf("reward pool balance = %s, want %s (before %s - paid %s)", after, want, before, settlement.PaidTotal)
	}
}

func TestSettleEpochRewards_NoAdminWalletLeavesRewardPoolUntouched(t *testing.T) {
	// newRewardTestState has no admin wallet configured -- this mirrors
	// every pre-existing reward test in this package and must keep working
	// unchanged: no Reward Pool exists to clamp or debit against.
	sp := newRewardTestState(t)
	seedEligibleValidator(t, sp, 6000, 10)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.String() != "100" {
		t.Fatalf("expected the full unclamped schedule to pay out, got %s", settlement.PaidTotal)
	}
}

func TestSettleEpochRewards_ClampsToRemainingRewardPoolBalance(t *testing.T) {
	sp := newRewardTestState(t)
	adminAddr := withRewardPoolAdminWallet(t, sp)

	// The test schedule plans to pay out 100 (raw units) this epoch --
	// starve the pool down to less than that so the clamp has to engage.
	drainRewardPool(t, sp, adminAddr, big.NewInt(30))

	seedEligibleValidator(t, sp, 6000, 10)
	seedEligibleValidator(t, sp, 4000, 5)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	// PlannedTotal reflects the clamped epoch budget (30), not the
	// schedule's raw, pool-blind request (100) -- otherwise UnusedTotal()
	// would misleadingly report 70 "unused" ZNHB that the pool never
	// actually had to give out.
	if settlement.PlannedTotal.String() != "30" {
		t.Fatalf("planned total should reflect the reward-pool-clamped budget: got %s, want 30", settlement.PlannedTotal)
	}
	if settlement.PaidTotal.Cmp(big.NewInt(30)) > 0 {
		t.Fatalf("paid total %s exceeds the 30-unit Reward Pool balance that was available", settlement.PaidTotal)
	}
	if settlement.PaidTotal.Sign() <= 0 {
		t.Fatalf("expected a positive (but clamped) paid total, got %s", settlement.PaidTotal)
	}

	after := rewardPoolBalance(t, sp)
	if after.Sign() < 0 {
		t.Fatalf("reward pool balance went negative: %s", after)
	}
	want := new(big.Int).Sub(big.NewInt(30), settlement.PaidTotal)
	if after.Cmp(want) != 0 {
		t.Fatalf("reward pool balance = %s, want %s", after, want)
	}
}

func TestSettleEpochRewards_ExhaustedRewardPoolPaysNothing(t *testing.T) {
	sp := newRewardTestState(t)
	adminAddr := withRewardPoolAdminWallet(t, sp)
	drainRewardPool(t, sp, adminAddr, big.NewInt(0))

	seedEligibleValidator(t, sp, 6000, 10)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.Sign() != 0 {
		t.Fatalf("expected zero paid total against an exhausted reward pool, got %s", settlement.PaidTotal)
	}
	if got := rewardPoolBalance(t, sp); got.Sign() != 0 {
		t.Fatalf("reward pool balance should remain exactly zero, got %s", got)
	}
}

// TestSettleEpochRewards_PreservesSupplyInvariant is a regression test for
// the production incident where a real reward payout (recipient credited,
// Reward Pool label debited) never debited the admin wallet's actual ZNHB
// balance -- a pure mint with no funding side, invisible to every
// pre-existing test above because none of them checked
// CheckZNHBSupplyInvariant after a settlement. It broke on the very first
// live payout and halted block production for ~22 hours. This must pass
// after every reward settlement, not just be checked incidentally.
func TestSettleEpochRewards_PreservesSupplyInvariant(t *testing.T) {
	sp := newRewardTestState(t)
	withRewardPoolAdminWallet(t, sp)

	seedEligibleValidator(t, sp, 6000, 10)
	seedEligibleValidator(t, sp, 4000, 5)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.Sign() <= 0 {
		t.Fatalf("expected a positive paid total to exercise the debit path, got %s", settlement.PaidTotal)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated after a reward settlement: %v", err)
	}
}

// TestSettleEpochRewards_RecipientIsAdminWalletPreservesInvariant covers the
// case where the admin/treasury wallet is itself among the reward
// recipients (e.g. a phantom self-stake) -- the debit and credit land on
// the same account, and must still net out to a balance that exactly
// matches what every other recipient actually received.
func TestSettleEpochRewards_RecipientIsAdminWalletPreservesInvariant(t *testing.T) {
	sp := newRewardTestState(t)
	adminAddr := withRewardPoolAdminWallet(t, sp)

	// Make the admin/treasury wallet itself an eligible validator (mirrors
	// the production "phantom self-stake" scenario) instead of using
	// seedEligibleValidator, which always mints a fresh unrelated key.
	if err := nhbstate.NewManager(sp.Trie).SetMinimumValidatorStake(big.NewInt(1000)); err != nil {
		t.Fatalf("set test minimum stake: %v", err)
	}
	adminAccount, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	adminAccount.Stake = big.NewInt(6000)
	adminAccount.EngagementScore = 10
	adminAccount.EngagementLastHeartbeat = uint64(rewardBlockTimestamp1)
	if err := sp.setAccount(adminAddr[:], adminAccount); err != nil {
		t.Fatalf("seed admin as validator: %v", err)
	}
	if sp.EligibleValidators == nil {
		sp.EligibleValidators = make(map[string]*big.Int)
	}
	sp.EligibleValidators[string(adminAddr[:])] = big.NewInt(6000)

	seedEligibleValidator(t, sp, 4000, 5)
	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.Sign() <= 0 {
		t.Fatalf("expected a positive paid total to exercise the debit path, got %s", settlement.PaidTotal)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated when the admin wallet is itself a reward recipient: %v", err)
	}
}

func TestReconcileZNHBSupplyDriftOnce_RepairsExistingDriftAndIsIdempotent(t *testing.T) {
	sp := newRewardTestState(t)
	adminAddr := withRewardPoolAdminWallet(t, sp)

	// Simulate the pre-fix bug's aftermath directly: credit a recipient and
	// debit the Reward Pool label, WITHOUT debiting the admin wallet -- the
	// exact drift CheckZNHBSupplyInvariant caught in production.
	manager := nhbstate.NewManager(sp.Trie)
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool balance: %v", err)
	}
	drift := big.NewInt(50)
	if err := manager.ZNHBSetRewardPoolBalance(new(big.Int).Sub(rewardPool, drift)); err != nil {
		t.Fatalf("set reward pool balance: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err == nil {
		t.Fatalf("expected the induced drift to violate the invariant before reconciliation")
	}

	if err := sp.ReconcileZNHBSupplyDriftOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant still violated after reconciliation: %v", err)
	}
	adminAccount, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	wantBalance := new(big.Int).Sub(znhbExpectedTotalSupplyWei, drift)
	if adminAccount.BalanceZNHB.Cmp(wantBalance) != 0 {
		t.Fatalf("admin balance after reconciliation = %s, want %s", adminAccount.BalanceZNHB, wantBalance)
	}

	// Idempotency: re-running must not touch state again, even if a fresh
	// (post-fix) violation appears afterward -- the flag guards against
	// ever masking a real future bug, not just against re-running cleanly.
	// Reads the CURRENT (post-reconciliation) pool balance rather than
	// reusing the pre-reconciliation `rewardPool` value -- that value is
	// stale (reconciliation only corrects the admin wallet, not the pool
	// fields, so the pool is already sitting at rewardPool-drift; reusing
	// it here would be a no-op re-write, not a fresh violation).
	manager2 := nhbstate.NewManager(sp.Trie)
	currentRewardPool, err := manager2.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read current reward pool balance: %v", err)
	}
	if err := manager2.ZNHBSetRewardPoolBalance(new(big.Int).Sub(currentRewardPool, drift)); err != nil {
		t.Fatalf("re-induce drift: %v", err)
	}
	if err := sp.ReconcileZNHBSupplyDriftOnce(); err != nil {
		t.Fatalf("reconcile (second call): %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err == nil {
		t.Fatalf("expected the reconciliation to be a no-op on its second call, leaving the new violation in place")
	}
}
