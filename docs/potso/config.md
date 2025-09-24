# POTSO Weight Configuration

Composite weighting is controlled via the `[potso.weights]` section in
`config.toml`. These parameters are evaluated at node start-up and exposed via
`potso_params` for observability.

| Key | Description | Default |
| --- | --- | --- |
| `AlphaStakeBps` | Stake share in basis points (0–10_000). | `7000` |
| `TxWeightBps` | Transaction counter multiplier. | `6000` |
| `EscrowWeightBps` | Escrow touchpoint multiplier. | `3000` |
| `UptimeWeightBps` | Uptime/heartbeat multiplier. | `1000` |
| `MaxEngagementPerEpoch` | Hard ceiling applied after EMA. | `1000` |
| `MinStakeToWinWei` | Minimum bonded stake required to qualify. | `"0"` |
| `MinEngagementToWin` | Minimum EMA required to qualify. | `0` |
| `DecayHalfLifeEpochs` | EMA half-life in epochs. | `7` |
| `TopKWinners` | Maximum leaderboard length. `0` disables the cap. | `5000` |
| `TieBreak` | Either `addrLex` or `addrHash`. | `addrHash` |

## Tuning Guidance

- **Alpha vs component weights:** `AlphaStakeBps` controls the blend between
  stake and engagement. Lowering the value shifts rewards toward recent
  activity. The component multipliers (`TxWeightBps`, `EscrowWeightBps`,
  `UptimeWeightBps`) describe how engagement is accumulated before the EMA.

- **Decay:** `DecayHalfLifeEpochs` moderates how quickly engagement responds to
  new activity. A smaller half-life increases reactivity; a larger value rewards
  sustained participation. Setting it to `0` makes the EMA equal the raw
  composite for the current epoch.

- **Eligibility filters:** Use `MinStakeToWinWei` and `MinEngagementToWin` to
  exclude dust accounts or low-effort participants. Accounts failing either
  threshold are removed before totals are computed.

- **Top-K:** `TopKWinners` trims the leaderboard after weights are computed.
  Combined with `MaxWinnersPerEpoch` in `[potso.rewards]` this bounds storage and
  payout loops.

- **Tie breaking:** `addrLex` provides human-readable ordering (byte ascending).
  `addrHash` offers pseudo-random yet deterministic ordering, reducing the
  ability to game equal-weight scenarios.

## Operational Notes

- Changes take effect when the node reloads its configuration.
- Weight parameters do not retroactively modify historical epochs, but future
  distributions and the leaderboard immediately reflect new values.
- The configuration is validated on start-up; invalid combinations (negative
  thresholds, out-of-range basis points) abort boot.

