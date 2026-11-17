# POTSO Staking Locks

ZNHB holders can lock ZNHB into a POTSO stake vault. The bonded total counts as the stake input of the POTSO reward weights ([weights.md](weights.md)). It is separate from validator staking (`TxTypeStake` and friends) and does not affect BFT voting power ([consensus-integration.md](consensus-integration.md)). Code: `core/potso_stake_tx.go`, `native/potso/stake.go`, `core/state/manager.go`.

## Transactions

Lock, unbond and withdraw are signed native transactions submitted with `nhb_sendTransaction` like any other transaction. The owner is always the transaction signer (`tx.From()`); replay protection is the account nonce.

| Type | Value | Payload |
| --- | --- | --- |
| `TxTypePotsoStakeLock` | `0x2D` | RLP `{Amount *big.Int}` (wei, must be positive) |
| `TxTypePotsoStakeUnbond` | `0x2E` | RLP `{Amount *big.Int}` (wei, must be positive) |
| `TxTypePotsoStakeWithdraw` | `0x2F` | none |

Each is quota-checked against the `potso` module (`applyQuota(modulePotso, ...)`) and blocked while the `potso` module is paused. They do not increment the POTSO meters.

## Lifecycle

1. **Lock.** `Amount` ZNHB moves from the owner's `BalanceZNHB` to the vault account. A new lock is created with the next lock nonce, `CreatedAt` = block timestamp, and `UnbondAt = 0`. The owner's bonded total increases. Fails with "insufficient ZNHB balance" if the balance is too low. Emits `potso.stake.locked`.
2. **Unbond.** Takes `Amount` from the owner's locks that are still bonded (`UnbondAt == 0`), in the order of the owner's lock index (creation order; the bonded remainder of a split lock is appended at the end). Fails with "insufficient bonded stake" if the bonded locks do not cover it. A lock that is only partly consumed keeps its nonce with the unbonded amount (`UnbondAt` = block timestamp, `WithdrawAt = UnbondAt + 7 days`), and its remainder becomes a new bonded lock with a fresh nonce. Locks after the last one consumed are left untouched and stay in the index, so their stake can still be unbonded and withdrawn later. The bonded total decreases immediately. Emits `potso.stake.unbonded`.
3. **Withdraw.** Pays out every lock whose `WithdrawAt` is not later than the block timestamp, in one transaction, deletes those locks, and moves the total from the vault back to the owner. Emits `potso.stake.withdrawn`. Behaviour when nothing is payable:
   - no locks, or only bonded locks: the transaction succeeds as a no-op (the nonce still advances);
   - locks that are unbonding but not yet mature: the transaction fails with "potsoStakeWithdraw: no withdrawable locks yet".

The cooldown is the constant `StakeUnbondSeconds = 7 * 24 * 60 * 60` in `native/potso/stake.go`. It is not configurable at run time.

A single owner can have many locks. There is no minimum amount and no limit on the number of locks in the code.

## State

How the keys map to trie keys (`core/state/manager.go`): `kvKey(x)` is `Keccak256(x)`, and `KVPut`/`KVGet`/`KVDelete`/`KVAppend` store under `kvKey(key)`.

- `potso/stake/<owner>` holds the bonded total. `PotsoStakeSetBondedTotal` writes it with `writeBigInt(potsoStakeTotalKey(owner))`, an RLP-encoded `big.Int` under a single `Keccak256("potso/stake/" + owner)`.
- `potso/stake/owners` is written with `KVAppend`/`KVPut` on the plain key, so it sits under a single `Keccak256("potso/stake/owners")`.
- The other records are written with `KVPut` (RLP values), but their key helpers (`potsoStakeNonceKey`, `potsoStakeLockIndexKey`, `potsoStakeLockKey`, `potsoStakeQueueKey`) already return `Keccak256(prefix + id)`. Their trie key is `Keccak256(Keccak256(prefix + id))`, hashed twice. The comment on `PotsoStakeDeleteLock` describes this as "twice-wrapped".

| Key | Value |
| --- | --- |
| `potso/stake/nonce/<owner>` | `uint64` lock nonce counter |
| `potso/stake/locks/index/<owner>` | `[]uint64` lock nonces, in creation order |
| `potso/stake/locks/<owner>:<nonce>` | `StakeLock` |
| `potso/stake/unbondq/<day>` | `[]WithdrawalRef` queue bucket, day = UTC date of `WithdrawAt` |
| `potso/stake/owners` | index of owners with a positive bonded total (read by reward processing) |

`<owner>` is the raw 20-byte address and `<nonce>` is decimal.

When `PotsoStakePutQueueEntries` is given an empty list (which `PotsoStakeQueueRemove` does after removing the last entry of a bucket) it calls `m.trie.Update(key, nil)` with the once-hashed helper key, not the twice-hashed key the bucket was written under. The bucket is therefore not cleared and keeps its old entries. Nothing outside tests reads the queue (`PotsoStakeQueueEntries` has no non-test caller), so this has no effect on withdrawals, which are driven by the lock records.

```go
type StakeLock struct {
    Owner      [20]byte
    Amount     *big.Int
    CreatedAt  uint64
    UnbondAt   uint64
    WithdrawAt uint64
}
```

## Vault

The vault address is the last 20 bytes of `Keccak256("module/potso/stake/vault")` (`potsoStakeModuleAddress`). Lock credits it and withdraw debits it. Withdraw fails with "staking vault underfunded" if the vault balance is below the payout.

## Events

| Event | Attributes |
| --- | --- |
| `potso.stake.locked` | `owner`, `amount` |
| `potso.stake.unbonded` | `owner`, `amount`, `withdrawAt` |
| `potso.stake.withdrawn` | `owner`, `amount` |

## RPC: `potso_stake_info`

Requires authentication (JWT bearer token or verified client certificate; HTTP 401 otherwise). Params `[{"owner": "nhb1..."}]`. Result:

```json
{
  "bonded": "100000000000000000000",
  "pendingUnbond": "0",
  "withdrawable": "0",
  "locks": [
    {"nonce": 1, "amount": "100000000000000000000", "createdAt": 1732473600, "unbondAt": 0, "withdrawAt": 0}
  ]
}
```

`bonded` is the stored bonded total (locks with `UnbondAt == 0`). `pendingUnbond` and `withdrawable` sum the unbonding locks whose `WithdrawAt` is later than, or not later than, the node's current wall-clock time. `locks` lists every lock, including unbonding ones. There are no `potso_stake_lock`, `potso_stake_unbond` or `potso_stake_withdraw` RPC methods.

## CLI

```bash
nhb-cli potso stake lock --amount 100e18 [--key wallet.key]
nhb-cli potso stake unbond --amount 40e18 [--key wallet.key]
nhb-cli potso stake withdraw [--key wallet.key]
nhb-cli potso stake info --owner nhb1...        # needs NHB_RPC_TOKEN
```

`lock`, `unbond` and `withdraw` sign with the key file (default `wallet.key`), fetch the account nonce, and broadcast a transaction with `GasLimit` 100000 and `GasPrice` 1. The owner is the key's own address; there is no `--owner` or `--nonce` flag on these. `--amount` accepts a positive integer number of wei, optionally with a decimal point and/or `e` exponent (`100e18`, `1.5e18`); a value that is not a whole number of wei is rejected. `info` sends `NHB_RPC_TOKEN` as a bearer token (`nhb-cli rpc-token` prints one when run on the node host).
