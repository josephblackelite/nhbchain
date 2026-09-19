# POS SLA & Troubleshooting Runbook

This runbook lists the POS signals the node exports and how to use them when POS traffic is
slow or being rejected. The code defines no numeric SLO targets for POS; targets are yours to
set on the metrics below. The lane design is in [POS quality of service](../specs/pos-qos.md).

## Signals

Prometheus metrics registered by the node (`observability/metrics.go`):

| Metric | Type | Meaning |
| --- | --- | --- |
| `nhb_mempool_pos_lane_fill` | gauge | POS transactions waiting divided by the lane's target capacity. A value above 1 means the lane is over its reservation. When the target is zero it reports the raw POS count. |
| `nhb_mempool_pos_lane_backlog{asset}` | gauge | POS-tagged transfers waiting, per asset (`nhb`, `znhb`). |
| `nhb_mempool_pos_tx_enqueued_total` | counter | POS-tagged transactions admitted to the mempool. |
| `nhb_mempool_pos_p95_finality_ms` | histogram | POS enqueue-to-finality latency in milliseconds, buckets 50 to 12800. The name says p95 but it is a histogram; compute the quantile in Prometheus. |
| `nhb_pos_auth_expired_total` | counter | POS authorizations voided automatically after expiry. |
| `nhb_paymaster_autotopups_total{outcome}` | counter | Automatic paymaster top-ups (see [auto top-up](./paymaster-autotopup.md)). |

Events: `pos.auth_auto_voided` (expiry sweep), `tx.sponsorship.failed` and
`paymaster.throttled` (sponsorship rejected; `paymaster.throttled` is emitted only when a daily cap was hit, not for a paused merchant or revoked device; see
[Paymaster administration](../launch/paymaster-admin.md)).

Realtime feed: the node serves the WebSocket `/ws/pos/finality` (`rpc/http.go`) and the gRPC
method `pos.v1.Realtime/SubscribeFinality`. Both stream `pending` and `finalized` updates for
POS intents; the schema is in [POS realtime](../api/pos-realtime.md).

The general request error and latency alerts are in the [alert runbook](./alerts.md).

## Tuning

1. **POS lane reservation.** `[global.Mempool] POSReservationBPS` in `config.toml` is the
   share of each block reserved for POS transactions, in basis points. The default is `1500`
   (`consensus.DefaultPOSReservationBPS`) and validation rejects values above `10000`
   (`config/validate.go`). Block size is `[global.Blocks] MaxTxs` (default `500`). Both are
   read from the node's configuration.
2. **Sponsorship limits.** Sponsored POS traffic is limited by the settings in
   [paymaster budgets](./paymaster-budgets.md). Those are also read from `config.toml` at start.
3. **Consensus timing.** Proposal, prevote, precommit and commit timeouts can be set with the
   `consensusd` flags `--consensus-timeout-proposal`, `--consensus-timeout-prevote`,
   `--consensus-timeout-precommit` and `--consensus-timeout-commit`, or in the `[consensus]`
   section of `config.toml`.
4. **Benchmark before changing.** `make bugcheck-perf` runs the performance audit target
   (`Makefile`).

## Common incidents

| Symptom | Where to look |
| --- | --- |
| Finality latency high | `nhb_mempool_pos_p95_finality_ms` and `nhb_consensus_block_interval_seconds`; validator health. |
| POS lane over 1 | `nhb_mempool_pos_lane_fill` and `nhb_mempool_pos_lane_backlog`; raise `POSReservationBPS` or investigate a burst. |
| Sponsored transactions rejected | Run `tx_previewSponsorship` on one of them. A paused merchant, revoked device, exhausted cap or low sponsor balance each has its own `reason` (see [paymaster budgets](./paymaster-budgets.md) and [POS pause and revoke](./pos-pause-revoke.md)). |
| Authorizations disappearing | `pos.auth_auto_voided` events and `nhb_pos_auth_expired_total`. |

## Incident review

Record the metrics above for the incident window, the rejected transactions' `status` and
`reason` values, and any configuration change applied before the incident.
