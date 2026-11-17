# POS SLA & Troubleshooting Runbook

This runbook lists the POS signals the node exports and how to use them when POS traffic is
slow or being rejected. The code defines no numeric SLO targets for POS; targets are yours to
set on the metrics below. The lane design is in [POS quality of service](../specs/pos-qos.md).

## Signals

Prometheus metrics registered by the node (`observability/metrics.go`). They are served on
`/metrics` by `nhb` and `consensusd` only when `NHB_METRICS_ADDR` is set (see the
[alert runbook](./alerts.md#consensus-liveness-alerts)).

| Metric | Type | Meaning |
| --- | --- | --- |
| `nhb_mempool_pos_p95_finality_ms` | histogram | Milliseconds from enqueue to finality for POS-tagged transactions (buckets 50 ms to 12,800 ms). Despite the name it is a histogram; compute the percentile with `histogram_quantile`. |
| `nhb_mempool_pos_lane_fill` | gauge | Fill ratio of the POS-reserved transaction lane. |
| `nhb_mempool_pos_lane_backlog` | gauge, label `asset` | POS-tagged transfers waiting, by asset. |
| `nhb_mempool_pos_tx_enqueued_total` | counter | POS-tagged transactions admitted to the mempool. |
| `nhb_pos_auth_expired_total` | counter | POS authorizations voided automatically after expiry. |
| `nhb_consensus_block_interval_seconds` | gauge | Interval between the timestamps of consecutive committed blocks. |

The gateway has no POS rejection metric (there is no `pos_gateway_rejections_total` in the code).

Events: `pos.auth_auto_voided` (expiry sweep), `tx.sponsorship.failed` and
`paymaster.throttled` (sponsorship rejected; `paymaster.throttled` is emitted only when a daily cap was hit, not for a paused merchant or revoked device; see
[Paymaster administration](../launch/paymaster-admin.md)).

Expired authorizations are voided by every block as part of its own execution
(`StateProcessor.FinalizeBlock`, `core/state_transition.go`). The RPC method `pos_sweepVoids` and the
command `nhb-cli pos sweep-voids` are retired: the method answers HTTP 410 with code `-32060`.

Realtime feed: the node serves the WebSocket `/ws/pos/finality` (`rpc/http.go`) and the gRPC
method `pos.v1.Realtime/SubscribeFinality`. Both stream `pending` and `finalized` updates for
POS intents; the schema is in [POS realtime](../api/pos-realtime.md).

The general request error and latency alerts are in the [alert runbook](./alerts.md).

## Tuning

1. **POS lane reservation.** `[global.Mempool] POSReservationBPS` in `config.toml` is the
   share of each block reserved for POS transactions, in basis points. The default is `1500`
   (`consensus.DefaultPOSReservationBPS`). `config.ValidateConfig` reports a value above `10000`
   (`config/validate.go`); `consensusd` refuses to start on it, while `nhb` logs a warning
   and starts (`cmd/nhb/config_check.go`). Block size is `[global.Blocks] MaxTxs` (default `500`).
   Both are read from the node's configuration.
2. **Sponsorship limits.** Sponsored POS traffic is limited by the settings in
   [paymaster budgets](./paymaster-budgets.md). Those are also read from `config.toml` at start.
3. **Consensus timing.** Proposal, prevote, precommit and commit timeouts can be set with the
   `consensusd` flags `--consensus-timeout-proposal`, `--consensus-timeout-prevote`,
   `--consensus-timeout-precommit` and `--consensus-timeout-commit` (or the environment
   variables `NHB_CONSENSUS_TIMEOUT_PROPOSAL`, `..._PREVOTE`, `..._PRECOMMIT`, `..._COMMIT`), or in
   the `[consensus]` section of `config.toml`. The pace of block production is set by
   `[consensus] MinBlockInterval` (the wait after each commit before the next round: 1s when the
   key is absent, `0s` turns it off, and a value above half the commit timeout is lowered to it).
   Finality latency and every quantity counted in blocks follow it; see
   [Block cadence](../consensus/block-cadence.md).
4. **Benchmark before changing.** `make bugcheck-perf` runs the performance audit target
   (`Makefile`).

## Common incidents

| Symptom | Where to look |
| --- | --- |
| Finality latency high | `nhb_mempool_pos_p95_finality_ms` and `nhb_consensus_block_interval_seconds`; the validator's `MinBlockInterval`; validator health. |
| POS lane over 1 | `nhb_mempool_pos_lane_fill` and `nhb_mempool_pos_lane_backlog`; raise `POSReservationBPS` or investigate a burst. |
| Sponsored transactions rejected | Run `tx_previewSponsorship` on one of them. A paused merchant, revoked device, exhausted cap or low sponsor balance each has its own `reason` (see [paymaster budgets](./paymaster-budgets.md) and [POS pause and revoke](./pos-pause-revoke.md)). |
| Authorizations disappearing | `pos.auth_auto_voided` events and `nhb_pos_auth_expired_total`. |
| Capture or void rejected with `pos: invalid authorization id` | The capture and void messages take the authorization id as 64 hex characters, optionally prefixed with `0x` (`decodePOSAuthorizationID`, `core/state_pos.go`), the form `pos_getAuthorization` returns. Any other text is refused. |
| Registry change rejected | `pos: signer may not change this registry entry` or `pos: stale nonce`; see [POS merchant and device onboarding](./pos-onboarding.md). |

## Incident review

Record the metrics above for the incident window, the rejected transactions' `status` and
`reason` values, and any configuration change applied before the incident.
