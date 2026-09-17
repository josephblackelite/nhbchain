# POS Finality and QoS SLA Validation

The `POS-READINESS-3` initiative introduces an automated readiness harness that verifies the
priority lane remains healthy under sustained load. The goal is to ensure that POS-tagged
transactions finalize within five seconds (p95) while the reserved lane does not saturate.

## Load Harness (`bench/posloader`)

The `bench/posloader` utility produces a stream of POS-tagged transactions against a JSON-RPC
endpoint. Transactions are emitted at a configurable rate and the loader consumes the POS finality
websocket stream to capture end-to-end latency. Usage:

```bash
go run ./bench/posloader \
  --rpc http://127.0.0.1:8545 \
  --rate 600 \
  --duration 2m \
  --intent-prefix pos-qos
```

Required environment variables:

- `NHB_RPC_TOKEN` – bearer token for RPC authentication.
- `POSLOADER_KEY` – hex-encoded secp256k1 private key seeded with gas funds.

The loader logs submission totals, observed finality counts, and latency statistics to help diagnose
violations.

## Readiness Test (`TestPosQosSla`)

`tests/posreadiness/qos/qos_test.go` boots an in-memory chain via the POS readiness harness,
executes the load harness for a short burst, and then inspects Prometheus metrics:

- `nhb_mempool_pos_lane_fill` must remain ≤ 1.0 to confirm the reserved lane does not saturate.
- `nhb_mempool_pos_p95_finality_ms` must report a p95 latency ≤ 5,000 ms.
- `nhb_mempool_pos_tx_enqueued_total` must match the number of finalized samples to guard against
  starvation.

The test fails if the SLA thresholds are exceeded or if finality events lag behind enqueue events.
