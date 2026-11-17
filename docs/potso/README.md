# POTSO Overview

POTSO ("Proof of Time Spent Online") is the name used in the code for a set of participation counters, a stake-lock module and an epoch reward distribution. This page covers the daily meters and the RPC methods that read them. The other parts are documented in:

- [Staking locks](stake.md) - `TxTypePotsoStakeLock` / `Unbond` / `Withdraw`.
- [Weighting](weights.md), [spec](spec.md), [config](config.md) - how epoch weights are computed.
- [Epoch rewards](../potso_rewards.md), [payout modes](rewards-modes.md), [rewards API](rewards-api.md).
- [Evidence and penalties](evidence-and-penalties.md).
- [Consensus integration](consensus-integration.md) - what POTSO does and does not feed into BFT.

## Daily meters

Every participant address has one meter per UTC day (`YYYY-MM-DD`, taken from the block timestamp). The struct is `potso.Meter` in `native/potso/meter.go`:

| Field | Description |
| --- | --- |
| `day` | The UTC day string. |
| `uptimeSeconds` | Seconds credited by `Node.PotsoHeartbeat`. See "Heartbeats" below: nothing in the running node calls it. |
| `txCount` | Incremented by 1 for each transaction that calls `recordEngagementActivity` in `core/state_transition.go` (EVM-format transfers, ZNHB transfers, and most native transaction types). The three POTSO stake transaction types do not call it. |
| `escrowEvents` | Incremented by 1 for `TxTypeCreateEscrow`, `ReleaseEscrow`, `RefundEscrow`, `LockEscrow`, `DisputeEscrow`, `ArbitrateRelease`, `ArbitrateRefund` and `TxTypeSwapBurn`. |
| `rawScore` | `floor(uptimeSeconds / 60) + txCount * 5 + escrowEvents * 10` (`ComputeRawScore`). |
| `score` | Equal to `rawScore` (`ComputeScore` returns its input unchanged). |

Meters are written from `updatePotsoActivity` (`core/state_transition.go`) when a transaction is applied. Each write also adds the address to that day's participant index, which `potso_top` reads.

These day-keyed meters are separate from the per-epoch engagement counters that feed reward weights (see [weights](weights.md)) and from the account-level `EngagementScore` used in proposer selection (see [consensus integration](consensus-integration.md)).

## Heartbeats

`native/potso/heartbeat.go` defines the heartbeat rules: at least 60 seconds (`HeartbeatIntervalSeconds`) between accepted heartbeats, timestamps within 120 seconds (`TimestampToleranceSeconds`) of the node clock, and the first heartbeat crediting 60 seconds.

`Node.PotsoHeartbeat` (`core/node.go`) is the only code that adds `uptimeSeconds`. The `potso_heartbeat` RPC method is the only caller, and it is disabled: `handlePotsoHeartbeat` (`rpc/potso_handlers.go`) always returns HTTP 503 with the message "potso heartbeat rpc is temporarily disabled; submit engagement through the canonical transaction pipeline". `nhb-cli potso heartbeat` calls that method and therefore fails.

`TxTypeHeartbeat` (`0x08`) is a different mechanism (its RPC entry point and device tokens are described in [engagement program](../overview/engagement-program.md)). `applyHeartbeat` (`core/state_transition.go`) updates the account's `EngagementMinutes` / `EngagementLastHeartbeat` fields (`core/engagement`), not the POTSO meter. `EngagementLastHeartbeat` is also what keeps a validator in the active set: `validatorReadyForActivation` (`core/epochs.go`) requires it to be non-zero and recent, so a registered validator that stops sending `TxTypeHeartbeat` transactions is dropped from the set at the next epoch boundary (unless no validator qualifies, in which case `fallbackValidatorSet` refills it). See [consensus integration](consensus-integration.md#voting-power-quorums).

## Storage keys

Records are written with `KVPut` (RLP encoded). `KVPut`/`KVGet` store a value under `Keccak256(key)` (`kvKey` in `core/state/manager.go`), but the helpers that build these three keys (`potsoHeartbeatKey`, `potsoMeterKey`, `potsoDayIndexKey`) already return `Keccak256(prefix + id)`. The trie key is therefore `Keccak256(Keccak256(prefix + id))`, hashed twice. Anyone reading the trie directly must hash twice:


- `potso/heartbeat/<addr>` - `HeartbeatState` (`LastTimestamp`, `LastBlock`, `LastHash`).
- `potso/meter/<day>:<addr>` - `Meter` for that day.
- `potso/day-index/<day>` - addresses with a meter for that day (written with `KVAppend`, which hashes the helper key once more in the same way).

`<addr>` is the raw 20-byte address and `<day>` the `YYYY-MM-DD` string.

## RPC surface

Both working methods are read-only and require exactly one parameter object in `params` (send `{}` to use the defaults of `potso_top`).

- `potso_userMeters` - params `{"user": "nhb1...", "day": "YYYY-MM-DD"}`. `user` is required; `day` defaults to the current UTC day and is not validated. Returns the `Meter` object (all zeros with the requested `day` if nothing is stored).
- `potso_top` - params `{"day": "...", "limit": N}`, both optional (`day` defaults to today UTC, `limit` defaults to 10 when `<= 0`). Returns an array of `{"user": "nhb1...", "meter": {...}}`, ordered by `score` desc, then `rawScore` desc, then `uptimeSeconds` desc, then address bytes ascending (`Node.PotsoTop`).
- `potso_heartbeat` - disabled, see above.

Other POTSO methods are documented with their subjects: `potso_leaderboard`, `potso_params`, `potso_getWeight` ([leaderboard](leaderboard.md)); `potso_stake_info` ([stake](stake.md)); `potso_epoch_info`, `potso_epoch_payouts`, `potso_rewards_history`, `potso_rewards_outflow`, `potso_export_epoch` ([rewards API](rewards-api.md), [epoch rewards](../potso_rewards.md)); `potso_getEvidence`, `potso_listEvidence` ([evidence](evidence-and-penalties.md)).

Two methods that older clients may still call are gone:

- `potso_reward_claim` is routed but always answers HTTP 410, JSON-RPC code `-32060` (`handlePotsoRewardClaim`, `rpc/potso_reward_handlers.go`; `codeMethodDisabled` in `rpc/http.go`). See [rewards API](rewards-api.md).
- `potso_submitEvidence` no longer exists; the node answers it as an unknown method (HTTP 404, code `-32601`, `default` branch of the dispatcher in `rpc/http.go`). Evidence is reported with a signed transaction, see [evidence](evidence-and-penalties.md).

Errors use the RPC codes in `rpc/http.go`: `-32602` invalid params (HTTP 400), `-32000` server error, `-32001` unauthorized (HTTP 401), `-32060` method disabled (HTTP 410), `-32601` unknown method (HTTP 404).

## CLI

```bash
nhb-cli potso user-meters --user nhb1... [--day 2025-09-24]
nhb-cli potso top [--day 2025-09-24] [--limit 10]
```

`nhb-cli potso heartbeat` calls `potso_heartbeat` and therefore fails (exit code 1). `nhb-cli potso reward claim` is retired: it prints an error and exits 1 without contacting the node (`reportRetired`, `cmd/nhb-cli/retired_cmd.go`). `stake` and `reward history` / `reward export` are described in [stake](stake.md) and [rewards API](rewards-api.md). Commands that need a bearer token read it from `NHB_RPC_TOKEN`; `nhb-cli rpc-token` prints a short-lived token signed with `NHB_RPC_JWT_SECRET` (run it on the node host; `cmd/nhb-cli/rpc_token.go`).
