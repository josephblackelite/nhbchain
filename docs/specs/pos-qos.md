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

* `maxTxs` is `[global.Blocks] MaxTxs` (default 500), or the whole mempool size if
  that is not positive; it is also capped at the number of pending transactions.
* Reserved POS slots: `ceil(maxTxs * ReservationBPS / 10000)`, never more than
  `maxTxs`. Zero reservation reserves nothing.
* The proposer takes up to that many POS transactions first, then fills the
  remaining slots from the normal lane; if either lane cannot fill its share, the
  other lane gets the leftover slots. Total block size is unchanged.
* The proposal order is: the selected POS transactions, the selected normal
  transactions, then the unselected remainder of each lane.

## Configuration

`[global.Mempool] POSReservationBPS` (basis points, 0 to 10000; validated at
load). The default is 1500 (15%) when the key is absent (`consensus.DefaultPOSReservationBPS`);
an explicit `0` disables the reservation.

## Metrics

Prometheus, namespace `nhb`, subsystem `mempool`:

| Metric | Type | Meaning |
| --- | --- | --- |
| `nhb_mempool_pos_lane_fill` | Gauge | Pending POS transactions divided by reserved slots. When the reservation is zero, the raw pending POS count. |
| `nhb_mempool_pos_lane_backlog{asset}` | Gauge | Pending POS-lane transactions by asset; `nhb` and `znhb` are always exported. |
| `nhb_mempool_pos_tx_enqueued_total` | Counter | POS-lane transactions admitted. |
| `nhb_mempool_pos_p95_finality_ms` | Histogram | Enqueue-to-finality latency in ms, buckets 50, 100, 200, 400, 800, 1600, 3200, 6400, 12800. The name says p95, the metric is a histogram; compute the percentile in your dashboard. |
