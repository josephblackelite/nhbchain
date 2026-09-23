# POTSO Composite Weighting

This page describes the pipeline implemented by `ComputeWeightSnapshot` in `native/potso/metrics.go`. It runs once per reward epoch from `processPotsoRewardEpoch` (`core/state_transition.go`). The result is stored and used for reward payouts, the leaderboard, and governance voting power.

## Inputs

For each candidate address `i` in epoch `E`, `processPotsoRewardEpoch` builds:

- `stake_i` (wei): the sum of
  - the bonded total from POTSO stake locks (`potso/stake/<owner>`, see [stake.md](stake.md)); and
  - for addresses in `EligibleValidators`, the eligibility basis stored there (added on top of the lock total). That basis is the account's total `Stake`, which includes ZNHB delegated in by third parties (`validatorEligibilityBasis`, `core/state_transition.go`); see [consensus integration](consensus-integration.md#voting-power-quorums).
- `tx_i`, `escrow_i`: per-epoch counters accumulated as transactions are applied (`PotsoMetricsAddEngagement`, keyed by `height / EpochLengthBlocks`).
- `uptime_i`: `UptimeSeconds / 60` for the epoch (whole minutes). Only `Node.PotsoHeartbeat` writes uptime and nothing in the running node calls it (see [README](README.md)), so this is 0 in practice.
- `EMA_{E-1,i}`: the engagement stored in the previous epoch's snapshot (0 if none).

The candidate set is every address with stake, every address with an epoch meter, and every address in the previous snapshot.

Parameters come from `[potso.weights]` and `[potso.abuse]` in the node's TOML config, see [config.md](config.md):

- `AlphaStakeBps`, `TxWeightBps`, `EscrowWeightBps`, `UptimeWeightBps`
- `MaxEngagementPerEpoch`, `MinStakeToWinWei`, `MinStakeToEarnWei`, `MinEngagementToWin`
- `DecayHalfLifeEpochs`, `TopKWinners`, `TieBreak`
- `QuadraticTxDampenAfter`, `QuadraticTxDampenPower`

Constants: `WeightBpsDenominator = 10000` and `engagementBetaScale = 1_000_000_000` (fixed-point scale for the EMA).

## Step 1: Raw composite

```
raw_i = TxWeightBps * tx_i' + EscrowWeightBps * escrow_i + UptimeWeightBps * uptime_i
```

`tx_i'` is `tx_i` after the optional quadratic dampening described in [spec.md](spec.md). If `stake_i < MinStakeToEarnWei`, `raw_i` is 0. The sum uses `math/big`; a result wider than 64 bits is clamped to `math.MaxUint64`.

## Step 2: Exponential moving average

The decay coefficient for half-life `h = DecayHalfLifeEpochs` is

```
β = round( 2^(-1/h) * engagementBetaScale )     (0 when h = 0)
```

computed with exact integer arithmetic (`computeBetaScaled`). The EMA is

```
EMA_{E,i} = floor( (EMA_{E-1,i} * β + raw_i * (engagementBetaScale - β)) / engagementBetaScale )
```

- If `h = 0`, `β = 0` and the EMA equals `raw_i`.
- If `β >= engagementBetaScale`, the previous value is kept unchanged.
- If `stake_i < MinStakeToEarnWei`, the result is forced to 0.
- If `MaxEngagementPerEpoch > 0`, the result is clamped to it. The clamped value is what is stored and used as `EMA_{E-1}` next epoch.

`DecayHalfLifeEpochs` must be `<= 100000` and `QuadraticTxDampenPower` must be `<= 1000` (`WeightParams.Validate`).

## Step 3: Eligibility filters

A participant is removed if any of these holds (checked in this order after step 2):

1. `stake_i < MinStakeToWinWei`
2. `EMA_{E,i} < MinEngagementToWin`
3. `stake_i = 0` and `EMA_{E,i} = 0`

Removed participants are excluded from the totals and from the snapshot.

## Step 4: Normalisation

With `S = Σ stake_i` and `G = Σ EMA_{E,i}` over the remaining participants:

```
stakeShare_i = stake_i / S        (0 if S = 0 or stake_i = 0)
engShare_i   = EMA_{E,i} / G      (0 if G = 0 or EMA_{E,i} = 0)
w_i          = α * stakeShare_i + (1 - α) * engShare_i,   α = AlphaStakeBps / 10000
```

Shares are exact rationals (`big.Rat`). The basis-point figures stored and returned by RPC are `floor(share * 10000)`.

## Step 5: Ranking, Top-K, tie break

Entries are sorted by `w_i` descending. Equal weights are ordered by the tie-break key, ascending:

- `addrHash`: SHA-256 digest of the 20-byte address.
- `addrLex`: the raw 20-byte address. An empty `TieBreak` value behaves as `addrLex` in `tieBreakKey`; the config loader sets `addrHash` when the key is omitted.

The list is then truncated to `TopKWinners` entries when `TopKWinners > 0`.

## Worked example

Two participants, no dampening, and `DecayHalfLifeEpochs = 0`:

| Address | Stake | tx | EMA<sub>E-1</sub> |
| ------- | ----- | -- | ------------------ |
| A       | 60    | 3  | 0                  |
| B       | 40    | 7  | 0                  |

Configuration (only the relevant keys):

```
AlphaStakeBps         = 7000
TxWeightBps           = 10000
MaxEngagementPerEpoch = 100000   # with the default 1000 both raw values would be clamped to 1000
```

Raw composites are `30_000` and `70_000`, and with `h = 0` the EMA equals them. Totals: `S = 100`, `G = 100_000`.

```
stakeShare_A = 0.60   engShare_A = 0.30    w_A = 0.7*0.60 + 0.3*0.30 = 0.51
stakeShare_B = 0.40   engShare_B = 0.70    w_B = 0.7*0.40 + 0.3*0.70 = 0.49
```

With a budget of 1,000 wei the base payouts are `floor(1000 * 0.51) = 510` for A and `490` for B.

(Through the TOML loader `DecayHalfLifeEpochs = 0` is replaced by the default 7, see [config.md](config.md), so this example applies to direct library calls.)

## Persistence

`processPotsoRewardEpoch` stores the ranked entries as a `potso.StoredWeightSnapshot` (epoch, total stake, total engagement, and per entry: address, stake, engagement, `StakeShareBps`, `EngagementShareBps`, `WeightBps`) under two keys with identical content:

- `potso/metrics/snapshot/<epoch>` - read by `potso_leaderboard` and used as the previous-epoch engagement input.
- `snapshots/potso/<epoch>/weights` - read by governance voting (`native/governance`) and by `potso_getWeight`.

Trie keys: `snapshots/potso/<epoch>/weights` is written with `KVPut` on the plain string (`SnapshotPotsoWeightsKey`), so it is stored under `Keccak256(key)`. `potso/metrics/snapshot/<epoch>` (and the `potso/metrics/meter/<epoch>:<addr>` and `potso/metrics/index/<epoch>` records) go through key helpers that already return `Keccak256(prefix + id)` (`potsoMetricsSnapshotKey` and siblings in `core/state/manager.go`), so `KVPut` hashes them a second time: `Keccak256(Keccak256(key))`.

The snapshot is written only when `ComputeRewards` computed weights. It returns before that when the epoch budget is zero or negative (for example an empty treasury), so no snapshot is stored for such an epoch.
