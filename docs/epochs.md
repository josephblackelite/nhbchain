# Epochs and Validator Rotation

This document explains the epoch snapshot system introduced in the NHB Chain
state processor. It is intended to serve the due-diligence needs of multiple
stakeholders, including auditors, investors, developers, end-users, and
regulators.

## Overview

* Every `epoch` is a fixed number of blocks (configurable, default `100`).
* At the end of each epoch the chain records a snapshot of the composite weight
  `W_i` for every eligible validator.
* Snapshots are retained on-chain (default: latest 64 epochs) to provide an
  auditable history of validator performance and eligibility.
* Optionally, the active validator set is **rotated** to the top `N` validators
  each epoch. Rotation is disabled by default.
* Two structured events are emitted at epoch boundaries:
  * `epoch.finalized` summarises the epoch and the computed weight totals.
  * `validators.rotated` captures the list of validators promoted to the active
    set when rotation is enabled.

## Composite Weight Calculation

The composite weight combines stake and engagement metrics:

```
W_i = (stake_i * stake_weight) + (engagement_i * engagement_weight)
```

Where:

* `stake_i` is the validator's staked balance (ZapNHB).
* `engagement_i` is the exponentially-weighted engagement score already tracked
  by the protocol.
* `stake_weight` and `engagement_weight` are positive integers configured per
  network (defaults: `100` and `1` respectively).

Only accounts whose stake is **greater than or equal to** the protocol minimum
(`1000` ZapNHB) are considered. Validators below the minimum are pruned from the
eligibility pool and from the active validator set.

### Determinism and Tie-Breaking

* Weights are sorted deterministically: higher composite weight first, ties
  broken by ascending lexicographic order of the validator address bytes.
* Dedicated tests verify deterministic ordering, tie-break behaviour, and
  enforcement of the minimum stake threshold.

## Configuration Surface

The epoch system exposes the following configuration parameters (available via
`StateProcessor.SetEpochConfig` / `Node.SetEpochConfig`):

| Field              | Description                                                                     | Default |
|--------------------|---------------------------------------------------------------------------------|---------|
| `Length`           | Number of blocks per epoch (must be `> 0`).                                      | 100     |
| `StakeWeight`      | Multiplier applied to the stake component.                                      | 100     |
| `EngagementWeight` | Multiplier applied to the engagement component.                                | 1       |
| `RotationEnabled`  | When `true`, the validator set is rotated each epoch.                           | false   |
| `MaxValidators`    | Maximum validators retained after rotation (must be `> 0` when rotation on).    | 0       |
| `SnapshotHistory`  | Number of epoch snapshots retained in state (`0` keeps the full history).       | 64      |

Changing the configuration prunes the in-memory snapshot cache according to the
new history length.

## Epoch Lifecycle

1. Transactions are processed normally during a block.
2. Before the block is committed, the state processor checks whether the block
   height is a multiple of the configured epoch length.
3. If so, it computes composite weights for all eligible validators and builds a
   snapshot.
4. The snapshot is stored in state, the `epoch.finalized` event is emitted, and
   (if enabled) the validator set is rotated to the top `MaxValidators` entries.
5. The rotation emits the `validators.rotated` event.

Snapshots capture:

* Epoch number and the block height that finalized the epoch.
* Timestamp (from the block header) at finalization.
* The total composite weight and individual entries per validator (address,
  stake, engagement, composite weight).
* The list of validators selected for the next epoch (always all weights when
  rotation is disabled).

## JSON-RPC Endpoints

Two JSON-RPC endpoints expose the new functionality.

### `nhb_getEpochSummary`

Returns a lightweight view of an epoch.

**Parameters** (optional):

* `epoch` (number) — specific epoch number to query. If omitted the latest
  available epoch is returned.

**Result fields:**

| Field                     | Type     | Description                                                     |
|---------------------------|----------|-----------------------------------------------------------------|
| `epoch`                   | `uint64` | Epoch number.                                                   |
| `height`                  | `uint64` | Block height that finalized the epoch.                          |
| `finalizedAt`             | `int64`  | Block timestamp (Unix seconds).                                 |
| `totalWeight`             | `string` | Decimal-encoded composite weight sum.                           |
| `activeValidators`        | `[]string` | Hex addresses (0x-prefixed) in the active set after rotation. |
| `eligibleValidatorCount`  | `int`    | Number of validators considered when computing weights.         |

**Example:**

```json
{
  "jsonrpc": "2.0",
  "method": "nhb_getEpochSummary",
  "params": [42],
  "id": 1
}
```

### `nhb_getEpochSnapshot`

Returns the full recorded snapshot for an epoch, including per-validator
weights.

**Parameters** (optional):

* `epoch` (number) — specific epoch number. If omitted, the latest snapshot is
  returned.

**Result fields:**

| Field            | Type              | Description                                         |
|------------------|-------------------|-----------------------------------------------------|
| `epoch`          | `uint64`          | Epoch number.                                       |
| `height`         | `uint64`          | Block height that finalized the epoch.              |
| `finalizedAt`    | `int64`           | Block timestamp (Unix seconds).                     |
| `totalWeight`    | `string`          | Decimal composite weight sum.                       |
| `weights`        | `[]object`        | Detailed entries per validator (see below).         |
| `selectedValidators` | `[]string`    | Hex addresses that remain in the active set.        |

Each element of `weights` contains:

| Field             | Type     | Description                                   |
|-------------------|----------|-----------------------------------------------|
| `address`         | `string` | Validator address (0x-prefixed hex).          |
| `stake`           | `string` | Decimal stake used in the calculation.        |
| `engagement`      | `uint64` | Engagement score input.                       |
| `compositeWeight` | `string` | Decimal composite weight for the validator.   |

## Events

### `epoch.finalized`

Attributes:

| Key                   | Description                                         |
|-----------------------|-----------------------------------------------------|
| `epoch`               | Epoch number.                                       |
| `height`              | Block height finalising the epoch.                  |
| `finalized_at`        | Block timestamp (Unix seconds).                     |
| `eligible_validators` | Validators considered when computing weights.       |
| `total_weight`        | Decimal representation of the aggregated weight.    |

### `validators.rotated`

Attributes:

| Key         | Description                                           |
|-------------|-------------------------------------------------------|
| `epoch`     | Epoch number triggering the rotation.                 |
| `validators`| Comma-separated list of 0x-prefixed validator addrs.  |

An empty `validators` attribute indicates that rotation was disabled for the
epoch or that no validators satisfied the minimum stake requirement.

## Audience Notes

### Auditors

* Snapshots are deterministic and reproducible from state, enabling independent
  verification.
* Historical retention ensures traceability of validator eligibility changes.
* Minimum stake enforcement prevents zero-weight or dust accounts from entering
  the validator pool.

### Investors

* Composite weights surface both capital commitment (stake) and operational
  engagement, providing a richer signal of validator quality.
* Optional rotation rewards top performers while preserving transparency into
  the selection process.

### Developers & Implementers

* Use `Node.SetEpochConfig` / `StateProcessor.SetEpochConfig` to customise epoch
  length, weighting, rotation, and snapshot retention.
* Snapshots are stored in the state trie under `epoch-history`, enabling light
  clients or indexers to fetch data directly from state if required.
* JSON-RPC endpoints offer machine-readable access without full state decoding.

### Users

* `nhb_getEpochSummary` provides a simple way to inspect current validators and
  their aggregate weight.
* Rotations (if enabled) are announced via events, allowing wallet UIs to alert
  delegators when the validator set changes.

### Regulators

* The system exposes clear, immutable records of validator eligibility and
  selection, aiding oversight and compliance checks.
* Deterministic tie-break rules eliminate discretionary behaviour in validator
  promotions.

## Testing Guarantees

Automated tests cover:

* Deterministic ordering of composite weights.
* Address-based tie-breaking when weights are equal.
* Strict enforcement of the minimum stake threshold during rotation.

These tests are located in `core/epoch_state_test.go` and executed as part of
`go test ./...`.
