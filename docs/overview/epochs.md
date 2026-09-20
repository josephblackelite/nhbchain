# Epochs and validator selection

An epoch is a fixed number of blocks. At each epoch boundary the state processor
computes a weight for every eligible validator, stores a snapshot, emits an event,
settles epoch rewards, and updates the validator set. Code:
`core/epochs.go`, `core/epoch/`, `core/events/epoch.go`.

## Configuration

`epoch.Config` (`core/epoch/config.go`):

| Field | Meaning | Default (`DefaultConfig`) |
| --- | --- | --- |
| `Length` | Blocks per epoch; must be greater than 0. | 100 |
| `StakeWeight` | Multiplier on the stake component. | 100 |
| `EngagementWeight` | Multiplier on the engagement component. | 1 |
| `RotationEnabled` | Rotate the validator set to the top `MaxValidators` each epoch. | false |
| `MaxValidators` | Size of the rotated set; must be greater than 0 when rotation is enabled. | 0 |
| `SnapshotHistory` | Snapshots kept in state; `0` keeps all. | 64 |

`Validate` also requires at least one of the two weights to be non-zero. The
state processor starts from `DefaultConfig()`. No key in `config.toml` or the
genesis loader sets an epoch config, and the shipped binaries do not call
`SetEpochConfig`, so a running node uses the defaults above; changing them means
calling `Node.SetEpochConfig` / `StateProcessor.SetEpochConfig` from Go.
Changing the history length prunes retained snapshots.

## When the boundary runs

`ProcessBlockLifecycle(height, timestamp)` runs after a block's transactions. It
does the per-block housekeeping (subscription and lending settlement, POTSO
rewards, the ZNHB supply invariant check, one-time state migrations) and then,
when `height` is a non-zero multiple of `Length`, ticks loyalty smoothing and
calls `finalizeEpoch`, which:

1. Computes weights (below) and the epoch number `height / Length`.
2. Selects validators.
3. Settles epoch rewards and the buyback epoch.
4. Appends the snapshot, prunes history to `SnapshotHistory`, and persists it in
   state under the trie key `keccak256("epoch-history")` (RLP).
5. Emits `epoch.finalized`.
6. Applies the validator selection to the active validator set.

## Who is eligible and how weight is computed

For each address in the eligible-validator set (`computeEpochWeights`), all of
these must hold:

* The account is registered as a validator (`ValidatorRegistered`, set by a stake
  transaction with the register-validator flag; `nhb-cli register-validator`).
* Its eligibility basis is at least the governed minimum stake
  `staking.minimumValidatorStake` (default 10,000 ZNHB, `10000000000000000000000`
  wei, `defaultMinimumValidatorStakeWei` in `native/governance/types.go`). The
  basis is the account's total `Stake`, which includes ZNHB delegated to it by
  other addresses; there is no separate self-stake requirement
  (`validatorEligibilityBasis`). Reward accrual is different: it excludes
  delegated-in stake.
* The account is not delegating its own stake to a different validator
  (`selfDelegated`).
* It has sent a heartbeat recently enough: `EngagementLastHeartbeat` is non-zero,
  not more than 2 minutes in the future, and within the readiness grace period of
  the block time. The grace period is `max(5 x HeartbeatInterval, 15 minutes)`
  (`validatorReadyForActivation`; with the default one-minute heartbeat interval
  it is 15 minutes).

The composite weight is

```
W = basis * StakeWeight + engagementScore * EngagementWeight
```

(`epoch.ComputeCompositeWeight`), using the account's `EngagementScore` (see
[engagement](./engagement-program.md)). Weights are sorted by descending `W`, ties
broken by ascending address bytes (`epoch.SortWeights`).

## Selection and rotation

* Rotation disabled (default): every weighted validator is selected, and the
  active validator set becomes the eligible validators that meet the minimum
  stake, are registered and pass the heartbeat check.
* Rotation enabled: the top `MaxValidators` by weight (with stake at least the
  minimum) are selected, re-checked against registration, minimum stake and
  self-delegation, and become the active set; `validators.rotated` is emitted.
* If the resulting set would be empty, a fallback set is used: previously known
  validators (current set, eligible set, recent snapshots' selections) that are
  registered, meet the minimum stake, are self-delegated and have ever sent a
  heartbeat.

## Events

`epoch.finalized` attributes: `epoch`, `height`, `finalized_at` (unix seconds),
`eligible_validators` (number of weighted validators), `total_weight`.

`validators.rotated` (rotation enabled only) attributes: `epoch`, `validators`
(comma-separated `0x`-prefixed hex addresses).

The reward settlement performed at the same boundary emits `rewards.epoch_closed`
and `rewards.paid` (`core/rewards_logic.go`).

## JSON-RPC

Both methods take an optional epoch number, either as a plain number or as
`{"epoch": N}`; without it the latest snapshot is used. An unknown epoch returns
HTTP 404 with code `-32000` (`epoch summary not found` / `epoch snapshot not
found`).

`nhb_getEpochSummary` returns:

| Field | Type | Description |
| --- | --- | --- |
| `epoch` | number | Epoch number. |
| `height` | number | Block height that finalized it. |
| `finalizedAt` | number | Block timestamp (unix seconds). |
| `totalWeight` | string | Sum of composite weights. |
| `activeValidators` | string[] | `0x`-prefixed hex addresses selected. |
| `eligibleValidatorCount` | number | Number of weighted validators. |

`nhb_getEpochSnapshot` returns `epoch`, `height`, `finalizedAt`, `totalWeight`,
`weights[]` (`address`, `stake`, `engagement`, `compositeWeight`) and
`selectedValidators[]`. `address` and the selected addresses are `0x`-prefixed
hex; `stake` (the eligibility basis) and `compositeWeight` are decimal strings.

```json
{"jsonrpc": "2.0", "id": 1, "method": "nhb_getEpochSummary", "params": [42]}
```

## Tests

`core/epoch_state_test.go` and `core/validator_registration_test.go` cover
ordering, tie-breaking and the eligibility gates.
