# POTSO Emissions, Metrics and Dashboards

## Emission configuration

The per-epoch reward pool is one fixed value: `EmissionPerEpoch` under `[potso.rewards]` in the node TOML file, a decimal wei string (for example `"1000000000000000000"` for 1 ZNHB). There is no decay schedule, epoch-window configuration or change log in the code. The actual budget of an epoch is `min(EmissionPerEpoch, treasury balance)`; see [potso_rewards.md](../potso_rewards.md).

To change it, edit the file and restart the node (`cmd/nhb/main.go` reads it once). The governance key `potso.rewards.EmissionPerEpochWei` is accepted by `param.update` validation but nothing reads it, so it has no effect ([config.md](config.md)). The value feeds block execution, so it is part of the node's consensus-relevant configuration.

Where to see the emission that was actually used: `potso_epoch_info` returns `emission`, `budget`, `totalPaid` and `remainder` per epoch, and each epoch emits `potso.reward.epoch` ([notifications.md](notifications.md)).

## Prometheus metrics defined by the node

`observability/metrics/potso.go` registers these series when `metrics.Potso()` is first called (at node construction, via `potso.NewEngine`):

| Metric | Labels |
| --- | --- |
| `potso_evidence_accepted_total` | `type` |
| `potso_penalty_applied_total` | `type` |
| `potso_epoch_pool` | none |
| `potso_rewards_sum` | `epoch` |
| `potso_webhook_failures_total` | `destination` |
| `potso_rounding_dust` | `epoch` |
| `potso_heartbeat_total` | `epoch`, `address` |
| `potso_heartbeat_rate_limited_total` | `epoch`, `address` |
| `potso_heartbeat_unique_peers` | `epoch` |
| `potso_heartbeat_avg_session_seconds` | `epoch` |
| `potso_heartbeat_wash_total` | `epoch`, `address` |

No code calls the setters of the first six (`ObserveEvidenceAccepted`, `ObservePenaltyApplied`, `SetEpochPool`, `ObserveRewardsSum`, `ObserveRoundingDust`, `IncWebhookFailure`, or the `Init*` helpers), so they stay at zero or absent. The heartbeat series are updated only by `Node.PotsoHeartbeat`, which has no live caller ([operations.md](operations.md)). In practice none of these series change on a running node. There is no `potso_min_stake_gated_total` or `potso_rewards` metric in the code.

## Dashboards and alert rules in the repository

- Grafana dashboards: `observability/grafana/dashboards/potso-overview.json`, `potso-emissions-and-caps.json`, `potso-rewards-pipeline.json`.
- Alert rules: the `potso-alerts` group in `observability/alerts/alert_rules.yaml` (`POTSOEvidenceSpike`, `POTSOEvidenceDelta`, `POTSOFailedWebhookDelivery`, `POTSOIdempotencyConflicts`, `POTSOEmissionCapApproach`, `POTSORoundingDustExceedsThreshold`, `POTSOCapInvariantBreach`).

Their expressions use the metrics listed as never updated (for example `potso_evidence_accepted_total`, `potso_epoch_pool`, `potso_rewards_sum`), so those panels stay empty and those alerts cannot fire on data from a node.

## What can be monitored today

- `potso_params` for the running weight parameters ([leaderboard.md](leaderboard.md)).
- `potso_epoch_info` / `potso_leaderboard` / `potso_rewards_outflow` for per-epoch results ([potso_rewards.md](../potso_rewards.md)).
- Events listed in [notifications.md](notifications.md) and [evidence-and-penalties.md](evidence-and-penalties.md).
