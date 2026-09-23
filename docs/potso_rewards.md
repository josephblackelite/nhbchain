# POTSO Epoch Rewards (ZNHB)

This document describes how `processPotsoRewardEpoch` (`core/state_transition.go`) distributes ZNHB each reward epoch from a configured treasury account, using the composite weights in [weights.md](potso/weights.md).

## Overview

- **What it does:** at each epoch boundary, transfers up to `EmissionPerEpoch` ZNHB from the account at `[potso.rewards].TreasuryAddress` to winning addresses (auto mode) or records claimable amounts (claim mode, see [payout modes](potso/rewards-modes.md)). No ZNHB is minted by this code; balances are moved from the treasury account. When that account is the chain's admin wallet, the ZNHB Reward Pool ledger is reduced by the amount paid to other addresses in the same block (`processPotsoRewardEpoch`; `ProcessBlockLifecycle` also wraps `maybeProcessPotsoRewards` in `withTreasuryPoolBooking` so any remaining movement of that wallet is booked, `core/epochs.go`, `core/znhb_treasury_pool.go`).
- **Cadence:** `ProcessBlockLifecycle` (`core/epochs.go`) calls `maybeProcessPotsoRewards` on every block. With `currentEpoch = height / EpochLengthBlocks`, it processes every epoch from the one after `potso/rewards/lastProcessed` (or 0 if none) up to `currentEpoch - 1`, in order. Nothing is processed when `EpochLengthBlocks = 0`, `EmissionPerEpoch <= 0`, or `currentEpoch = 0`. An epoch is a number of blocks, so its length in time is `EpochLengthBlocks` times the block interval, which follows each validator's `[consensus] MinBlockInterval` ([block cadence](consensus/block-cadence.md)).
- **Inputs:** POTSO stake locks, the stake basis of eligible validators, and per-epoch engagement counters, combined as described in [weights.md](potso/weights.md).
- **Budget:** `B = min(EmissionPerEpoch, treasury ZNHB balance)`. A smaller treasury therefore shrinks the payouts proportionally. If `B <= 0`, no weights are computed, no snapshot is stored and no winners are paid, but the epoch is still marked processed.

## Weighting model

```
w_i = α * stakeShare_i + (1 − α) * engagementShare_i
```

- `α = AlphaStakeBps / 10000`. The value used is `RewardConfig.AlphaStakeBps`, which comes from `[potso.weights].AlphaStakeBps` when that is non-zero (always, after `config.Load` defaults; default `7000`).
- `stakeShare_i` is `stake_i` over the total stake of the candidates that passed the filters. `stake_i` is the POTSO bonded total plus, for eligible validators, their eligibility basis (the account's total `Stake`, including ZNHB delegated in by third parties).
- `engagementShare_i` is the decayed engagement after filters and cap over the total.
- A zero total makes that component 0.

Candidates are ranked by `w_i`. At most `MaxWinnersPerEpoch` (when non-zero) of the ranked entries are paid, and the ranking itself is cut to `TopKWinners` beforehand.

## Payout calculation

1. `payout_i = floor(B * w_i)`, then the optional per-winner cap `MaxUserShareBps` with redistribution ([spec.md](potso/spec.md)).
2. Payouts that are `<= 0` or below `MinPayoutWei` are dropped.
3. `TotalPaid` is the sum of the remaining payouts and `Remainder = B - TotalPaid` (stored in the epoch meta). The remainder is simply not debited from the treasury (auto mode) or not recorded (claim mode). The `CarryRemainder` setting is not read by any code, so there is no separate carry-over.
4. Auto mode: debit the treasury by `TotalPaid`, credit each winner, write a claim record (`claimed = true`, `mode = auto`, `claimedAt` = block time of settlement) and a history entry per winner. Claim mode: write a claim record per winner (`claimed = false`); nothing on a running node settles these records ([payout modes](potso/rewards-modes.md)).
5. Persist winners, meta and emit events:
   - `potso.reward.paid` per winner in auto mode: `epoch`, `address`, `amount`, `mode`.
   - `potso.reward.ready` per winner in claim mode: same attributes.
   - `potso.reward.epoch`: `epoch`, `totalPaid`, `winners`, `emission`, `budget`, `remainder`.

## State persistence

Records use `KVPut` with the plain format-string keys below (`potsoReward*Key` in `core/state/manager.go`, which do not pre-hash), so each RLP value is stored under a single `Keccak256(key)`. `<addr>` in the reward keys is lower-case hex of the 20 address bytes.

- `potso/rewards/lastProcessed` - latest epoch index processed.
- `potso/rewards/epoch/<E>/meta` - `RewardEpochMeta` (`Epoch`, `Day`, `StakeTotal`, `EngagementTotal`, `AlphaBps`, `Emission`, `Budget`, `TotalPaid`, `Remainder`, `Winners`). `Day` is the UTC date of the block that processed the epoch, used only as a label.
- `potso/rewards/epoch/<E>/winners` - ordered winner addresses.
- `potso/rewards/epoch/<E>/payout/<addr>` - payout amount.
- `potso/rewards/epoch/<E>/claim/<addr>` - `RewardClaim` (`Amount`, `Claimed`, `ClaimedAt`, `Mode`).
- `potso/rewards/history/<addr>` - settled payouts for the address, oldest first.
- `potso/metrics/snapshot/<E>` and `snapshots/potso/<E>/weights` - the weight snapshot ([weights.md](potso/weights.md#persistence)).

An epoch whose meta already exists is skipped, so processing is idempotent.

## RPC endpoints

Routing is in `rpc/http.go`. Every parameter list is a single JSON object. No authentication is required for `potso_epoch_info`, `potso_epoch_payouts`, `potso_rewards_history`, `potso_rewards_outflow` and `potso_export_epoch`, and none of them changes state:

- `potso_epoch_info` - params optional `{"epoch": N}`; default is the last processed epoch. Result: `epoch`, `day`, `stakeTotal`, `engagementTotal`, `alphaBps`, `emission`, `budget`, `totalPaid`, `remainder`, `winners` (amounts are decimal wei strings). Unknown epoch: HTTP 404, "epoch not found".
- `potso_epoch_payouts` - params `{"epoch": N, "cursor": "nhb1...", "limit": N}` (`cursor` and `limit` optional; `limit` defaults to 50). Result: `{"epoch": N, "payouts": [{"user": "nhb1...", "amount": "..."}]}`. The cursor is the last address of the previous page; the next page starts after it in winner order. An unknown cursor restarts from the first winner. There is no next-cursor field.
- `potso_rewards_history` and `potso_export_epoch`: see [rewards API](potso/rewards-api.md).
- `potso_rewards_outflow` - see below.

`potso_reward_claim` is retired: it always answers HTTP 410, code `-32060` ([rewards API](potso/rewards-api.md)).

### `potso_rewards_outflow`

Params `{"lookbackDays": N}` with `N > 0` (otherwise HTTP 400, "lookbackDays must be positive"). The handler (`rpc/potso_reward_handlers.go`, `computePotsoRewardsOutflow`) reports two adjacent windows of `RewardEpochMeta.TotalPaid` sums, using the node's wall-clock UTC date as "today". It walks epochs backwards from the latest processed one and assigns each epoch by its `Day` label: `daysAgo = today - Day` in whole days. `daysAgo < N` goes to the latest window, `N <= daysAgo < 2N` to the previous window. The walk stops at the first epoch older than that, at epoch 0, or after 2,000,000 epochs (`potsoRewardsOutflowMaxEpochsScanned`). An epoch whose meta is missing or whose `Day` does not parse is skipped but still counted in `epochsScanned`.

Result:

| Field | Meaning |
| --- | --- |
| `lookbackDays` | the requested `N` |
| `latestTotalPaid` / `previousTotalPaid` | decimal wei sums for the two windows |
| `latestEpochs` / `previousEpochs` | number of epochs counted in each window |
| `latestComplete` / `previousComplete` | `false` only if the scan hit the 2,000,000-epoch limit before finishing that window; a `false` total is partial |
| `epochsScanned` | epochs visited |
| `oldestEpochScanned` / `newestEpochScanned` | epoch range visited (`newestEpochScanned` is the latest processed epoch) |
| `asOfDay` | today's UTC date, `YYYY-MM-DD` |

If no epoch has ever been processed, both totals are `"0"`, both `Complete` flags are `true` and the other counters are 0. A failure returns HTTP 500, code `-32000`, "failed to compute rewards outflow".

## Configuration

`[potso.rewards]` keys, defaults and validation are in [config.md](potso/config.md). They are read from the node TOML file at start-up.

## Operational notes

- A block that processes a backlog of epochs runs them all in that block.
- If the treasury cannot cover a payout when paying (checked after the budget was already capped to the balance), the epoch fails with `potso.ErrInsufficientTreasury` (`core/state_transition.go`). With `B = min(emission, balance)` this check cannot trigger in the same call.
- Unit tests: `native/potso/rewards_test.go`, `metrics_test.go`, `metrics_abuse_test.go`.

Implementation: [`native/potso/rewards.go`](../native/potso/rewards.go), [`native/potso/metrics.go`](../native/potso/metrics.go), [`core/state_transition.go`](../core/state_transition.go).
