# Epoch Rewards and ZNHB Emission

This document describes the epoch reward emission system for ZapNHB (ZNHB).
It supports the due-diligence needs of auditors, investors, developers,
implementers, end-users, and regulators.

## Overview

* Emissions follow a configurable schedule `E(t)` (per epoch) and are split across
  validators, stakers, and engagement reward pools using basis-point weights.
* Rewards accrue deterministically per block and are settled when an epoch
  finalises. Settlement produces immutable records in state and structured
  events for off-chain observers.
* Payouts are credited directly to account ZNHB balances—no treasury account is
  involved. Undistributed portions remain recorded as "unused" allocations.
* JSON-RPC endpoints expose settlement summaries and per-account payout data.

## Configuration Surface

Reward configuration is represented by `rewards.Config` (see
`core/rewards/config.go`). The state processor and node expose the following
helpers:

| Method | Description |
|--------|-------------|
| `StateProcessor.RewardConfig()` | Returns a defensive copy of the active configuration. |
| `StateProcessor.SetRewardConfig(cfg rewards.Config)` | Validates and applies a new configuration, pruning settlement history to the configured retention window. |
| `Node.RewardConfig()` / `Node.SetRewardConfig(cfg)` | Remote accessors mirroring the state processor helpers. |
| `StateProcessor.RewardEpochSettlement(epoch uint64)` | Fetch a specific settlement record (copy) from state. |
| `StateProcessor.LatestRewardEpochSettlement()` | Fetch the newest settlement (copy) if present. |
| `Node.RewardEpochSettlement(epoch)` / `Node.LatestRewardEpochSettlement()` | Node-level convenience wrappers. |

`rewards.Config` fields:

* `Schedule []EmissionStep` – piecewise-constant emission amounts. Each step is
  active from `StartEpoch` (1-indexed) until the next entry. Amounts are integer
  ZNHB values (no decimals).
* `ValidatorSplit`, `StakerSplit`, `EngagementSplit` – basis point weights (0 to
  10,000). Non-zero configurations must sum to exactly 10,000.
* `HistoryLength` – number of settlement records retained (0 keeps the full
  history).

`Config.Validate()` enforces monotonic schedule ordering, non-negative amounts,
and correct basis-point totals. `SplitEmission(total)` splits the per-epoch
emission into category amounts, allocating rounding dust to the engagement
bucket to preserve conservation.

## Accrual and Settlement Flow

1. **Per-block accrual:** During `ProcessBlockLifecycle`,
   `StateProcessor.accrueEpochRewards(height)` increments category accruals for
   the current epoch. Each block receives the deterministic per-block share, with
   remainders carried to the earliest blocks.
2. **Epoch finalisation:** When `height % epoch.Length == 0`,
   `settleEpochRewards(snapshot)` executes:
   * Copies the validator weight snapshot (stake + engagement) computed for the
     epoch.
   * Uses the per-epoch emission totals to build deterministic payouts:
     - Validator pool: equal split across the rotated validator set (`snapshot.Selected`).
     - Staker pool: proportional to validator stake.
     - Engagement pool: proportional to engagement scores. Zero-denominator pools
       remain unused and are recorded as such.
   * Credits account ZNHB balances, emits `rewards.paid` events per recipient,
     persists settlement metadata (with payout breakdowns), and finally emits a
     `rewards.epoch_closed` summary event.
   * Settlement is idempotent—if a record for an epoch already exists, rerunning
     the lifecycle results in no additional payouts or events.

Settlement records are stored under the `reward-history` trie key. Each record
captures planned vs. paid totals, unused amounts, block counts, and per-account
payout components (validators/stakers/engagement).

## Events

### `rewards.paid`

Emitted once per account receiving a reward during settlement.

| Attribute | Description |
|-----------|-------------|
| `epoch` | Epoch number. |
| `account` | Bech32 address of the recipient. |
| `amount` | Total ZNHB paid. |
| `validators` | Portion derived from the validator pool (optional, omitted if zero). |
| `stakers` | Portion from the staker pool (optional). |
| `engagement` | Portion from the engagement pool (optional). |

### `rewards.epoch_closed`

Summarises the epoch settlement results.

| Attribute | Description |
|-----------|-------------|
| `epoch` | Epoch number. |
| `height` | Block height that finalised the epoch. |
| `closed_at` | Block timestamp (Unix seconds). |
| `blocks` | Number of blocks accrued in the epoch. |
| `planned_total` / `paid_total` | Emission vs. distributed totals (decimal strings). |
| `validators_planned` / `validators_paid` | Category-level totals. |
| `stakers_planned` / `stakers_paid` | Category-level totals. |
| `engagement_planned` / `engagement_paid` | Category-level totals. |

Unused amounts can be derived by subtracting `*_paid` from `*_planned`.

## JSON-RPC Endpoints

Two endpoints provide machine-readable access.

### `nhb_getRewardEpoch`

**Parameters:**

* Optional `epoch` (number or `{ "epoch": <uint64> }`). When omitted, the latest
  settlement is returned.

**Result fields:**

| Field | Type | Description |
|-------|------|-------------|
| `epoch` | `uint64` | Epoch number. |
| `height` | `uint64` | Finalising block height. |
| `closedAt` | `int64` | Finalising block timestamp (Unix seconds). |
| `blocks` | `uint64` | Number of blocks accrued in the epoch. |
| `plannedTotal` / `paidTotal` | `string` | Decimal ZNHB amounts. |
| `validatorsPlanned` / `validatorsPaid` | `string` | Category totals. |
| `stakersPlanned` / `stakersPaid` | `string` | Category totals. |
| `engagementPlanned` / `engagementPaid` | `string` | Category totals. |
| `unusedTotal`, `unusedValidators`, `unusedStakers`, `unusedEngagement` | `string` | Undistributed amounts per category. |
| `payouts` | `[]object` | Detailed per-account breakdowns. |

Each element of `payouts` contains `account` (0x-prefixed hex), `total`, and the
category-specific amounts (`validators`, `stakers`, `engagement`).

### `nhb_getRewardPayout`

Retrieves a payout for a specific account.

**Parameters:**

* `account` (Bech32 or 0x-prefixed hex, required).
* Optional `epoch` (number or field in an object).

**Result fields:**

| Field | Type | Description |
|-------|------|-------------|
| `epoch` | `uint64` | Epoch number containing the payout. |
| `payout` | `object` | Same shape as entries returned by `nhb_getRewardEpoch`. |

If the account has no payout for the requested epoch, the endpoint returns a
404 error.

Authentication is not required for these read-only endpoints.

## Storage & Persistence

* Settlements are persisted to the state trie under the hashed key
  `reward-history`. Retention obeys `Config.HistoryLength`.
* Per-account payout breakdowns are stored alongside each settlement record and
  are included in RPC responses.
* The settlement structures are cloned when returned via API helpers to avoid
  leaking mutable state.

## Testing Strategy

Unit tests in `core/rewards_logic_test.go` cover:

* **Distribution sums:** Ensures the total paid matches the configured emission
  and matches the sum of per-account payouts.
* **Rounding:** Verifies deterministic handling of integer division remainders
  across staker and engagement pools.
* **Empty sets:** Confirms that epochs with no eligible validators do not panic
  and record the full amount as unused.
* **Idempotency:** Validates that rerunning epoch finalisation does not emit
  duplicate payouts or mutate balances.

Integration tests for epochs (`core/epoch_state_test.go`) continue to succeed,
with reward persistence using RLP-friendly encodings (`ClosedAt` stored as
`uint64`).

## Audience Notes

### Auditors

* Every settlement is fully persisted with per-account breakdowns, enabling
  deterministic recomputation from state.
* Undistributed amounts are explicitly tracked, preventing silent supply drift.
* Events provide a verifiable audit trail linking on-chain balances to emission
  calculations.

### Investors

* Transparent emission schedule and split configuration ensure predictable
  token economics.
* Reward history (via RPC or state inspection) reveals validator performance and
  engagement incentives over time.

### Developers & Implementers

* Use `Node.SetRewardConfig` (or `StateProcessor.SetRewardConfig`) to adjust
  emission schedules and splits as part of governance flows. Validation prevents
  misconfiguration (e.g., incorrect basis point totals).
* Settlement helpers (`RewardEpochSettlement`, `LatestRewardEpochSettlement`)
  return defensive copies suitable for UI or analytics backends.
* JSON-RPC endpoints simplify integration for dashboards or indexers without
  needing to parse raw state.

### End-users

* `rewards.paid` events allow wallets or explorers to notify users of earned
  ZNHB, including the category contributions.
* `nhb_getRewardPayout` exposes a simple API for verifying rewards credited to a
  specific address.

### Regulators

* Deterministic distribution rules (stake- and engagement-based) and immutable
  settlement history provide a clear compliance trail.
* The emission schedule can be published alongside governance decisions to
  demonstrate adherence to supply policies.

## Function Reference

Key functions added or modified to support rewards:

* `StateProcessor.accrueEpochRewards(height uint64)` – internal helper invoked
  each block to accumulate per-category totals.
* `StateProcessor.settleEpochRewards(snapshot epoch.Snapshot)` – orchestrates
  settlement, storage, and event emission at epoch boundaries.
* `rewards.NewAccumulator` – maintains per-block accrual state including
  remainder distribution.
* `distributeValidatorRewards`, `distributeStakerRewards`,
  `distributeEngagementRewards` – deterministic allocation helpers that ensure
  conservation and reproducible rounding.

Together these components ensure reward emissions are transparent, reproducible,
and verifiable across all stakeholders.
