# POTSO Epoch Rewards (ZNHB)

This document describes how the POTSO rewards module distributes epoch-based ZNHB incentives using a hybrid of staked balance and engagement activity. It is intended for node operators, governance participants, and auditors who require both technical and compliance-oriented context.

## Overview

- **Objective:** Periodically distribute ZNHB from a pre-funded treasury pool to active participants.
- **Cadence:** Rewards are processed once per POTSO epoch. Epoch length is configurable via `potso.rewards.EpochLengthBlocks` in `config.toml`.
- **Inputs:**
  - Bonded ZNHB stake snapshots sourced from the POTSO staking subsystem.
  - Engagement meters representing user activity (transactions, escrow touchpoints, uptime) recorded during the epoch. These
    counters feed the composite weighting pipeline described in [`weights.md`](potso/weights.md).
- **Budget:** Rewards are paid entirely from the treasury address configured in `potso.rewards.TreasuryAddress`. No new ZNHB is minted. If the treasury balance is below the configured emission for an epoch, payouts are scaled down pro-rata.

## Weighting Model

Each participant’s payout weight combines stake share and the composite
engagement share produced by the [POTSO weighting pipeline](potso/weights.md):

```
w_i = α * stakeShare_i + (1 − α) * engagementShare_i
```

Where:

- `α` is configured by `AlphaStakeBps` (basis points, default `7000` → 70% stake / 30% engagement).
- `stakeShare_i` is the participant’s bonded stake divided by the total bonded stake captured at epoch close.
- `engagementShare_i` is the participant’s decayed engagement score (after filters and caps) divided by the total engagement for the epoch.

If either the total stake or engagement denominator is zero, that component contributes zero weight.

Participants are ranked by composite weight. The highest weights receive rewards up to the minimum of
`MaxWinnersPerEpoch` (from `[potso.rewards]`) and `TopKWinners` (from `[potso.weights]`) to protect block processing time.

## Payout Calculation

1. Compute the epoch budget `B_E = min(TreasuryBalance, EmissionPerEpoch)`.
2. For each selected participant, calculate `payout_i = floor(B_E * w_i)`.
3. Drop payouts below `MinPayoutWei` (default `1e15` wei = 0.001 ZNHB). Removed amounts remain in the treasury and may be re-distributed in future epochs when `CarryRemainder` is `true`.
4. Sum payouts and transfer ZNHB from the treasury account to each winner.
5. Persist the distribution and emit events:
   - `potso.reward.paid` per winner.
   - `potso.reward.epoch` summarizing totals for the epoch.

## State Persistence

The following keys are stored in the application state trie:

- `potso/rewards/lastProcessed` → latest epoch index processed.
- `potso/rewards/epoch/<E>/meta` → serialized [`RewardEpochMeta`](../native/potso/rewards.go) structure.
- `potso/rewards/epoch/<E>/winners` → ordered list of winner addresses.
- `potso/rewards/epoch/<E>/payout/<address>` → payout amount per winner.

Snapshots are idempotent; re-processing an already finalised epoch has no effect.

## RPC Endpoints

Two read-only JSON-RPC methods expose epoch results:

- `potso_epoch_info` (optional `{"epoch": <number>}` param): returns totals, configuration parameters, and winner count for the requested epoch. Defaults to the latest processed epoch.
- `potso_epoch_payouts` (`{"epoch": <number>, "cursor": "nhb1...", "limit": N}`): paginated list of winners and payout amounts. Cursor is an optional Bech32 address; pagination defaults to 50 entries.

These endpoints do not require authentication and never reveal internal state beyond persisted results.

## Configuration Parameters

All reward controls reside under `[potso.rewards]` in `config.toml`:

| Key | Description |
| --- | --- |
| `EpochLengthBlocks` | Number of blocks per rewards epoch. Set to `0` to disable payouts. |
| `AlphaStakeBps` | Stake weighting factor in basis points (0–10000). |
| `EmissionPerEpoch` | Maximum wei budget per epoch. Combined with treasury balance to produce the spend ceiling. |
| `TreasuryAddress` | Bech32 NHB address supplying reward funds. Must be pre-funded. |
| `MinPayoutWei` | Dust floor; payouts below this amount are skipped. |
| `MaxWinnersPerEpoch` | Hard cap on winners stored per epoch. |
| `CarryRemainder` | When `true`, unallocated amounts remain in the treasury for future epochs. |

Changes take effect when the node reloads configuration (e.g., at start-up).

## Compliance & Governance Considerations

- **Treasury Funding:** Governance is responsible for ensuring the configured treasury address holds sufficient ZNHB. Under-funding directly reduces payouts; no implicit minting occurs.
- **Audit Trail:** Persisted metadata, payout lists, and emitted events provide a verifiable trail for auditors. Stored values can be retrieved via RPC or by inspecting the state trie.
- **Parameter Updates:** Adjustments to weighting or emission should follow existing governance procedures. Parameter changes influence future epochs only; historical payouts remain immutable.
- **Participant Privacy:** Addresses and payout amounts are public, aligning with on-chain transparency requirements. No additional personally identifiable information is stored.
- **Regulatory Reporting:** Reward distributions may be subject to local taxation or incentive reporting rules. Operators should monitor aggregate payouts (`totalPaid`) per epoch as an input into compliance workflows.

## Operational Notes

- Epoch processing occurs automatically during block execution. If the node falls behind, missed epochs are processed sequentially on the next block, maintaining deterministic results.
- Reward logic avoids double payouts through idempotent state checks (`rewards/epoch/<E>/meta`).
- Unit tests (`native/potso/rewards_test.go`) cover weighting, dust filtering, alpha extremes, and winner capping. Integration coverage (`core/potso_rewards_integration_test.go`) simulates state persistence, balance transfers, and event emission.
- Always run `go test ./...` after modifying reward logic to ensure determinism and regression safety.

For deeper technical reference see the implementation in [`native/potso/rewards.go`](../native/potso/rewards.go) and state processing in [`core/state_transition.go`](../core/state_transition.go).
