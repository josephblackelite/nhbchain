# POTSO Operations

Runtime invariants and controls for validators operating POTSO-enabled nodes. See [abuse controls](abuse-controls.md) for the weighting guardrails.

## Emission safety

`RewardConfig.Enabled` is true only when `EpochLengthBlocks > 0` and `EmissionPerEpoch > 0`. `RewardConfig.Validate` rejects a config with `EpochLengthBlocks > 0` and a non-positive `EmissionPerEpoch`, and rejects an enabled config with a zero `TreasuryAddress`. The node panics at start-up on a rejected config (`cmd/nhb/main.go`). Values are documented in [config.md](config.md).

## Heartbeat rate limit and metrics

`native/potso/engine.go` implements a per-address, per-epoch heartbeat limit (`EngineParams.MaxHeartbeatsPerEpoch`, default 1440) and the Prometheus series `potso_heartbeat_total`, `potso_heartbeat_rate_limited_total`, `potso_heartbeat_unique_peers`, `potso_heartbeat_avg_session_seconds` and `potso_heartbeat_wash_total` (`observability/metrics/potso.go`). The engine is used only by `Node.PotsoHeartbeat`, which no running code path calls because the `potso_heartbeat` RPC is disabled (see [README](README.md)). These series are registered but stay at zero. There is no caller of `Node.SetPotsoEngineParams`, so the limit is not configurable from config or governance.

The six other POTSO metrics registered in the same file (`potso_evidence_accepted_total`, `potso_penalty_applied_total`, `potso_epoch_pool`, `potso_rewards_sum`, `potso_rounding_dust`, `potso_webhook_failures_total`) have no callers of their setter methods either. See [emissions.md](emissions.md).

## Pause switch

The POTSO module honours the `potso` entry of the `system/pauses` parameter and the `[global.Pauses] POTSO` config value (`nativecommon.Guard(..., "potso")`). The node enforces one in-memory flag per module (`Node.IsPaused`). It is set from `[global.Pauses]` at start-up (`cmd/nhb/main.go:162`, `Node.SetModulePauses`) and reloaded from the on-chain `system/pauses` value by `refreshModulePauses` in the block validate/commit/create paths, so once blocks are processed the on-chain value is what is enforced.

The guard blocks the POTSO stake transactions, evidence submission (RPC and transaction), reward claims and `Node.PotsoHeartbeat`. It does not stop reward epoch processing in `processPotsoRewardEpoch`.

Start-up caveat: `cmd/nhb/main.go` applies `[global.Pauses]` (line 162) before it applies the POTSO reward and weight configuration (`Node.SetPotsoRewardConfig`, `Node.SetPotsoWeightConfig`, lines 240 to 248). Both setters begin with `nativecommon.Guard(n, modulePotso)` (`core/node.go:4929` and `core/node.go:4958`). With `POTSO = true` in `[global.Pauses]` the guard returns "module paused" and the node panics at start-up with "Failed to apply POTSO rewards config: module paused". Leave `POTSO = false` in the config file and pause the module through the on-chain parameter instead.

Read the live state and stage a change with the example programs under `examples/docs/ops/`:

```bash
go run ./examples/docs/ops/read_pauses [--db ./nhb-data] [--consensus localhost:9090]
go run ./examples/docs/ops/pause_toggle --authority <governance authority address> \
    --module potso --state pause [--db ./nhb-data] [--consensus localhost:9090] [--governance localhost:50061]
```

`pause_toggle` requires `--authority` and `--module`, and broadcasts a set-pauses message to the governance service endpoint.

## Transaction quota

`applyQuota("potso", ...)` counts the following per sender against `[global.Quotas.POTSO]` (`MaxRequestsPerMin`, `EpochSeconds`): `TxTypeStake`, `TxTypeUnstake`, `TxTypeStakeClaim`, `TxTypeStakeClaimRewards`, `TxTypeHeartbeat` and the three POTSO stake transaction types (`core/state_transition.go`, `handleNativeTransaction`).

## Reward concentration

`MaxUserShareBps` caps a winner's share of an epoch budget. Clipping is silent: the code emits no event and returns no error when a cap applies, and there is no `potso.reward.capped` event or `capUsage` metric for POTSO rewards. The effect is visible as `remainder` in `potso_epoch_info` and the `potso.reward.epoch` event.
