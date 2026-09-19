# Epoch Rewards (validators, stakers, engagement)

This page describes the per-epoch ZNHB emission that is split across validators, stakers and engagement scores. It is separate from loyalty rewards ([`loyalty.md`](./loyalty.md)). Code: `core/rewards/` (`config.go`, `halving.go`, `accumulator.go`, `types.go`), `core/rewards_logic.go`, `core/rewards_state.go`, `core/epochs.go`, `rpc/rewards_handlers.go`.

## Overview

* Each epoch has a planned emission taken from a schedule (`rewards.Config`). The emission is split into validator, staker and engagement pools by basis-point weights.
* A per-block accumulator (`accrueEpochRewards`) tracks the planned amounts, and the payout is settled when the epoch closes.
* Payouts are transfers of ZNHB. When the node has an admin wallet configured (`hasAdminWallet`), the payout is limited by the ZNHB **Reward Pool** balance and debited from the admin wallet's ZNHB balance; the pool ledger is reduced by the amount that reaches other accounts (`settleEpochRewards`). Without an admin wallet (for example in unit tests) no pool check or debit is made.
* Anything not distributed stays undistributed: settlement records planned and paid amounts separately, so unused amounts are visible.

## Configuration

`rewards.Config` fields:

| Field | Meaning |
|-------|---------|
| `Schedule []EmissionStep` | Piecewise-constant emission: each `{StartEpoch, Amount}` applies from `StartEpoch` (1-indexed, greater than zero, no duplicates) until the next step. Amounts are non-negative wei. No schedule means zero emission. |
| `ValidatorSplit`, `StakerSplit`, `EngagementSplit` | Basis points. Must sum to exactly 10000, or all be zero (disabled). |
| `HistoryLength` | Number of settlement records kept; 0 keeps all. |

`rewards.DefaultConfig()` is disabled (empty schedule, zero splits, `HistoryLength` 64). `IsEnabled` is true only with a non-empty schedule, at least one non-zero split and at least one positive step. `SplitEmission` computes `total * split / 10000` per pool and adds any rounding dust to the engagement pool.

**What the node runs.** When the node has an admin wallet, it applies `rewards.HalvingScheduleConfig(2000, 5000, 3000, 2000)` at start-up (`core/node.go`): 20% validators, 50% stakers, 30% engagement, history length 2000. The configuration is held in memory and recomputed at each start; it is not governance-adjustable (a code comment in `core/node.go` says so). The halving schedule (`core/rewards/halving.go`):

* Base emission `HalvingBaseEmissionZNHB` = 200 ZNHB per epoch in the first era.
* Era length `HalvingEraLengthEpochs` = 500,000 epochs; the emission is halved (right shift at wei precision, rounding down) each era, for at most 80 eras.

Epoch length in blocks comes from the epoch configuration (`epoch.Config.Length`; default 100).

## Accrual and settlement

1. **Per block** (`accrueEpochRewards(height)`): epoch number = `((height-1) / length) + 1`; the accumulator for that epoch is created with the planned pool amounts and accrues one block's share per block.
2. **At the epoch boundary** (`height % length == 0`, `ProcessBlockLifecycle` in `core/epochs.go`, epoch number `height / length`): `finalizeEpoch` computes the validator weight snapshot and runs `settleEpochRewards`, then buyback settlement, then applies validator selection. If a settlement for the epoch already exists, nothing is paid again.
3. **Pool limit**: with an admin wallet, if the planned total exceeds the Reward Pool balance, every pool's plan is scaled down proportionally (rounded down).
4. **Distribution**:
   * Validator pool: split equally across the unique addresses in the snapshot's selected validators, ordered by address; remainder units go to the first addresses in that order.
   * Staker pool: pro-rata by each weight entry's `Stake`. A validator's share is then split between the validator's own basis (its stake minus stake delegated in by others) and its indexed delegators, pro-rata by the delegators' `LockedZNHB`; with no indexed delegators the validator receives the whole share.
   * Engagement pool: pro-rata by engagement score.
   * Pro-rata rounding: leftover units go first to the largest remainders (ties by address), then by address order. A pool with a zero denominator pays nothing and stays unused.
5. **Credit**: each account's ZNHB balance is increased. If the account has a reward beneficiary set (`Account.RewardBeneficiary`, set with `TxTypeSetRewardBeneficiary`, `0x1A`) and it differs from the account, the credit goes to the beneficiary instead.
6. **Persist**: the settlement is appended to the history, pruned to `HistoryLength`, and stored in state under the key `keccak256("reward-history")`. `Blocks` in the record is the configured epoch length.
7. With an admin wallet the ledger and wallet updates run: Reward Pool balance reduced by the paid amount that did not go back to the admin wallet, and the admin wallet's ZNHB balance reduced by the full paid total. If that balance is below the paid total, settlement returns an error.

## Events

**`rewards.paid`** (one per account with a non-zero payout): `epoch`, `account` (bech32, the account the reward was earned by), `amount`; `paidTo` (bech32) when redirected to a beneficiary; `validators`, `stakers`, `engagement` when the portion is non-zero.

**`rewards.epoch_closed`**: `epoch`, `height`, `closed_at` (unix seconds), `blocks`, `planned_total`, `paid_total`, `validators_planned`, `validators_paid`, `stakers_planned`, `stakers_paid`, `engagement_planned`, `engagement_paid`.

## JSON-RPC

None of these methods require authentication.

**`nhb_getRewardEpoch`**: optional param: an epoch number or `{"epoch": n}`; without it the latest settlement is returned. Result fields: `epoch`, `height`, `closedAt`, `blocks`, `plannedTotal`, `paidTotal`, `validatorsPlanned`, `validatorsPaid`, `stakersPlanned`, `stakersPaid`, `engagementPlanned`, `engagementPaid`, `unusedTotal`, `unusedValidators`, `unusedStakers`, `unusedEngagement`, `payouts` (array of `{account, total, validators, stakers, engagement}` where `account` is `0x` plus hex of the credited address, that is the beneficiary when redirected). Amounts are decimal strings. Unknown epoch: HTTP 404, code `-32000`, message `reward epoch not found`.

**`nhb_getRewardPayout`**: param `{"account": "<bech32 or 0x hex>", "epoch": n (optional)}` (a bare account string also works). Result `{"epoch": n, "payout": {...}}`. Errors: `reward epoch not found` or `payout not found`, both HTTP 404 with code `-32000`. The account is matched against the credited address.

**`nhb_getRewardHistory`**: param `{"account": ...}` or a bare account string. Result `{"account": "0x...", "entries": [{epoch, height, closedAt, total, validators, stakers, engagement}]}` covering the retained history window.

## Node helpers

`Node.RewardConfig()`, `Node.SetRewardConfig(cfg)`, `Node.RewardEpochSettlement(epoch)`, `Node.LatestRewardEpochSettlement()` and `StateProcessor.SetRewardConfig` (validates, resets the accumulator, prunes history) are Go-level accessors, not RPC methods. A node that has an admin wallet resets the configuration to the halving configuration above when it starts.
