# POTSO Abuse Controls

The reward pipeline has three configurable guardrails, all in the `[potso.abuse]` TOML section ([config.md](config.md)). The formulas are in [spec.md](spec.md).

## Parameters

| Parameter | Effect |
|-----------|--------|
| `MinStakeToEarnWei` | If a participant's stake is below this value, its raw engagement is 0 and its decayed engagement is forced to 0 for the epoch. Its stake still counts toward the stake share (unless other filters remove it). |
| `QuadraticTxDampenAfter` / `QuadraticTxDampenPower` | Above `QuadraticTxDampenAfter` transactions in an epoch, only `round(excess^(1/power))` (at least 1) of the excess counts. `QuadraticTxDampenAfter = 0` disables it; the curve applies only for power `> 1`. Power `2` is a square root. Power must be `<= 1000`. |
| `MaxUserShareBps` | Each winner's payout is capped at `MaxUserShareBps / 10000` of the epoch budget. The clipped amount is redistributed to winners with headroom, proportionally to weight; whatever cannot be placed stays in the epoch remainder. `0` disables the cap. |

Also relevant: `MinStakeToWinWei` and `MinEngagementToWin` in `[potso.weights]` remove participants from the snapshot entirely, and `MaxEngagementPerEpoch` caps each participant's engagement.

## Test coverage

- `TestMinStakeToEarnZerosEngagement`, `TestZeroValueParticipantDropped`, `TestQuadraticTxDampening` in `native/potso/metrics_abuse_test.go`.
- `TestComputeRewardsMaxUserShareRedistribution` and `TestComputeRewardsMaxUserShareAllClipped` in `native/potso/rewards_test.go`. The second uses `MaxUserShareBps = 1` with a budget of 500, so the cap resolves to 0 wei, nobody is paid and the whole budget is the remainder.
- `TestWeightParamsValidateRejectsUnsafeDecayHalfLife` and `TestWeightParamsValidateRejectsUnsafeQuadraticTxDampenPower` in `native/potso/metrics_test.go` cover the upper bounds on the half-life and dampening power.

Run them with `go test ./native/potso/...`.

## Observability

The applied values of `MinStakeToWinWei` and the weight parameters are returned by `potso_params`. `MinStakeToEarnWei`, the dampening parameters and `MaxUserShareBps` are not part of that result. The effect of `MaxUserShareBps` shows up as `remainder` in `potso_epoch_info`. No POTSO metric or event reports when a control activates. See [emissions.md](emissions.md).
