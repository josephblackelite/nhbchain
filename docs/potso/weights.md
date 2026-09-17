# POTSO Composite Weighting

This document specifies the engagement weighting pipeline used by POTSO epoch
distributions. The process blends bonded stake with a decayed engagement EMA,
allowing governance to tune responsiveness while preserving determinism.

## Inputs

For each candidate address `i` in epoch `E` we collect:

- `stake_i`: bonded stake in wei at the epoch boundary.
- `tx_i`, `escrow_i`, `uptime_i`: per-epoch activity counters sourced from the
  POTSO meters.
- `EMA_{E-1,i}`: the engagement score stored for the previous epoch.

Configuration parameters (all integer values) are provided via
`[potso.weights]`:

- `AlphaStakeBps`: stake blend factor in basis points (0–10_000).
- `TxWeightBps`, `EscrowWeightBps`, `UptimeWeightBps`: component multipliers
  applied to each counter.
- `MaxEngagementPerEpoch`: upper bound applied after EMA evaluation.
- `MinStakeToWinWei`, `MinEngagementToWin`: eligibility thresholds.
- `DecayHalfLifeEpochs`: EMA half-life measured in epochs.
- `TopKWinners`: deterministic cut-off applied after ranking.
- `TieBreak`: tie-resolution strategy (`addrLex` or `addrHash`).

Two scaling constants are used throughout the implementation:

- `WeightBpsDenominator = 10_000` for basis point math.
- `engagementBetaScale = 1_000_000_000` (1e9) for fixed-point EMA decay.

## Step 1: Raw Composite

The raw engagement contribution aggregates counters using component weights:

```
raw_i = TxWeightBps * tx_i + EscrowWeightBps * escrow_i + UptimeWeightBps * uptime_i
```

This computation uses 128-bit integer arithmetic; any overflow is clamped to
`math.MaxUint64` before the EMA stage.

## Step 2: Exponential Moving Average

The decay coefficient `β` is derived from the configured half-life `h`:

```
β = round( 2^(-1/h) * engagementBetaScale )
```

The EMA for epoch `E` is then:

```
EMA_{E,i} = floor( (EMA_{E-1,i} * β + raw_i * (engagementBetaScale - β)) / engagementBetaScale )
```

Special cases:

- If `h = 0`, then `β = 0` and the EMA equals the raw composite.
- If `β >= engagementBetaScale`, only the historical component is retained.

The final value is clamped to `MaxEngagementPerEpoch` to avoid runaway scores.

## Step 3: Eligibility Filters

Participants are removed unless both conditions hold:

1. `stake_i ≥ MinStakeToWinWei`
2. `EMA_{E,i} ≥ MinEngagementToWin`

Filtered entries are excluded from totals and never appear on the leaderboard.

## Step 4: Normalisation

Let `S = Σ stake_i` and `G = Σ EMA_{E,i}` over the remaining participants. The
stake and engagement shares are computed as rationals:

```
stakeShare_i = stake_i / S         (if S > 0)
engShare_i   = EMA_{E,i} / G       (if G > 0)
```

The composite weight is then:

```
w_i = α * stakeShare_i + (1 - α) * engShare_i
```

where `α = AlphaStakeBps / WeightBpsDenominator`. Zero denominators contribute
zero share (e.g. if all stake is zero, only engagement drives the result).
Basis-point projections used by RPCs are computed as
`floor(share * WeightBpsDenominator)`.

## Step 5: Ranking, Top-K, Tie Break

Participants are sorted by `w_i` descending. Equal weights are broken using the
configured strategy:

- `addrLex` compares raw 20-byte addresses lexicographically.
- `addrHash` hashes addresses with SHA-256 and compares digests lexicographically.

After ordering, the list is truncated to the first `TopKWinners` entries when
`TopKWinners > 0`. The resulting ordering is deterministic across all nodes.

## Worked Example

Consider two participants with the following metrics:

| Address | Stake | tx | EMA<sub>E-1</sub> |
| ------- | ----- | -- | ------------------ |
| A       | 60    | 3  | 0                  |
| B       | 40    | 7  | 0                  |

Configuration:

```
AlphaStakeBps = 7000
TxWeightBps   = 10000
DecayHalfLifeEpochs = 0
```

The raw composites are `30_000` and `70_000`. Because the half-life is zero the
EMA equals the raw value. Totals: `S = 100`, `G = 100_000`. Shares:

```
stakeShare_A = 0.60      engShare_A = 0.30
stakeShare_B = 0.40      engShare_B = 0.70
```

Composite weights:

```
w_A = 0.7*0.60 + 0.3*0.30 = 0.51
w_B = 0.7*0.40 + 0.3*0.70 = 0.49
```

With a 1,000 wei budget, payouts are ⌊1,000 * 0.51⌋ = 510 wei for A and 490 wei
for B—matching the historical expectations but now derived via the multi-factor
pipeline.

## Integer Safety

All arithmetic is performed using Go's `math/big` and 64-bit integer types.
Fixed-point operations rely on `engagementBetaScale` to avoid floating point
rounding. The SHA-256 based tie break ensures that identical weights yield the
same ordering on every node.

## Persistence

Computed snapshots are stored as `potso.StoredWeightSnapshot` records containing
raw stake/engagement figures, basis-point projections, and the deterministic
ordering. Auditors can reconstruct the full pipeline by re-running
`ComputeWeightSnapshot` using the stored data and published parameters.

