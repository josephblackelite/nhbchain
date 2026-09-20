# POTSO Weighting - Determinism and Audit Notes

## Determinism

- **Integer arithmetic.** `ComputeWeightSnapshot` uses `math/big` integers and rationals (`big.Rat`). The EMA decay factor and the dampening root are computed with exact integer routines (`computeBetaScaled`, `integerNthRootRounded` in `native/potso/metrics.go`), not floating point. The EMA uses a `1_000_000_000` fixed-point scale.
- **Ordering.** Ties in weight are broken by the SHA-256 digest of the address (`addrHash`) or by raw address bytes (`addrLex`). Candidate addresses are sorted before processing (`processPotsoRewardEpoch`).
- **Inputs.** Reward processing reads only trie state: stake locks, eligible-validator stake basis, and the epoch-keyed engagement counters (`potso/metrics/...`) recorded by block height. The block timestamp only labels the epoch's `Day`.
- **Bounds.** `DecayHalfLifeEpochs <= 100000` and `QuadraticTxDampenPower <= 1000` are enforced in `WeightParams.Validate` because both feed loops whose cost grows with the exponent.

## Stored data

Per processed epoch (see [potso_rewards.md](../potso_rewards.md#state-persistence)): the weight snapshot (epoch, total stake, total engagement, and per entry address, stake, engagement, `StakeShareBps`, `EngagementShareBps`, `WeightBps`), the epoch meta, the winners list, payouts and claim records. An epoch whose meta exists is not reprocessed.

Raw per-epoch counters (`potso/metrics/meter/<epoch>:<addr>`, stored under the twice-hashed key described in [weights](weights.md#persistence)) remain in state, so the counters of an epoch can be inspected in addition to the final engagement values.

## Reproducing an epoch

1. Read the snapshot for the epoch (`potso_leaderboard`, or the `potso/metrics/snapshot/<epoch>` key) and the epoch meta (`potso_epoch_info`).
2. Take the parameters from the node configuration in force ([config.md](config.md)). `potso_params` returns the `[potso.weights]` values only. `MinStakeToEarnWei`, `QuadraticTxDampenAfter`, `QuadraticTxDampenPower`, `MaxUserShareBps` and the `[potso.rewards]` values (`MinPayoutWei`, `MaxWinnersPerEpoch`, `PayoutMode`, `EmissionPerEpoch`, `TreasuryAddress`) are not exposed over RPC.
3. Rebuild the inputs (stake and epoch counters from state, previous epoch's stored engagement) and call `potso.ComputeWeightSnapshot(epoch, inputs, params)`, then `potso.ComputeRewards(cfg, params, snapshot, budget)` with `budget = epochMeta.budget`.
4. Compare with the stored snapshot, payouts and remainder.

The stored snapshot holds the engagement after EMA, filters and cap, so it can be checked without the epoch counters only for the weight and share steps, not for the EMA and composite steps.

## State integrity

- Reward processing is idempotent per epoch (`potso/rewards/epoch/<E>/meta`).
- The leaderboard and epoch RPCs are read-only.
- POTSO reward and weight settings are per-node configuration read directly by block execution ([config.md](config.md)); the governance parameter keys for them are not consumed.
