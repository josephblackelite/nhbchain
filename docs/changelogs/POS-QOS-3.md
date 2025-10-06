# POS-QOS-3

## Summary

* Added a POS-priority mempool lane that reserves a configurable share of block
  space for intent-tagged transactions while allowing unused quota to spill back
  to the normal lane.
* Introduced Prometheus metrics (`pos_lane_fill`, `pos_tx_enqueued_total`,
  `pos_p95_finality_ms`) to track reservation pressure and latency outcomes.
* Documented the scheduling policy, configuration knob, and operational
  guidance for POS QoS.
