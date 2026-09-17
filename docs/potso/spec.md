# POTSO Weighting and Abuse Controls

This specification outlines the deterministic math used by the POTSO weighting
pipeline and the additional anti-abuse controls introduced for the rewards
module. It supplements the high level overview in `weights.md` with exact
formulas that implementers can reproduce in analytics or off-chain validation
systems.

## Eligibility Gates

Each participant `i` is described by a bonded stake `s_i`, a raw engagement
meter `(tx_i, escrow_i, uptime_i)`, and the exponentially decayed engagement
value from the prior epoch `e_{i,t-1}`. The following thresholds are applied
before weights are computed:

1. **Minimum stake to win**: If `s_i < MinStakeToWinWei` the participant is
   removed from the candidate set entirely.
2. **Minimum stake to earn**: If `s_i < MinStakeToEarnWei` the participant stays
   in the candidate set (so their bonded stake can still contribute to the
   staking share) but their raw composite engagement is forced to zero. This
   immediately drives the decayed engagement `e_{i,t}` to zero as well.
3. **Zero-value filter**: Addresses with `s_i = 0` *and* `e_{i,t} = 0` are
   removed to prevent zero-weight entries from polluting snapshots or tie-breaks.

The new `MinStakeToEarnWei` guard ensures large botnets cannot harvest
engagement while staking only dust amounts. Once the account posts the minimum
stake, engagement accrues normally on the next epoch.

## Engagement Composite and Dampening

Raw engagement prior to decay is defined as a weighted sum of the meter inputs:

```
raw_i = tx_i * TxWeightBps + escrow_i * EscrowWeightBps + uptime_i * UptimeWeightBps
```

To reduce the marginal value of spammed transactions we apply quadratic
suppression after a configured knee point. Let `T_after` be
`QuadraticTxDampenAfter` and `p` be `QuadraticTxDampenPower`.

```
if tx_i > T_after and p > 1:
    excess = tx_i - T_after
    dampened_excess = round(excess^(1/p))
    dampened_tx = T_after + max(1, dampened_excess)
else:
    dampened_tx = tx_i
```

The `dampened_tx` value replaces `tx_i` inside `raw_i`. Large spikes in
transaction count therefore contribute proportionally less once the knee point
is exceeded while still rewarding moderate growth. Setting `QuadraticTxDampenAfter`
to zero disables the curve, and `QuadraticTxDampenPower = 2` yields a square-root
response.

The exponentially weighted moving average from the previous epoch is applied as
before using the configured half-life. When `MinStakeToEarnWei` suppresses the
raw composite, the post-EMA engagement is pinned to zero.

## Composite Weight

Stake and engagement shares are combined with the familiar convex blend:

```
alpha = AlphaStakeBps / WeightBpsDenominator
stake_share_i = s_i / Σ s_j
engagement_share_i = e_{i,t} / Σ e_{j,t}
weight_i = alpha * stake_share_i + (1 - alpha) * engagement_share_i
```

The tie-breaker semantics described in `weights.md` remain unchanged.

## Reward Share Cap

During reward settlement each candidate’s ideal payout is `weight_i * budget`.
`MaxUserShareBps` introduces a hard ceiling on the proportion of the epoch
budget that any single winner can receive:

```
max_share = MaxUserShareBps / RewardBpsDenominator
cap_i = max_share * budget
amount_i = min(weight_i * budget, cap_i)
```

If clipping occurs the excess budget is redistributed deterministically across
participants that still have headroom. Redistribution is proportional to the
original weights within the uncapped subset and continues until either the
excess pool is exhausted or every participant has reached their cap. Any
residual amount that cannot be distributed without breaking the constraints
returns to the remainder bucket.

These mechanics keep rewards predictable while allowing governance to ratchet
caps up or down as abuse patterns emerge.
