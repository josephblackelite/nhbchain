# Structured event reference

The state processor appends structured events (`types.Event`: a `type` string and
a string-to-string `attributes` map) while applying a transaction. This page lists
the transfer and staking events defined in `core/events/transfer.go` and
`core/events/stake.go`. Attribute values are always strings; amounts are decimal
integers in wei unless stated.

When a transaction fails, the events it appended are discarded, except when the
failure is a transfer pause (`transfer.nhb.paused`, `transfer.znhb.paused`) or a
rejected sponsorship (`ApplyTransaction` in `core/state_transition.go`), whose
events are kept.

`nhb_getTransactionReceipt` exposes events as flat `logs` entries and re-derives
them by simulating the transaction; see [rpc.md](./rpc.md#nhb_gettransactionreceipt)
for that shape. The examples below show the underlying event objects.

## `transfer.native`

Emitted once per NHB transfer (`TxTypeTransfer`) and ZNHB transfer
(`TxTypeTransferZNHB`).

| Attribute | Description |
| --- | --- |
| `asset` | `NHB` or `ZNHB` (upper-cased). |
| `from` | Bech32 address that was debited. |
| `to` | Bech32 address that was credited. |
| `amount` | Amount in wei. |
| `txHash` | `0x`-prefixed lowercase hex transaction hash (omitted if the hash is zero). |

```json
{
  "type": "transfer.native",
  "attributes": {
    "asset": "ZNHB",
    "from": "nhb1...",
    "to": "nhb1...",
    "amount": "500000000000000000",
    "txHash": "0x98b4fca3b8e3c6f734944c6c287f66f724d9c917edc1818dc3f028de5a1a6a11"
  }
}
```

When a transfer is rejected because the asset's transfer pause is active, the
node emits `transfer.nhb.paused` or `transfer.znhb.paused` instead, with the same
address and `txHash` attributes plus `reason` and `asset`.

## `stake.delegated`

Emitted by a stake (delegate) transaction. A delegator who delegates to their own
address gets one event. A delegator who delegates to a different validator
produces two events: one for the delegator and one for the validator
(`core/state_transition.go`, `StakeDelegate`).

| Attribute | Description |
| --- | --- |
| `addr` | Address whose stake shares were updated. |
| `sharesAdded` | Shares accrued to this address during the reward-index checkpoint (not a function of `amount`). |
| `newShares` | Share balance after the checkpoint. |
| `lastIndex` | Reward index stored on the account after the checkpoint. |
| `validator` | Validator address receiving the delegation. |
| `amount` | Amount delegated. |
| `locked` | Delegator's total locked ZNHB after the delegation. Only on the delegator event. |

## `stake.undelegated`

Emitted by an unstake transaction. Same delegator-plus-validator pattern as
above.

| Attribute | Description |
| --- | --- |
| `addr` | Address whose shares were checkpointed. |
| `sharesRemoved` | Shares removed during the checkpoint. |
| `newShares` | Share balance after the checkpoint. |
| `lastIndex` | Reward index stored on the account. |
| `validator` | Validator whose delegation was reduced. |
| `amount` | Amount moved into unbonding. |
| `releaseTime` | Unix time when the unbond matures. Delegator event only. |
| `unbondingId` | Id of the pending unbond entry. Delegator event only. |

## `stake.claimed` (unbond claim)

Emitted by `StakeClaim` (`TxTypeStakeClaim`) when a matured unbond is released
back to the delegator's balance. Attributes: `delegator`, `validator`, `amount`,
`unbondingId`.

## `stake.unbondClaimed`

Claiming a matured unbonding entry returns the unbonded ZNHB to the delegator's liquid balance.
This event used to be named `stake.claimed`, the name the legacy alias of `stake.rewardsClaimed`
(below) still carries, so the two could not be told apart by type.

### Attributes

| Attribute | Type | Description |
| --- | --- | --- |
| `delegator` | `string` | Delegator address that claimed the unbond. |
| `validator` | `string` | Validator the stake was bonded to. |
| `amount` | `string` | Amount of ZNHB returned (wei). |
| `unbondingId` | `string` | Identifier of the unbonding entry that was claimed. |

## `stake.rewardsClaimed`

Emitted by a reward claim (`TxTypeStakeClaimRewards`), together with a second
event that reuses the type `stake.claimed` (see below).

| Attribute | Description |
| --- | --- |
| `addr` | Delegator address. |
| `paidZNHB` | ZNHB paid out. |
| `periods` | Payout periods settled (omitted if zero). |
| `aprBps` | APR in basis points in effect (omitted if zero). |
| `nextEligibleUnix` | Unix time of the next payout (omitted if zero). |

The legacy alias event has type `stake.claimed` and attributes `addr`, `minted`
(same value as `paidZNHB`), `periods`, `aprBps`, `nextEligibleUnix`
(`StakeRewardsClaimed.LegacyEvent`). The same type string `stake.claimed` is also
used by the unbond-claim event above, so consumers must distinguish them by
attributes (`minted` versus `delegator`/`unbondingId`).

If the annual emission cap reduces the payout, a `stake.emissionCapHit` event is
emitted first with `requestedZNHB`, `attemptedZNHB`, `allowedZNHB`, `ytd`, `cap`.

## Other staking events

* `stake.paused` — a staking operation was rejected because the module is paused.
  Attributes: `addr`, `operation`, `reason`, `unbondingId` (each only when set).
* `stake.validatorRegistrationChanged` — an account's validator-registration flag
  flipped. Attributes: `addr`, `registered` (`true`/`false`), `at` (unix time,
  when set).
