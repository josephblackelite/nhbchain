# POS-QOS-3: Priority Mempool and Proposer Quotas

## Overview

This change introduces a dual-lane mempool that guarantees dedicated block
capacity for POS-tagged transactions. Transactions that carry an
`intent_ref` are routed into a priority lane and scheduled ahead of normal
traffic whenever block space is scarce. Validators reserve a configurable
fraction of each block (default 15%) for the priority lane while allowing any
unused reservation to spill over to the normal lane so overall throughput is
unchanged.

## Lane classification

* Incoming transactions are classified at enqueue time by inspecting
  `transaction.IntentRef`.
* POS-tagged NHB and ZNHB transfers populate the priority lane; all others
  remain in the normal lane.
* When a transaction with an `intent_ref` is admitted, the node records an
  enqueue timestamp so that finality latency can be measured when the
  transaction is committed.
* If a transaction ages out (for example, an expired mint) or is evicted, the
  priority-lane bookkeeping is cleared to prevent stale latency samples.

## Scheduling and quotas

* During block proposal the mempool snapshot is partitioned into the priority
  and normal lanes.
* Let `max_txs` be the configured block cap and `reservation_bps` the POS lane
  reservation expressed in basis points (default 1,500 = 15%).
* The proposer reserves `ceil(max_txs * reservation_bps / 10_000)` slots for
  the priority lane. If fewer POS transactions are pending, the unused share is
  immediately released to the normal lane.
* When the normal lane cannot fill its allocation, remaining block space is
  returned to the priority lane, ensuring the reservation never reduces total
  throughput.
* All transactions are presented to the consensus engine in the computed order
  so that the first `max_txs` positions inside a proposal adhere to the quota.

## Metrics

New Prometheus metrics are exported under the `nhb_mempool` subsystem:

| Metric | Type | Description |
| --- | --- | --- |
| `pos_lane_fill` | Gauge | POS backlog divided by reserved capacity (>1 = saturation; 0 reservation -> backlog). |
| `pos_lane_backlog{asset="…"}` | Gauge | Count of POS-tagged transfers segmented by asset (e.g. `nhb`, `znhb`). |
| `pos_tx_enqueued_total` | Counter | Number of POS-tagged transactions accepted into the mempool. |
| `pos_p95_finality_ms` | Histogram | POS enqueue-to-finality latency samples in milliseconds (dashboards compute p95). |

## Configuration

* `global.mempool.POSReservationBPS` defines the reserved percentage in basis
  points (0–10,000). The default is 1,500 (15%).
* Setting the value to zero disables the reservation and causes `pos_lane_fill`
  to report the raw POS backlog count.

## Expected performance

* Under sustained congestion the priority lane receives at least 15% of each
  block, ensuring POS-tagged transactions reach finality without waiting behind
  normal traffic.
* Operators should monitor `pos_p95_finality_ms` and adjust the reservation if
  the priority lane regularly saturates (`pos_lane_fill` ≫ 1).
