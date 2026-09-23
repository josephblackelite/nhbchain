# POS-QOS-3

## Summary

* Added a POS-priority mempool lane that reserves a configurable share of block
  space for intent-tagged NHB and ZNHB transfers (`mempool/priority.go` `IsPOSLaneEligible`) while allowing unused quota to spill back
  to the normal lane.
* Introduced Prometheus metrics (`nhb_mempool_pos_lane_fill`,
  `nhb_mempool_pos_tx_enqueued_total`, `nhb_mempool_pos_p95_finality_ms`, a histogram of enqueue-to-finality latency in milliseconds, not a precomputed p95 gauge) to track reservation pressure and latency outcomes.
* Documented the scheduling policy, configuration knob, and operational
  guidance for POS QoS.
