# POTSO Weighting and Abuse Controls

This page gives the exact formulas implemented in `native/potso/metrics.go` (`ComputeWeightSnapshot`) and `native/potso/rewards.go` (`ComputeRewards`). It supplements [weights.md](weights.md), which describes the same pipeline step by step.

## Per-participant filters (in code order)

For each participant `i`, `ComputeWeightSnapshot` receives the bonded stake `s_i`, the epoch meter `(tx_i, escrow_i, uptime_i)` and the previous epoch's stored engagement `e_{i,t-1}`. It applies these steps in this order:

1. **Earning gate.** If `s_i < MinStakeToEarnWei`, the raw composite is set to 0 and, after the EMA step, `e_{i,t}` is forced to 0. The participant stays in the candidate set at this point, so its stake still counts.
2. **Engagement cap.** If `MaxEngagementPerEpoch > 0`, `e_{i,t}` is clamped to it.
3. **Win-stake filter.** If `s_i < MinStakeToWinWei`, the participant is removed.
4. **Win-engagement filter.** If `e_{i,t} < MinEngagementToWin`, the participant is removed.
5. **Zero-value filter.** If `s_i = 0` and `e_{i,t} = 0`, the participant is removed.

Removed participants do not contribute to `Σ s_j` or `Σ e_j`. `MinStakeToEarnWei` (read from `[potso.abuse]`) and `MinStakeToWinWei` (read from `[potso.weights]`) both default to 0.

## Engagement composite and dampening

```
raw_i = tx_i' * TxWeightBps + escrow_i * EscrowWeightBps + uptime_i * UptimeWeightBps
```

`tx_i'` is `tx_i` after transaction dampening. With `T = QuadraticTxDampenAfter` and `p = QuadraticTxDampenPower` (`computeComposite`):

```
if T > 0 and tx_i > T and p > 1:
    excess = tx_i - T
    dampened = round(excess^(1/p))       # exact integer root, rounded half up
    if dampened == 0: dampened = 1
    tx_i' = T + dampened                 # saturates at MaxUint64
else:
    tx_i' = tx_i
```

`QuadraticTxDampenAfter = 0` disables the curve. The sum is computed with `math/big` and clamped to `MaxUint64` if it exceeds 64 bits.

The EMA and cap are applied to `raw_i` exactly as described in [weights.md](weights.md) (step 2).

## Composite weight

```
alpha             = AlphaStakeBps / 10000
stake_share_i     = s_i / Σ s_j            (0 if the sum is 0)
engagement_share_i = e_{i,t} / Σ e_j       (0 if the sum is 0)
weight_i          = alpha * stake_share_i + (1 - alpha) * engagement_share_i
```

Entries are sorted by `weight_i` descending with exact rational comparison. Ties are broken by the tie-break key (`addrHash` = SHA-256 of the address, or `addrLex` = raw address bytes, both ascending; an empty mode behaves as `addrLex`). The list is then truncated to `TopKWinners` when that is greater than 0.

## Reward share cap and payouts

`ComputeRewards` takes the sorted entries with `weight > 0`, truncates to `MaxWinnersPerEpoch` when that is greater than 0, and then:

```
base_i = floor(weight_i * budget)
if MaxUserShareBps > 0:
    cap    = floor(MaxUserShareBps / 10000 * budget)
    amount_i = min(base_i, cap)
else:
    amount_i = base_i
```

When `MaxUserShareBps > 0`, the amount clipped from each capped winner forms a pool. The pool is distributed over winners that still have headroom, in proportion to their weights, repeating until the pool is empty or no winner has headroom. Any pool left over stays in the remainder. If the cap resolves to 0 wei (for example `MaxUserShareBps = 1` with a budget of 500), no winner is paid and the whole budget is the remainder (`TestComputeRewardsMaxUserShareAllClipped`).

Finally, amounts that are `<= 0` or below `MinPayoutWei` are dropped. `TotalPaid` is the sum of the remaining amounts and `Remainder = budget - TotalPaid`. Nothing in this code carries the remainder to a later epoch. Each epoch's budget is computed afresh (see [epoch rewards](../potso_rewards.md)).

## Where the parameters are used

`AlphaStakeBps` in the reward computation is `RewardConfig.AlphaStakeBps`, which `Config.PotsoRewardConfig` sets from `[potso.weights].AlphaStakeBps` when that is greater than 0 (`core/state_transition.go` overwrites the weight parameters' alpha with it). See [config.md](config.md).
