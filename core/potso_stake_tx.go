package core

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/potso"
)

// applyPotsoStakeLockTransaction/applyPotsoStakeUnbondTransaction/
// applyPotsoStakeWithdrawTransaction replace
// Node.PotsoStakeLock/Unbond/Withdraw (core/node.go, removed): those
// methods mutated n.state.Trie directly under n.stateMu.Lock(), outside
// CreateBlock/ApplyTransaction entirely -- never gossiped, never included
// in a block, applied independently (and non-deterministically re: timing,
// since they also stamped lock/unbond/withdraw timestamps from real
// wall-clock time.Now() rather than the block timestamp) by whichever
// single validator happened to receive the RPC call. The bookkeeping logic
// below is a direct adaptation of those methods' bodies -- the lifecycle
// itself (vault balance movement, lock splitting, day-bucketed unbond
// queue) is unchanged, only how it is invoked and where "now" comes from.
//
// The bespoke sha256/secp256k1 signature scheme those RPC methods verified
// owner identity with (rpc/potso_stake_handlers.go's decodeStakeSignature,
// removed) was, on its own terms, cryptographically sound -- it really did
// prove possession of the claimed owner's private key. But it was a
// deliberately duplicated, bespoke-domain digest and verification path
// sitting outside the standard transaction envelope, and it also carried
// its own separate authNonce for replay protection. Both are now redundant
// now that this is a real transaction: the standard envelope signature
// (tx.From(), verified once before dispatch) already proves owner
// identity, and the standard account nonce already provides replay
// protection -- there is no payload owner/nonce/signature field left to
// spoof or duplicate.
func (sp *StateProcessor) applyPotsoStakeLockTransaction(tx *types.Transaction, sender []byte) error {
	if err := nativecommon.Guard(sp.pauses, modulePotso); err != nil {
		return err
	}
	var payload struct {
		Amount *big.Int
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("potsoStakeLock: decode payload: %w", err)
	}
	if payload.Amount == nil || payload.Amount.Sign() <= 0 {
		return fmt.Errorf("potsoStakeLock: amount must be positive")
	}

	var owner [20]byte
	copy(owner[:], sender)

	manager := nhbstate.NewManager(sp.Trie)
	ownerAcc, err := manager.GetAccount(owner[:])
	if err != nil {
		return fmt.Errorf("potsoStakeLock: load owner account: %w", err)
	}
	if ownerAcc.BalanceZNHB.Cmp(payload.Amount) < 0 {
		return fmt.Errorf("potsoStakeLock: insufficient ZNHB balance")
	}

	vaultAddr := manager.PotsoStakeVaultAddress()
	vaultAcc, err := manager.GetAccount(vaultAddr[:])
	if err != nil {
		return fmt.Errorf("potsoStakeLock: load vault account: %w", err)
	}

	ownerAcc.BalanceZNHB = new(big.Int).Sub(ownerAcc.BalanceZNHB, payload.Amount)
	vaultAcc.BalanceZNHB = new(big.Int).Add(vaultAcc.BalanceZNHB, payload.Amount)
	if err := manager.PutAccount(owner[:], ownerAcc); err != nil {
		return fmt.Errorf("potsoStakeLock: persist owner: %w", err)
	}
	if err := manager.PutAccount(vaultAddr[:], vaultAcc); err != nil {
		return fmt.Errorf("potsoStakeLock: persist vault: %w", err)
	}

	nonce, err := manager.PotsoStakeAllocateNonce(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeLock: allocate lock nonce: %w", err)
	}
	now := uint64(sp.blockTimestamp().Unix())
	lock := &potso.StakeLock{Owner: owner, Amount: new(big.Int).Set(payload.Amount), CreatedAt: now}
	if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
		return fmt.Errorf("potsoStakeLock: persist lock: %w", err)
	}
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeLock: load lock nonces: %w", err)
	}
	nonces = append(nonces, nonce)
	if err := manager.PotsoStakePutLockNonces(owner, nonces); err != nil {
		return fmt.Errorf("potsoStakeLock: persist lock nonces: %w", err)
	}
	bonded, err := manager.PotsoStakeBondedTotal(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeLock: load bonded total: %w", err)
	}
	bonded = new(big.Int).Add(bonded, payload.Amount)
	if err := manager.PotsoStakeSetBondedTotal(owner, bonded); err != nil {
		return fmt.Errorf("potsoStakeLock: persist bonded total: %w", err)
	}

	if evt := (events.PotsoStakeLocked{Owner: owner, Amount: payload.Amount}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyPotsoStakeUnbondTransaction(tx *types.Transaction, sender []byte) error {
	if err := nativecommon.Guard(sp.pauses, modulePotso); err != nil {
		return err
	}
	var payload struct {
		Amount *big.Int
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("potsoStakeUnbond: decode payload: %w", err)
	}
	if payload.Amount == nil || payload.Amount.Sign() <= 0 {
		return fmt.Errorf("potsoStakeUnbond: amount must be positive")
	}

	var owner [20]byte
	copy(owner[:], sender)

	manager := nhbstate.NewManager(sp.Trie)
	bonded, err := manager.PotsoStakeBondedTotal(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeUnbond: load bonded total: %w", err)
	}
	if bonded.Cmp(payload.Amount) < 0 {
		return fmt.Errorf("potsoStakeUnbond: insufficient bonded stake")
	}

	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeUnbond: load lock nonces: %w", err)
	}
	if len(nonces) == 0 {
		return fmt.Errorf("potsoStakeUnbond: no active stake locks")
	}

	// Ensure the active locks cover the requested amount before mutating state.
	available := big.NewInt(0)
	for _, nonce := range nonces {
		lock, ok, err := manager.PotsoStakeGetLock(owner, nonce)
		if err != nil {
			return fmt.Errorf("potsoStakeUnbond: load lock: %w", err)
		}
		if !ok || lock == nil || lock.UnbondAt != 0 || lock.Amount == nil {
			continue
		}
		available = new(big.Int).Add(available, lock.Amount)
	}
	if available.Cmp(payload.Amount) < 0 {
		return fmt.Errorf("potsoStakeUnbond: insufficient bonded stake")
	}

	now := uint64(sp.blockTimestamp().Unix())
	withdrawAt := now + potso.StakeUnbondSeconds
	newNonces := make([]uint64, 0, len(nonces)+1)
	remaining := new(big.Int).Set(payload.Amount)
	unbonded := big.NewInt(0)

	for _, nonce := range nonces {
		lock, ok, err := manager.PotsoStakeGetLock(owner, nonce)
		if err != nil {
			return fmt.Errorf("potsoStakeUnbond: load lock: %w", err)
		}
		if !ok || lock == nil {
			continue
		}
		newNonces = append(newNonces, nonce)
		if remaining.Sign() == 0 || lock.Amount == nil || lock.Amount.Sign() == 0 || lock.UnbondAt != 0 {
			continue
		}
		take := new(big.Int)
		if lock.Amount.Cmp(remaining) > 0 {
			take.Set(remaining)
			leftover := new(big.Int).Sub(lock.Amount, remaining)
			lock.Amount = new(big.Int).Set(remaining)
			lock.UnbondAt = now
			lock.WithdrawAt = withdrawAt
			if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
				return fmt.Errorf("potsoStakeUnbond: persist lock: %w", err)
			}
			newNonce, err := manager.PotsoStakeAllocateNonce(owner)
			if err != nil {
				return fmt.Errorf("potsoStakeUnbond: allocate residual nonce: %w", err)
			}
			newLock := &potso.StakeLock{Owner: owner, Amount: leftover, CreatedAt: lock.CreatedAt}
			if err := manager.PotsoStakePutLock(owner, newNonce, newLock); err != nil {
				return fmt.Errorf("potsoStakeUnbond: persist residual lock: %w", err)
			}
			newNonces = append(newNonces, newNonce)
		} else {
			take.Set(lock.Amount)
			lock.UnbondAt = now
			lock.WithdrawAt = withdrawAt
			if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
				return fmt.Errorf("potsoStakeUnbond: persist lock: %w", err)
			}
		}
		ref := potso.WithdrawalRef{Owner: owner, Nonce: nonce, Amount: new(big.Int).Set(take)}
		if err := manager.PotsoStakeQueueAppend(potso.WithdrawDay(withdrawAt), ref); err != nil {
			return fmt.Errorf("potsoStakeUnbond: queue withdrawal: %w", err)
		}
		unbonded.Add(unbonded, take)
		remaining.Sub(remaining, take)
		if remaining.Sign() == 0 {
			break
		}
	}

	if err := manager.PotsoStakePutLockNonces(owner, newNonces); err != nil {
		return fmt.Errorf("potsoStakeUnbond: persist lock nonces: %w", err)
	}
	bonded.Sub(bonded, unbonded)
	if err := manager.PotsoStakeSetBondedTotal(owner, bonded); err != nil {
		return fmt.Errorf("potsoStakeUnbond: persist bonded total: %w", err)
	}

	if evt := (events.PotsoStakeUnbonded{Owner: owner, Amount: unbonded, WithdrawAt: withdrawAt}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyPotsoStakeWithdrawTransaction(tx *types.Transaction, sender []byte) error {
	if err := nativecommon.Guard(sp.pauses, modulePotso); err != nil {
		return err
	}

	var owner [20]byte
	copy(owner[:], sender)

	manager := nhbstate.NewManager(sp.Trie)
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return fmt.Errorf("potsoStakeWithdraw: load lock nonces: %w", err)
	}
	if len(nonces) == 0 {
		return sp.incrementNativeAccountNonce(sender)
	}

	now := uint64(sp.blockTimestamp().Unix())
	keepNonces := make([]uint64, 0, len(nonces))
	total := big.NewInt(0)
	pending := false

	for _, nonce := range nonces {
		lock, ok, err := manager.PotsoStakeGetLock(owner, nonce)
		if err != nil {
			return fmt.Errorf("potsoStakeWithdraw: load lock: %w", err)
		}
		if !ok || lock == nil {
			continue
		}
		if lock.UnbondAt == 0 {
			keepNonces = append(keepNonces, nonce)
			continue
		}
		amount := big.NewInt(0)
		if lock.Amount != nil {
			amount = new(big.Int).Set(lock.Amount)
		}
		if lock.WithdrawAt > now {
			keepNonces = append(keepNonces, nonce)
			if amount.Sign() > 0 {
				pending = true
			}
			continue
		}
		if amount.Sign() > 0 {
			total.Add(total, amount)
		}
		if err := manager.PotsoStakeDeleteLock(owner, nonce); err != nil {
			return fmt.Errorf("potsoStakeWithdraw: delete lock: %w", err)
		}
		if err := manager.PotsoStakeQueueRemove(potso.WithdrawDay(lock.WithdrawAt), owner, nonce); err != nil {
			return fmt.Errorf("potsoStakeWithdraw: remove queue entry: %w", err)
		}
	}

	if err := manager.PotsoStakePutLockNonces(owner, keepNonces); err != nil {
		return fmt.Errorf("potsoStakeWithdraw: persist lock nonces: %w", err)
	}

	if total.Sign() == 0 {
		if pending {
			return fmt.Errorf("potsoStakeWithdraw: no withdrawable locks yet")
		}
		return sp.incrementNativeAccountNonce(sender)
	}

	ownerAcc, err := manager.GetAccount(owner[:])
	if err != nil {
		return fmt.Errorf("potsoStakeWithdraw: load owner account: %w", err)
	}
	vaultAddr := manager.PotsoStakeVaultAddress()
	vaultAcc, err := manager.GetAccount(vaultAddr[:])
	if err != nil {
		return fmt.Errorf("potsoStakeWithdraw: load vault account: %w", err)
	}
	if vaultAcc.BalanceZNHB.Cmp(total) < 0 {
		return fmt.Errorf("potsoStakeWithdraw: staking vault underfunded")
	}
	ownerAcc.BalanceZNHB = new(big.Int).Add(ownerAcc.BalanceZNHB, total)
	vaultAcc.BalanceZNHB = new(big.Int).Sub(vaultAcc.BalanceZNHB, total)
	if err := manager.PutAccount(owner[:], ownerAcc); err != nil {
		return fmt.Errorf("potsoStakeWithdraw: persist owner: %w", err)
	}
	if err := manager.PutAccount(vaultAddr[:], vaultAcc); err != nil {
		return fmt.Errorf("potsoStakeWithdraw: persist vault: %w", err)
	}

	if evt := (events.PotsoStakeWithdrawn{Owner: owner, Amount: total}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.incrementNativeAccountNonce(sender)
}
