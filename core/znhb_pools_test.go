package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newZNHBPoolsStateProcessor(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func TestEnsureZNHBPoolsBootstrapped_SplitsCorrectly(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	if salePool.Cmp(znhbExpectedSalePoolWei) != 0 {
		t.Fatalf("sale pool = %s, want %s", salePool, znhbExpectedSalePoolWei)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Cmp(znhbExpectedRewardPoolWei) != 0 {
		t.Fatalf("reward pool = %s, want %s", rewardPool, znhbExpectedRewardPoolWei)
	}
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative sold: %v", err)
	}
	if cumulative.Sign() != 0 {
		t.Fatalf("cumulative sold = %s, want 0 immediately after bootstrap", cumulative)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant should hold immediately after bootstrap: %v", err)
	}
}

func TestEnsureZNHBPoolsBootstrapped_IsIdempotent(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	// Simulate a purchase having advanced the counter and drawn down the
	// sale pool, so a second bootstrap call would be destructive if it
	// weren't idempotent.
	if err := manager.ZNHBSetCumulativeSaleDistributed(big.NewInt(12345)); err != nil {
		t.Fatalf("simulate sale progress: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("second bootstrap call should be a no-op, got error: %v", err)
	}

	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative sold: %v", err)
	}
	if cumulative.Cmp(big.NewInt(12345)) != 0 {
		t.Fatalf("cumulative sold = %s, want 12345 (bootstrap must not re-run and reset progress)", cumulative)
	}
}

func TestEnsureZNHBPoolsBootstrapped_SplitsWhateverLiveBalanceIs(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	// A balance that has drifted from any frozen genesis snapshot -- e.g.
	// real buyZNHB purchases moved it before the node's first restart
	// under bootstrap-aware code. Bootstrap must split whatever this
	// actually is, not refuse to start the node.
	drift, ok := new(big.Int).SetString("21171800000000000000000", 10)
	if !ok {
		t.Fatalf("parse drift constant")
	}
	liveBalance := new(big.Int).Sub(znhbExpectedTotalSupplyWei, drift)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(liveBalance),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap should succeed against a drifted live balance: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	wantReward := znhbPoolRewardShare(liveBalance)
	wantSale := new(big.Int).Sub(liveBalance, wantReward)

	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Cmp(wantReward) != 0 {
		t.Fatalf("reward pool = %s, want %s (20%% of live balance)", rewardPool, wantReward)
	}
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	if salePool.Cmp(wantSale) != 0 {
		t.Fatalf("sale pool = %s, want %s (remainder of live balance)", salePool, wantSale)
	}
	if new(big.Int).Add(salePool, rewardPool).Cmp(liveBalance) != 0 {
		t.Fatalf("sale pool + reward pool must sum to exactly the live balance")
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant should hold immediately after bootstrap: %v", err)
	}
}

func TestEnsureZNHBPoolsBootstrapped_HardFailsOnZeroBalance(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err == nil {
		t.Fatalf("expected a hard failure for a zero admin wallet balance, got nil error")
	}

	manager := nhbstate.NewManager(sp.Trie)
	bootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		t.Fatalf("read bootstrap flag: %v", err)
	}
	if bootstrapped {
		t.Fatalf("bootstrap flag must not be set after a failed bootstrap attempt")
	}
}

func TestEnsureZNHBPoolsBootstrapped_NoAdminWalletIsNoop(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	// hasAdminWallet defaults to false -- neither function should error.
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap without an admin wallet should be a silent no-op, got: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant check without an admin wallet should be a silent no-op, got: %v", err)
	}
}

func TestCheckZNHBSupplyInvariant_DetectsViolation(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Simulate a consensus bug: silently inflate the sale pool sub-ledger
	// without a corresponding admin-wallet balance change.
	manager := nhbstate.NewManager(sp.Trie)
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	corrupted := new(big.Int).Add(salePool, big.NewInt(1))
	if err := manager.ZNHBSetSalePoolBalance(corrupted); err != nil {
		t.Fatalf("corrupt sale pool: %v", err)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err == nil {
		t.Fatalf("expected the supply invariant check to catch a desynced pool balance")
	}
}

// TestCheckZNHBSupplyInvariant_HoldsThroughSelfStakeAndUnstake documents the
// fix for the 2026-08-26 incident: the admin wallet self-staking used to
// move ZNHB from BalanceZNHB into Stake/LockedZNHB, which the old
// balance-only formula had no idea about, breaking the invariant the
// instant it staked anything. The corrected formula must hold at every
// step of self-stake -> self-unstake-to-pending, since all of it stays on
// the admin's own account in fields the formula now sums.
func TestCheckZNHBSupplyInvariant_HoldsThroughSelfStakeAndUnstake(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold after bootstrap: %v", err)
	}

	stakeAmount := big.NewInt(10_000)
	if _, err := sp.StakeDelegate(adminAddr[:], nil, stakeAmount); err != nil {
		t.Fatalf("admin self-stake: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold immediately after admin self-stake: %v", err)
	}

	if _, err := sp.StakeUndelegate(adminAddr[:], stakeAmount); err != nil {
		t.Fatalf("admin self-unstake: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold with the amount sitting in a pending unbond: %v", err)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if len(admin.PendingUnbonds) != 1 || admin.PendingUnbonds[0].Amount.Cmp(stakeAmount) != 0 {
		t.Fatalf("expected exactly one pending unbond of %s, got %+v", stakeAmount, admin.PendingUnbonds)
	}
}

// TestCheckZNHBSupplyInvariant_IgnoresThirdPartyDelegationIn documents why
// Account.Stake is deliberately excluded from the invariant formula: any
// third party can name the admin wallet as their validator with no
// eligibility check, crediting the admin's own Stake field with money that
// was never the treasury's. The invariant must not be affected by this at
// all -- it should hold both before and after such a delegation lands.
func TestCheckZNHBSupplyInvariant_IgnoresThirdPartyDelegationIn(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	thirdParty := [20]byte{0xB0}
	if err := sp.setAccount(thirdParty[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(5_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed third party: %v", err)
	}

	if _, err := sp.StakeDelegate(thirdParty[:], adminAddr[:], big.NewInt(5_000)); err != nil {
		t.Fatalf("third party delegates to admin wallet as validator: %v", err)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if admin.Stake.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("expected third-party delegated stake to land on admin.Stake, got %s", admin.Stake)
	}
	if admin.BalanceZNHB.Cmp(znhbExpectedTotalSupplyWei) != 0 {
		t.Fatalf("admin's own BalanceZNHB must be untouched by a delegation it never sent, got %s", admin.BalanceZNHB)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant must ignore third-party delegated-in stake, got: %v", err)
	}
}

// TestClearAdminStalePendingUnbondsOnce_RemovesExactMatchAndIsIdempotent
// covers the 2026-08-26 incident's specific leftover: a pending unbond
// already effectively paid out via ReconcileZNHBSupplyDriftOnce's blunt
// balance top-up, which must be spliced out without a second credit.
func TestClearAdminStalePendingUnbondsOnce_RemovesExactMatchAndIsIdempotent(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)

	staleAmount := new(big.Int).Set(staleAdminPendingUnbondSeed[0].Amount)
	balanceBefore := new(big.Int).Set(znhbExpectedTotalSupplyWei)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(balanceBefore),
		Stake:       big.NewInt(0),
		PendingUnbonds: []types.StakeUnbond{
			{ID: staleAdminPendingUnbondSeed[0].ID, Validator: append([]byte(nil), adminAddr[:]...), Amount: staleAmount, ReleaseTime: 1},
		},
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.ClearAdminStalePendingUnbondsOnce(); err != nil {
		t.Fatalf("clear stale pending unbonds: %v", err)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if len(admin.PendingUnbonds) != 0 {
		t.Fatalf("expected the stale pending unbond to be removed, got %+v", admin.PendingUnbonds)
	}
	if admin.BalanceZNHB.Cmp(balanceBefore) != 0 {
		t.Fatalf("clearing a stale pending unbond must not credit BalanceZNHB again: before=%s after=%s", balanceBefore, admin.BalanceZNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	cleared, err := manager.ZNHBAdminStaleUnbondsCleared()
	if err != nil {
		t.Fatalf("read cleared flag: %v", err)
	}
	if !cleared {
		t.Fatalf("expected the cleared flag to be set")
	}

	// Idempotency: seed a brand new pending unbond with the same ID and
	// amount (simulating a legitimate new stake/unstake cycle after this
	// deploys) and confirm a second call never touches it.
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(balanceBefore),
		Stake:       big.NewInt(0),
		PendingUnbonds: []types.StakeUnbond{
			{ID: staleAdminPendingUnbondSeed[0].ID, Validator: append([]byte(nil), adminAddr[:]...), Amount: new(big.Int).Set(staleAmount), ReleaseTime: 2},
		},
	}); err != nil {
		t.Fatalf("reseed admin wallet: %v", err)
	}
	if err := sp.ClearAdminStalePendingUnbondsOnce(); err != nil {
		t.Fatalf("second clear call: %v", err)
	}
	admin, err = sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account after second call: %v", err)
	}
	if len(admin.PendingUnbonds) != 1 {
		t.Fatalf("a legitimate new pending unbond created after the flag is set must never be touched, got %+v", admin.PendingUnbonds)
	}
}

// TestClearAdminStalePendingUnbondsOnce_SkipsMismatchedAmount confirms the
// migration never force-removes an entry whose amount doesn't exactly
// match what was confirmed live -- it must fail safe (skip) rather than
// guess, per the adversarial review this fix went through.
func TestClearAdminStalePendingUnbondsOnce_SkipsMismatchedAmount(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)

	wrongAmount := new(big.Int).Add(staleAdminPendingUnbondSeed[0].Amount, big.NewInt(1))
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
		PendingUnbonds: []types.StakeUnbond{
			{ID: staleAdminPendingUnbondSeed[0].ID, Validator: append([]byte(nil), adminAddr[:]...), Amount: wrongAmount, ReleaseTime: 1},
		},
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.ClearAdminStalePendingUnbondsOnce(); err != nil {
		t.Fatalf("clear stale pending unbonds: %v", err)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if len(admin.PendingUnbonds) != 1 {
		t.Fatalf("an amount mismatch must be left untouched, not removed, got %+v", admin.PendingUnbonds)
	}
}

// TestClearAdminStalePendingUnbondsOnce_NoOpIfAlreadyClaimed confirms the
// migration is a harmless no-op when the entry is simply absent (e.g. it
// was already claimed through the normal, correct path some other way).
func TestClearAdminStalePendingUnbondsOnce_NoOpIfAlreadyClaimed(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.ClearAdminStalePendingUnbondsOnce(); err != nil {
		t.Fatalf("clear stale pending unbonds on an account with none: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	cleared, err := manager.ZNHBAdminStaleUnbondsCleared()
	if err != nil {
		t.Fatalf("read cleared flag: %v", err)
	}
	if !cleared {
		t.Fatalf("expected the cleared flag to be set even when there was nothing to clear")
	}
}

// TestCheckZNHBSupplyInvariant_HoldsThroughGovernanceProposalDeposit
// documents a second gap found live in production on 2026-08-26, right
// after the self-stake/unstake fix above shipped: the admin wallet
// submitting a real governance proposal (policy.swapRiskParams, with a
// deposit) repeatedly failed block-building with "supply invariant
// violated" until adminZNHBOwned() also summed the admin wallet's
// GovernanceEscrowLock balance -- SubmitProposal debits BalanceZNHB and
// escrows the deposit in a completely separate KV ledger, invisible to the
// old two-field-plus-pending-unbonds formula. Drives the real
// TxTypeGovPropose transaction path (sp.ApplyTransaction via
// applySignedGovTx), never a direct governance.Engine call, matching this
// file's sibling swap_risk_params_governance_test.go.
func TestCheckZNHBSupplyInvariant_HoldsThroughGovernanceProposalDeposit(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	adminAddrBytes := adminKey.PubKey().Address().Bytes()
	var adminAddr [20]byte
	copy(adminAddr[:], adminAddrBytes)
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddrBytes, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold after bootstrap: %v", err)
	}

	sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})

	deposit := weiAmount(5_000)
	payload := `{` +
		`"redeemPerTxMinWei":"0","redeemPerTxMaxWei":"` + weiStr(1_000) + `",` +
		`"redeemPerAddressDailyCapWei":"` + weiStr(2_000) + `","redeemPerAddressMonthlyCapWei":"` + weiStr(20_000) + `",` +
		`"memo":"test"` +
		`}`
	applySignedGovTx(t, sp, adminKey, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindSwapRiskParams,
		Payload: payload,
		Deposit: deposit,
	})

	manager := nhbstate.NewManager(sp.Trie)
	escrow, err := manager.GovernanceEscrowBalance(adminAddrBytes)
	if err != nil {
		t.Fatalf("read governance escrow balance: %v", err)
	}
	if escrow.Cmp(deposit) != 0 {
		t.Fatalf("expected the deposit to be escrowed, got escrow=%s want=%s", escrow, deposit)
	}

	admin, err := sp.getAccount(adminAddrBytes)
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	wantBalance := new(big.Int).Sub(znhbExpectedTotalSupplyWei, deposit)
	if admin.BalanceZNHB.Cmp(wantBalance) != 0 {
		t.Fatalf("expected BalanceZNHB debited by the deposit, got %s want %s", admin.BalanceZNHB, wantBalance)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold with the deposit escrowed, got: %v", err)
	}
}

// TestZNHBPoolBootstrap_PreCommitWriteDoesNotSurviveDriftReset documents the
// mechanism behind a real production incident: calling
// EnsureZNHBPoolsBootstrapped before any block has been committed leaves its
// writes only in the trie's pending (uncommitted) state. core/node.go's
// startup drift-reset safety net (ensurePendingStateMatchesCommittedHeadLocked)
// unconditionally resets pending state to the last committed root whenever
// they disagree -- which they always will immediately after a fresh
// bootstrap write, since nothing has committed it yet. The reset silently
// discarded the bootstrap with no error: the node came up looking healthy
// while the pools were never actually bootstrapped. See
// TestProcessBlockLifecycle_BootstrapsZNHBPoolsOnFirstBlock for the fix.
func TestZNHBPoolBootstrap_PreCommitWriteDoesNotSurviveDriftReset(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	committedRoot, err := sp.Commit(0)
	if err != nil {
		t.Fatalf("commit seeded admin wallet: %v", err)
	}

	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Sign() == 0 {
		t.Fatalf("expected bootstrap to have written a nonzero reward pool before the reset")
	}

	// Simulate the startup drift-reset firing before this write was ever
	// folded into a committed block -- exactly what happened in production.
	if err := sp.ResetToRoot(committedRoot); err != nil {
		t.Fatalf("reset to committed root: %v", err)
	}

	manager = nhbstate.NewManager(sp.Trie)
	rewardPool, err = manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool after reset: %v", err)
	}
	if rewardPool.Sign() != 0 {
		t.Fatalf("reward pool = %s, want 0 -- a pre-commit bootstrap write must not survive a drift reset", rewardPool)
	}
	bootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		t.Fatalf("read bootstrap flag after reset: %v", err)
	}
	if bootstrapped {
		t.Fatalf("bootstrap flag must also be reset, so a later real bootstrap attempt is not blocked as a false idempotent no-op")
	}
}

// TestProcessBlockLifecycle_BootstrapsZNHBPoolsOnFirstBlock verifies the fix:
// EnsureZNHBPoolsBootstrapped runs as part of ProcessBlockLifecycle, so its
// writes are folded into a real committed block and survive exactly the
// kind of reset that silently discarded them when it ran at node startup.
func TestProcessBlockLifecycle_BootstrapsZNHBPoolsOnFirstBlock(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	if err := sp.ProcessBlockLifecycle(1, time.Now().UTC().Unix()); err != nil {
		t.Fatalf("process block lifecycle: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	bootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		t.Fatalf("read bootstrap flag: %v", err)
	}
	if !bootstrapped {
		t.Fatalf("expected ProcessBlockLifecycle to bootstrap the pools on the first block")
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	wantReward := znhbPoolRewardShare(znhbExpectedTotalSupplyWei)
	if rewardPool.Cmp(wantReward) != 0 {
		t.Fatalf("reward pool = %s, want %s", rewardPool, wantReward)
	}

	// Commit this block and reset to that committed root: unlike the
	// pre-commit case above, this write is now part of a real committed
	// block and must survive.
	committedRoot, err := sp.Commit(1)
	if err != nil {
		t.Fatalf("commit block 1: %v", err)
	}
	if err := sp.ResetToRoot(committedRoot); err != nil {
		t.Fatalf("reset to committed root: %v", err)
	}
	manager = nhbstate.NewManager(sp.Trie)
	bootstrapped, err = manager.ZNHBPoolsBootstrapped()
	if err != nil {
		t.Fatalf("read bootstrap flag after reset to committed root: %v", err)
	}
	if !bootstrapped {
		t.Fatalf("bootstrap must survive a reset to its own committed root")
	}
}

// TestSweepStaleRejectedGovernanceDepositsOnce_SweepsExactMatchAndIsIdempotent
// reproduces the 2026-09-05 finding directly: proposal 2 was rejected for
// lack of quorum and its 1000 ZNHB deposit was never refunded (only the
// Passed branch of Finalize does that) nor swept anywhere -- it just sat
// permanently locked in the submitter's own escrow ledger entry. This
// proves the one-time migration sweeps that exact, already-identified
// deposit to the admin wallet, credits the Reward Pool in lockstep (the
// same fix native/governance/engine.go's Finalize needed), and never
// touches it again on a second call.
func TestSweepStaleRejectedGovernanceDepositsOnce_SweepsExactMatchAndIsIdempotent(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	adminBalanceBefore := big.NewInt(5_000)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(adminBalanceBefore),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	rewardPoolBefore := big.NewInt(2_000)
	if err := manager.ZNHBSetRewardPoolBalance(new(big.Int).Set(rewardPoolBefore)); err != nil {
		t.Fatalf("seed reward pool: %v", err)
	}

	var proposer [20]byte
	proposer[19] = 0x42
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposer[:])
	deposit := new(big.Int).Set(staleRejectedGovernanceDepositSeed[0].ExpectedDeposit)
	proposalID := staleRejectedGovernanceDepositSeed[0].ProposalID
	if err := manager.GovernancePutProposal(&governance.Proposal{
		ID:        proposalID,
		Submitter: submitter,
		Status:    governance.ProposalStatusRejected,
		Deposit:   new(big.Int).Set(deposit),
	}); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	if _, err := manager.GovernanceEscrowLock(proposer[:], deposit); err != nil {
		t.Fatalf("seed escrow lock: %v", err)
	}

	if err := sp.SweepStaleRejectedGovernanceDepositsOnce(); err != nil {
		t.Fatalf("sweep stale rejected deposits: %v", err)
	}

	wantAdminBalance := new(big.Int).Add(adminBalanceBefore, deposit)
	wantRewardPool := new(big.Int).Add(rewardPoolBefore, deposit)

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if admin.BalanceZNHB.Cmp(wantAdminBalance) != 0 {
		t.Fatalf("expected admin wallet credited the swept deposit, got %s want %s", admin.BalanceZNHB, wantAdminBalance)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("read reward pool: %v", err)
	}
	if rewardPool.Cmp(wantRewardPool) != 0 {
		t.Fatalf("expected reward pool credited in lockstep, got %s want %s", rewardPool, wantRewardPool)
	}
	escrow, err := manager.GovernanceEscrowBalance(proposer[:])
	if err != nil {
		t.Fatalf("read escrow balance: %v", err)
	}
	if escrow.Sign() != 0 {
		t.Fatalf("expected the proposer's escrow cleared, got %s", escrow)
	}
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("reload proposal: ok=%v err=%v", ok, err)
	}
	if proposal.Deposit == nil || proposal.Deposit.Sign() != 0 {
		t.Fatalf("expected proposal.Deposit zeroed after sweep, got %v", proposal.Deposit)
	}

	swept, err := manager.GovStaleRejectedDepositsSwept()
	if err != nil {
		t.Fatalf("read swept flag: %v", err)
	}
	if !swept {
		t.Fatalf("expected the swept flag to be set")
	}

	// Idempotency: a second call must be a no-op even if this exact
	// proposal ID were somehow reused with a fresh matching deposit
	// (mirrors ClearAdminStalePendingUnbondsOnce's own idempotency check).
	if err := manager.GovernancePutProposal(&governance.Proposal{
		ID:        proposalID,
		Submitter: submitter,
		Status:    governance.ProposalStatusRejected,
		Deposit:   new(big.Int).Set(deposit),
	}); err != nil {
		t.Fatalf("reseed proposal: %v", err)
	}
	if err := sp.SweepStaleRejectedGovernanceDepositsOnce(); err != nil {
		t.Fatalf("second sweep call: %v", err)
	}
	admin, err = sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account after second call: %v", err)
	}
	if admin.BalanceZNHB.Cmp(wantAdminBalance) != 0 {
		t.Fatalf("a second call must never sweep again, got admin balance %s want %s", admin.BalanceZNHB, wantAdminBalance)
	}
}

// TestSweepStaleRejectedGovernanceDepositsOnce_SkipsMismatchedDeposit
// confirms the migration never sweeps an entry whose deposit doesn't
// exactly match what was confirmed live -- it must fail safe (skip)
// rather than guess, mirroring ClearAdminStalePendingUnbondsOnce's own
// adversarially-reviewed conservatism.
func TestSweepStaleRejectedGovernanceDepositsOnce_SkipsMismatchedDeposit(t *testing.T) {
	sp := newZNHBPoolsStateProcessor(t)
	adminAddr := [20]byte{0xAD}
	sp.SetAdminWallet(adminAddr, true)
	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(5_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var proposer [20]byte
	proposer[19] = 0x42
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposer[:])
	wrongDeposit := new(big.Int).Add(staleRejectedGovernanceDepositSeed[0].ExpectedDeposit, big.NewInt(1))
	proposalID := staleRejectedGovernanceDepositSeed[0].ProposalID
	if err := manager.GovernancePutProposal(&governance.Proposal{
		ID:        proposalID,
		Submitter: submitter,
		Status:    governance.ProposalStatusRejected,
		Deposit:   wrongDeposit,
	}); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	if err := sp.SweepStaleRejectedGovernanceDepositsOnce(); err != nil {
		t.Fatalf("sweep stale rejected deposits: %v", err)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if admin.BalanceZNHB.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("a deposit mismatch must be left untouched, got admin balance %s", admin.BalanceZNHB)
	}
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("reload proposal: ok=%v err=%v", ok, err)
	}
	if proposal.Deposit.Cmp(wrongDeposit) != 0 {
		t.Fatalf("a deposit mismatch must not be zeroed, got %s", proposal.Deposit)
	}
}
