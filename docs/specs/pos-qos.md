# POS quality of service: priority lane and block quota

When a proposer assembles a block it splits the mempool into a POS lane and a
normal lane and reserves a share of the block for the POS lane. Code:
`mempool/priority.go`, `consensus/proposer.go`, `Node.GetMempool` in
`core/node.go`, metrics in `observability/metrics.go`.

## Lane classification

A transaction is in the POS lane when it has a non-empty `intentRef` and its type
is `TxTypeTransfer` (NHB) or `TxTypeTransferZNHB` (`IsPOSLaneEligible`). Every
other transaction is in the normal lane. On admission of a POS-lane transaction
the node records its arrival time (used for the finality histogram below) and
publishes a `pending` finality update ([POS realtime](../api/pos-realtime.md)).

## Scheduling

`mempool.Schedule(lanes, maxTxs, quota)`:

## Metrics

New Prometheus metrics are exported with the `nhb_mempool_` prefix:

| Metric | Type | Description |
| --- | --- | --- |
| `nhb_mempool_pos_lane_fill` | Gauge | POS backlog divided by reserved capacity (>1 = saturation; 0 reservation -> backlog). |
| `nhb_mempool_pos_lane_backlog{asset="…"}` | Gauge | Count of POS-tagged transfers segmented by asset (e.g. `nhb`, `znhb`). |
| `nhb_mempool_pos_tx_enqueued_total` | Counter | Number of POS-tagged transactions accepted into the mempool. |
| `nhb_mempool_pos_p95_finality_ms` | Histogram | POS enqueue-to-finality latency samples in milliseconds (dashboards compute p95). |

## Configuration

* `global.mempool.POSReservationBPS` defines the reserved percentage in basis
  points (0–10,000). The default is 1,500 (15%).
* Setting the value to zero disables the reservation and causes `nhb_mempool_pos_lane_fill`
  to report the raw POS backlog count.

## Metrics

* Under sustained congestion the priority lane receives at least 15% of each
  block, ensuring POS-tagged transactions reach finality without waiting behind
  normal traffic.
* Operators should monitor `nhb_mempool_pos_p95_finality_ms` and adjust the reservation if
  the priority lane regularly saturates (`pos_lane_fill` ≫ 1).
