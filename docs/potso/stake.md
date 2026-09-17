# POTSO Staking Locks

ZapNHB (ZNHB) holders can bond their tokens into POTSO to increase their participation weight. This document captures the staking lock lifecycle, storage layout, RPC interface, CLI tooling, and operational considerations for auditors and compliance teams.

## Lifecycle Overview

1. **Lock** – The owner authorises a transfer of ZNHB into the staking vault and a new lock record is created. Locked balances remain illiquid but continue to accrue POTSO weight.
2. **Unbond** – The owner schedules some or all of their bonded balance for withdrawal. The amount immediately stops contributing to the bonded total and enters a cooldown period defined by `StakeUnbondSeconds` (7 days by default).
3. **Withdraw** – After the cooldown the owner retrieves the matured amount. Withdrawals are idempotent and a lock can only be paid out once.

A single owner may maintain multiple concurrent locks. Unbonding a partial amount splits the affected lock into an unbonding fragment and a residual bonded fragment with a fresh nonce.

## State Model

Locks are tracked entirely inside the POTSO namespace. All keys are Keccak-256 hashed before reaching the underlying trie.

| Key | Value |
| --- | --- |
| `potso/stake/<owner>` | `*big.Int` bonded total |
| `potso/stake/nonce/<owner>` | `uint64` next lock nonce |
| `potso/stake/authnonce/<owner>` | `uint64` latest staking authorisation nonce |
| `potso/stake/locks/index/<owner>` | `[]uint64` lock nonce ordering |
| `potso/stake/locks/<owner>/<nonce>` | `StakeLock` payload |
| `potso/stake/unbondq/<day>` | `[]WithdrawalRef` queue bucket |

The lock payload is defined in Go as:

```go
type StakeLock struct {
        Owner      [20]byte
        Amount     *big.Int
        CreatedAt  uint64
        UnbondAt   uint64
        WithdrawAt uint64
}
```

Locks with `UnbondAt == 0` are still bonded. When a lock enters cooldown, `UnbondAt` records the UNIX timestamp of the request and `WithdrawAt` is set to `UnbondAt + StakeUnbondSeconds`. Matured locks are removed from storage once withdrawn.

The unbonding queue buckets (`WithdrawalRef`) group locks by the UTC day of their withdrawal window. The queue enables efficient scanning for matured payouts without iterating every lock across the network.

## Module Vault

Funds are held in a deterministic module address derived from the seed `module/potso/stake/vault`. When locking, the owner’s ZNHB balance is debited and the same amount is credited to the vault account. Withdrawals perform the inverse transfer and the vault must therefore remain solvent at all times. Any mismatch indicates a severe accounting violation and should trigger immediate operator review.

## Events

Three event types extend the existing POTSO stream:

| Event | Attributes |
| --- | --- |
| `potso.stake.locked` | `owner`, `amount` |
| `potso.stake.unbonded` | `owner`, `amount`, `withdrawAt` |
| `potso.stake.withdrawn` | `owner`, `amount` |

These events enable downstream consumers to track lock creation, pending withdrawals, and completed payouts without reconstructing state from scratch.

## JSON-RPC Endpoints

Lock, unbond, and withdraw are real signed native transactions
(`TxTypePotsoStakeLock`/`Unbond`/`Withdraw`), submitted via the generic
`nhb_sendTransaction` RPC method like every other signed transaction on this
chain -- there is no bespoke `potso_stake_lock`/`potso_stake_unbond`/
`potso_stake_withdraw` RPC method anymore. Only the read-only status query
remains a dedicated method:

| Method | Params | Result |
| --- | --- | --- |
| `potso_stake_info` | `{owner}` | `{bonded, pendingUnbond, withdrawable, locks}` |

The owner of a lock/unbond/withdraw is always the transaction's own
cryptographically recovered signer (`tx.From()`) -- there is no owner field
in the payload to spoof, and no separate bespoke signature or nonce to
manage. Replay protection is the same standard account nonce every other
transaction on this chain uses (`nhb_getBalance`'s `nonce` field), not a
staking-specific one. `TxTypePotsoStakeLock`/`Unbond` carry an RLP-encoded
`{Amount}` payload (a positive ZNHB-wei integer); `TxTypePotsoStakeWithdraw`
carries no payload at all.

Withdrawals process every matured lock fragment in one transaction and are
safe to submit repeatedly -- a call with nothing left to withdraw succeeds
as a no-op rather than erroring.

## CLI Support

`nhb-cli` bundles convenience wrappers under `potso stake`:

```bash
nhb-cli potso stake lock --amount 100e18 --key wallet.key
nhb-cli potso stake unbond --amount 40e18 --key wallet.key
nhb-cli potso stake withdraw --key wallet.key
nhb-cli potso stake info --owner nhb1...
```

Amounts accept decimal or scientific notation (`100e18`, `1.5e18`, etc.).
`lock`/`unbond`/`withdraw` sign and broadcast a real transaction using the
key's own address as owner and the account's current on-chain nonce -- there
is no `--owner` or `--nonce` flag anymore, since the owner is always
whichever key signs, and the nonce is fetched automatically. `info` remains
a plain read-only query by address.

## Compliance & Operational Notes

* **Cooldown Enforcement** – `StakeUnbondSeconds` defaults to 604800 seconds (7 days). Operators may adjust this constant before network launch but must coordinate with wallet providers and publish any changes.
* **Vault Solvency** – Periodically reconcile `potso/stake/<owner>` totals against the vault balance to ensure no out-of-band transfers occurred. Any imbalance breaks withdrawal guarantees.
* **Idempotency Guarantees** – Locks are deleted immediately after a successful withdrawal. This ensures repeated calls cannot double-spend funds and provides clear audit trails.
* **Signature Requirements** – Every state-changing action is a standard signed transaction from the owner's own key, applied identically by every validator via consensus (not a single validator's local RPC call). Possession of the private key remains the final authority.
* **Monitoring** – Track `potso.stake.unbonded` events to forecast upcoming withdrawals and confirm the queue drains as expected. Alert on large pending totals that fail to mature.

By following the lifecycle and operational guidance above, validators and compliance auditors can transparently monitor staking activity without ambiguity.
