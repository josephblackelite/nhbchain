package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// potsoStakeView reads back the owner's whole staking position straight from
// state: the ordered lock nonce index, the bonded total, the sum of every
// listed lock still bonded (UnbondAt == 0), the sum of every listed lock
// unbonding, and the owner and vault ZNHB balances.
type potsoStakeView struct {
	nonces    []uint64
	bonded    *big.Int
	listedBnd *big.Int
	listedUnb *big.Int
	owner     *big.Int
	vault     *big.Int
}

func viewPotsoStake(t *testing.T, node *Node, owner [20]byte) potsoStakeView {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	v := potsoStakeView{listedBnd: big.NewInt(0), listedUnb: big.NewInt(0)}
	var err error
	if v.nonces, err = manager.PotsoStakeLockNonces(owner); err != nil {
		t.Fatalf("lock nonces: %v", err)
	}
	if v.bonded, err = manager.PotsoStakeBondedTotal(owner); err != nil {
		t.Fatalf("bonded total: %v", err)
	}
	for _, nonce := range v.nonces {
		lock, ok, err := manager.PotsoStakeGetLock(owner, nonce)
		if err != nil {
			t.Fatalf("get lock: %v", err)
		}
		if !ok {
			continue
		}
		if lock.UnbondAt == 0 {
			v.listedBnd.Add(v.listedBnd, lock.Amount)
		} else {
			v.listedUnb.Add(v.listedUnb, lock.Amount)
		}
	}
	acc, err := manager.GetAccount(owner[:])
	if err != nil {
		t.Fatalf("owner account: %v", err)
	}
	v.owner = new(big.Int).Set(acc.BalanceZNHB)
	vaultAddr := manager.PotsoStakeVaultAddress()
	vaultAcc, err := manager.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("vault account: %v", err)
	}
	v.vault = new(big.Int).Set(vaultAcc.BalanceZNHB)
	return v
}

// requirePotsoStakeConsistent checks the invariants a correct staking ledger
// keeps after every transaction: no lock holding stake is missing from the
// index (the bonded total equals the listed bonded locks), the vault holds
// exactly what the listed locks account for, and the owner balance plus the
// vault balance equals the amount the owner started with.
func requirePotsoStakeConsistent(t *testing.T, node *Node, owner [20]byte, total int64, step string) potsoStakeView {
	t.Helper()
	v := viewPotsoStake(t, node, owner)
	if v.bonded.Cmp(v.listedBnd) != 0 {
		t.Fatalf("%s: bonded total %s but listed bonded locks sum to %s (nonces %v): stake is orphaned", step, v.bonded, v.listedBnd, v.nonces)
	}
	wantVault := new(big.Int).Add(v.listedBnd, v.listedUnb)
	if v.vault.Cmp(wantVault) != 0 {
		t.Fatalf("%s: vault holds %s but listed locks account for %s (nonces %v)", step, v.vault, wantVault, v.nonces)
	}
	sum := new(big.Int).Add(v.owner, v.vault)
	if sum.Cmp(big.NewInt(total)) != 0 {
		t.Fatalf("%s: owner+vault = %s, want %d (supply not conserved)", step, sum, total)
	}
	return v
}

// matureUnbondingLocks moves every unbonding lock's WithdrawAt into the past
// so a withdraw can pay it, without sleeping through the cooldown.
func matureUnbondingLocks(t *testing.T, node *Node, owner [20]byte) {
	t.Helper()
	past := uint64(time.Now().Add(-time.Hour).Unix())
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		t.Fatalf("lock nonces: %v", err)
	}
	for _, nonce := range nonces {
		lock, ok, err := manager.PotsoStakeGetLock(owner, nonce)
		if err != nil || !ok {
			t.Fatalf("get lock %d: ok=%v err=%v", nonce, ok, err)
		}
		if lock.UnbondAt == 0 {
			continue
		}
		lock.WithdrawAt = past
		if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
			t.Fatalf("update lock: %v", err)
		}
	}
}

func newMultiLockStaker(t *testing.T, locks int, each int64) (*Node, *crypto.PrivateKey, [20]byte, uint64) {
	t.Helper()
	node := newTestNode(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(key)
	total := int64(locks) * each
	fundAccount(t, node, owner, big.NewInt(total))
	var nonce uint64
	for i := 0; i < locks; i++ {
		if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeLock, big.NewInt(each)); err != nil {
			t.Fatalf("lock %d: %v", i, err)
		}
		nonce++
	}
	requirePotsoStakeConsistent(t, node, owner, total, "after locks")
	return node, key, owner, nonce
}

// An unbond satisfied by the first lock must leave every later lock in the
// owner's nonce index; before the fix it persisted only the visited prefix
// and the later locks' stake could never be unbonded or withdrawn again.
func TestPotsoStakeUnbondKeepsLaterLocks(t *testing.T) {
	node, key, owner, nonce := newMultiLockStaker(t, 3, 100)

	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeUnbond, big.NewInt(100)); err != nil {
		t.Fatalf("unbond 100: %v", err)
	}
	nonce++
	v := requirePotsoStakeConsistent(t, node, owner, 300, "after unbond 100")
	if len(v.nonces) != 3 {
		t.Fatalf("expected all 3 lock nonces kept, got %v", v.nonces)
	}
	if v.bonded.Cmp(big.NewInt(200)) != 0 || v.listedUnb.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected bonded 200 / unbonding 100, got %s / %s", v.bonded, v.listedUnb)
	}

	// The remaining 200 must still be reachable.
	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeUnbond, big.NewInt(200)); err != nil {
		t.Fatalf("unbond remaining 200: %v", err)
	}
	nonce++
	v = requirePotsoStakeConsistent(t, node, owner, 300, "after unbond remaining")
	if v.bonded.Sign() != 0 || v.listedUnb.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("expected everything unbonding, got bonded %s unbonding %s", v.bonded, v.listedUnb)
	}

	matureUnbondingLocks(t, node, owner)
	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	v = requirePotsoStakeConsistent(t, node, owner, 300, "after withdraw")
	if v.owner.Cmp(big.NewInt(300)) != 0 || v.vault.Sign() != 0 || len(v.nonces) != 0 {
		t.Fatalf("expected the owner to recover all 300, got owner %s vault %s nonces %v", v.owner, v.vault, v.nonces)
	}
}

// Partial unbonds that split a lock in the middle of the list, repeated,
// then withdrawn in two rounds: every unit of stake stays reachable.
func TestPotsoStakeMultiLockPartialUnbondAndWithdraw(t *testing.T) {
	node, key, owner, nonce := newMultiLockStaker(t, 4, 100)

	step := func(amount int64, label string) potsoStakeView {
		t.Helper()
		if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeUnbond, big.NewInt(amount)); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		nonce++
		return requirePotsoStakeConsistent(t, node, owner, 400, label)
	}

	// 150 = all of lock 1 plus half of lock 2, splitting lock 2 in place;
	// locks 3 and 4 are untouched and must remain listed.
	v := step(150, "unbond 150")
	if v.bonded.Cmp(big.NewInt(250)) != 0 || v.listedUnb.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("after 150: bonded %s unbonding %s", v.bonded, v.listedUnb)
	}
	// 20 more comes out of the residual half-lock, again leaving 3 and 4.
	v = step(20, "unbond 20")
	if v.bonded.Cmp(big.NewInt(230)) != 0 || v.listedUnb.Cmp(big.NewInt(170)) != 0 {
		t.Fatalf("after 20: bonded %s unbonding %s", v.bonded, v.listedUnb)
	}

	// Withdraw what matured; the still-bonded locks must survive the pass.
	matureUnbondingLocks(t, node, owner)
	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("first withdraw: %v", err)
	}
	nonce++
	v = requirePotsoStakeConsistent(t, node, owner, 400, "after first withdraw")
	if v.owner.Cmp(big.NewInt(170)) != 0 || v.bonded.Cmp(big.NewInt(230)) != 0 {
		t.Fatalf("after first withdraw: owner %s bonded %s", v.owner, v.bonded)
	}

	// The rest is still fully reachable.
	step(230, "unbond remaining 230")
	matureUnbondingLocks(t, node, owner)
	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("second withdraw: %v", err)
	}
	v = requirePotsoStakeConsistent(t, node, owner, 400, "after second withdraw")
	if v.owner.Cmp(big.NewInt(400)) != 0 || v.vault.Sign() != 0 || len(v.nonces) != 0 {
		t.Fatalf("expected the owner to recover all 400, got owner %s vault %s nonces %v", v.owner, v.vault, v.nonces)
	}
}

// A withdraw refused because nothing has matured must leave every lock,
// unbonding or still bonded, in the index.
func TestPotsoStakeRefusedWithdrawKeepsAllLocks(t *testing.T) {
	node, key, owner, nonce := newMultiLockStaker(t, 3, 100)

	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeUnbond, big.NewInt(100)); err != nil {
		t.Fatalf("unbond: %v", err)
	}
	nonce++
	if err := submitPotsoStakeTx(t, node, key, nonce, types.TxTypePotsoStakeWithdraw, nil); err == nil {
		t.Fatalf("expected withdraw to fail before the cooldown")
	}
	v := requirePotsoStakeConsistent(t, node, owner, 300, "after refused withdraw")
	if len(v.nonces) != 3 {
		t.Fatalf("expected 3 lock nonces after refused withdraw, got %v", v.nonces)
	}
}
