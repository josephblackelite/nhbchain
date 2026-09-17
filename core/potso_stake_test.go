package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

func fundAccount(t *testing.T, node *Node, addr [20]byte, amount *big.Int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	account, err := manager.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	account.BalanceZNHB = new(big.Int).Set(amount)
	if err := manager.PutAccount(addr[:], account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}

// submitPotsoStakeTx builds, signs (with key, at nonce), and applies a
// TxTypePotsoStakeLock/Unbond/Withdraw transaction directly against node's
// StateProcessor -- the owner is always the signer, never a payload field
// (see core/potso_stake_tx.go), and replay protection is the standard
// account nonce (checked generically by sp.validateSenderAccount before
// dispatch), replacing the old bespoke authNonce parameter these tests used
// to pass to Node.PotsoStakeLock/Unbond/Withdraw directly.
func submitPotsoStakeTx(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64, txType types.TxType, amount *big.Int) error {
	t.Helper()
	var data []byte
	if txType != types.TxTypePotsoStakeWithdraw {
		encoded, err := rlp.EncodeToBytes(struct{ Amount *big.Int }{Amount: amount})
		if err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		data = encoded
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return node.state.ApplyTransaction(tx)
}

func TestPotsoStakeLifecycle(t *testing.T) {
	node := newTestNode(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(ownerKey)
	fundAccount(t, node, owner, big.NewInt(1000))

	if err := submitPotsoStakeTx(t, node, ownerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(600)); err != nil {
		t.Fatalf("stake lock: %v", err)
	}
	// Same nonce again must be rejected by the standard account-nonce check
	// -- there is no separate bespoke nonce left to duplicate/stale-check.
	if err := submitPotsoStakeTx(t, node, ownerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(1)); err == nil {
		t.Fatalf("expected stale nonce rejection")
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 1, types.TxTypePotsoStakeLock, big.NewInt(400)); err != nil {
		t.Fatalf("second stake lock: %v", err)
	}

	info, err := node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info: %v", err)
	}
	if info.Bonded.String() != "1000" {
		t.Fatalf("expected bonded 1000, got %s", info.Bonded.String())
	}
	if len(info.Locks) != 2 {
		t.Fatalf("expected two locks, got %d", len(info.Locks))
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 2, types.TxTypePotsoStakeUnbond, big.NewInt(700)); err != nil {
		t.Fatalf("stake unbond: %v", err)
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 2, types.TxTypePotsoStakeUnbond, big.NewInt(1)); err == nil {
		t.Fatalf("expected stale nonce rejection for unbond")
	}
	info, err = node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after unbond: %v", err)
	}
	if info.Bonded.String() != "300" {
		t.Fatalf("expected bonded 300, got %s", info.Bonded.String())
	}
	if info.PendingUnbond.String() != "700" {
		t.Fatalf("expected pending 700, got %s", info.PendingUnbond.String())
	}
	if info.Withdrawable.Sign() != 0 {
		t.Fatalf("expected zero withdrawable, got %s", info.Withdrawable.String())
	}

	// Find the withdrawAt timestamp the unbond just scheduled, to relocate
	// the queue entry into the past below (this test doesn't want to sleep
	// 7 real days).
	var withdrawAt uint64
	for _, lock := range info.Locks {
		if lock.UnbondAt != 0 && lock.WithdrawAt > withdrawAt {
			withdrawAt = lock.WithdrawAt
		}
	}
	if withdrawAt == 0 {
		t.Fatalf("expected an unbonding lock with a scheduled withdrawAt")
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err == nil {
		t.Fatalf("expected withdraw to fail before cooldown")
	}

	past := uint64(time.Now().Add(-time.Hour).Unix())
	originalDay := potso.WithdrawDay(withdrawAt)
	newDay := potso.WithdrawDay(past)

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	entries, err := manager.PotsoStakeQueueEntries(originalDay)
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("queue entries: %v", err)
	}
	if err := manager.PotsoStakePutQueueEntries(originalDay, nil); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("clear original queue: %v", err)
	}
	for _, entry := range entries {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, entry.Nonce)
		if getErr != nil {
			node.stateMu.Unlock()
			t.Fatalf("load lock: %v", getErr)
		}
		if !ok {
			continue
		}
		lock.WithdrawAt = past
		if err := manager.PotsoStakePutLock(owner, entry.Nonce, lock); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("update lock: %v", err)
		}
		entry.Amount = new(big.Int).Set(lock.Amount)
		if err := manager.PotsoStakeQueueAppend(newDay, entry); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("requeue entry: %v", err)
		}
	}
	node.stateMu.Unlock()

	// submitPotsoStakeTx at nonce 3 above failed validation before the tx
	// ever incremented the account nonce, so 3 is still the next valid one.
	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("withdraw matured: %v", err)
	}

	infoAfterWithdraw, err := node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after withdraw: %v", err)
	}
	if infoAfterWithdraw.Withdrawable.Sign() != 0 {
		t.Fatalf("expected no withdrawable after payout, got %s", infoAfterWithdraw.Withdrawable.String())
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err == nil {
		t.Fatalf("expected stale nonce rejection for withdraw")
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 4, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("idempotent withdraw failed: %v", err)
	}

	events := node.Events()
	if len(events) < 3 {
		t.Fatalf("expected stake events to be recorded")
	}
}

// TestPotsoStakeWithdrawCannotDrainVaultByReplaying is a regression test for
// a real, externally-reported vulnerability: PotsoStakeDeleteLock and
// PotsoStakePutLockNonces's empty-list "clear" branch both write directly
// via m.trie.Update(key, nil) using a key that potsoStakeLockKey/
// potsoStakeLockIndexKey ALREADY ran through kvKey() (keccak256) once --
// bypassing the second kvKey() wrap that KVPut/KVGet apply on top of
// whatever key they're given. A withdrawal that empties a staker's entire
// lock list (the common case: unbond everything, wait out the cooldown,
// withdraw once) therefore neither actually deletes the underlying lock
// record NOR actually clears the nonce index -- both silently write to an
// unrelated, never-populated trie key instead. The next PotsoStakeLockNonces
// read comes back with the SAME stale, non-empty list, pointing at a lock
// record that PotsoStakeGetLock still finds fully intact and still
// "matured" -- so a second, otherwise-ordinary withdraw transaction
// re-discovers and re-pays the exact same already-withdrawn lock a second
// time, out of the SHARED staking vault (i.e. at every other staker's
// expense, not just replaying against the withdrawer's own already-spent
// balance). TestPotsoStakeLifecycle's own "idempotent withdraw" step
// (nonce 4 above) only asserted the second call doesn't ERROR -- it never
// checked the resulting balances, which is exactly how this stayed hidden:
// the second call doesn't error, it succeeds AND pays out again.
func TestPotsoStakeWithdrawCannotDrainVaultByReplaying(t *testing.T) {
	node := newTestNode(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(ownerKey)
	fundAccount(t, node, owner, big.NewInt(1_000))

	if err := submitPotsoStakeTx(t, node, ownerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(1_000)); err != nil {
		t.Fatalf("stake lock: %v", err)
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 1, types.TxTypePotsoStakeUnbond, big.NewInt(1_000)); err != nil {
		t.Fatalf("stake unbond: %v", err)
	}

	info, err := node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after unbond: %v", err)
	}
	var withdrawAt uint64
	for _, lock := range info.Locks {
		if lock.UnbondAt != 0 && lock.WithdrawAt > withdrawAt {
			withdrawAt = lock.WithdrawAt
		}
	}
	if withdrawAt == 0 {
		t.Fatalf("expected an unbonding lock with a scheduled withdrawAt")
	}

	// Relocate the matured lock into the past, exactly like
	// TestPotsoStakeLifecycle -- this test isn't exercising the cooldown
	// period itself, just what happens once a lock is genuinely withdrawable.
	past := uint64(time.Now().Add(-time.Hour).Unix())
	originalDay := potso.WithdrawDay(withdrawAt)
	newDay := potso.WithdrawDay(past)
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	entries, err := manager.PotsoStakeQueueEntries(originalDay)
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("queue entries: %v", err)
	}
	if err := manager.PotsoStakePutQueueEntries(originalDay, nil); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("clear original queue: %v", err)
	}
	for _, entry := range entries {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, entry.Nonce)
		if getErr != nil {
			node.stateMu.Unlock()
			t.Fatalf("load lock: %v", getErr)
		}
		if !ok {
			continue
		}
		lock.WithdrawAt = past
		if err := manager.PotsoStakePutLock(owner, entry.Nonce, lock); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("update lock: %v", err)
		}
		entry.Amount = new(big.Int).Set(lock.Amount)
		if err := manager.PotsoStakeQueueAppend(newDay, entry); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("requeue entry: %v", err)
		}
	}
	node.stateMu.Unlock()

	balanceOf := func(addr [20]byte) *big.Int {
		t.Helper()
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		acc, err := nhbstate.NewManager(node.state.Trie).GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load balance: %v", err)
		}
		return new(big.Int).Set(acc.BalanceZNHB)
	}
	vaultAddr := func() [20]byte {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		return nhbstate.NewManager(node.state.Trie).PotsoStakeVaultAddress()
	}()

	// An unrelated second staker, never touched again after this -- purely
	// so the SHARED vault genuinely holds another real staker's funds. This
	// is what turns "the replayed withdraw recomputes a payout it shouldn't"
	// into an actual drain of money that belongs to someone else, rather
	// than merely failing on insufficient balance in an otherwise-empty
	// vault (which is a real but much less alarming failure mode).
	otherStakerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate other staker key: %v", err)
	}
	otherStaker := toAddress(otherStakerKey)
	fundAccount(t, node, otherStaker, big.NewInt(5_000))
	if err := submitPotsoStakeTx(t, node, otherStakerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(5_000)); err != nil {
		t.Fatalf("other staker's stake lock: %v", err)
	}

	ownerBalanceBefore := balanceOf(owner)

	if err := submitPotsoStakeTx(t, node, ownerKey, 2, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("first withdraw: %v", err)
	}
	ownerBalanceAfterFirst := balanceOf(owner)
	vaultBalanceAfterFirst := balanceOf(vaultAddr)
	firstPayout := new(big.Int).Sub(ownerBalanceAfterFirst, ownerBalanceBefore)
	if firstPayout.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("test fixture sanity: expected the first withdraw to pay out exactly 1000, got %s", firstPayout)
	}

	// A completely ordinary second withdraw transaction -- same owner, next
	// valid nonce, nothing malformed about it at all. If the lock/index were
	// genuinely cleared by the first withdraw, this has nothing left to pay
	// and must be a no-op (matching TestPotsoStakeLifecycle's own
	// "idempotent withdraw" expectation).
	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("second withdraw: %v", err)
	}
	ownerBalanceAfterSecond := balanceOf(owner)
	vaultBalanceAfterSecond := balanceOf(vaultAddr)

	if ownerBalanceAfterSecond.Cmp(ownerBalanceAfterFirst) != 0 {
		t.Fatalf("VAULT DRAIN: a second, ordinary withdraw transaction paid out again -- owner balance went %s -> %s (should be unchanged, nothing left to withdraw)", ownerBalanceAfterFirst, ownerBalanceAfterSecond)
	}
	if vaultBalanceAfterSecond.Cmp(vaultBalanceAfterFirst) != 0 {
		t.Fatalf("VAULT DRAIN: the shared staking vault paid out a second time for the same already-withdrawn lock -- vault balance went %s -> %s", vaultBalanceAfterFirst, vaultBalanceAfterSecond)
	}
}
