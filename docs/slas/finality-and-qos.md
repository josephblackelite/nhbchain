# POS Finality and QoS SLA Validation

The repository ships a load generator and one automated test that check the
POS-tagged transaction path: finality latency for POS transactions and the
fill level of the POS-reserved mempool lane.

## Load generator (`bench/posloader`)

`bench/posloader/main.go` submits POS-tagged transfers to a JSON-RPC endpoint and
measures the time until each one is reported finalized on the node's POS
finality websocket (`/ws/pos/finality`, registered in `rpc/http.go`, line 784).

```bash
NHB_RPC_TOKEN=<jwt> POSLOADER_KEY=<hex private key> \
go run ./bench/posloader \
  --rpc http://127.0.0.1:8545 \
  --rate 600 \
  --duration 2m \
  --intent-prefix pos-qos
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--rpc` | `http://127.0.0.1:8545` | RPC endpoint. The websocket URL is derived from it (`ws` or `wss`). |
| `--key` | empty | Hex secp256k1 private key of a funded account. Overrides `POSLOADER_KEY`. |
| `--rate` | `600` | Target transactions **per minute** (the loader sleeps `1 minute / rate` between submissions). |
| `--duration` | `2m` | How long to submit. |
| `--intent-prefix` | `pos-load` | Prefix for the generated `IntentRef` (`<prefix>-<nonce>`). |

Environment:

- `NHB_RPC_TOKEN` (required): bearer token sent with every `nhb_sendTransaction`.
  The loader exits without it.
- `POSLOADER_KEY`: hex private key, used when `--key` is empty. One of the two is
  required.

Each transaction is a zero-value `TxTypeTransfer` with a 5-minute `IntentExpiry`,
`MerchantAddress = "pos-qos"` and `DeviceID = "loader"`, signed by the key.
After the submission window the loader waits up to 30 seconds for outstanding
finality events, then logs the number submitted, the number finalized (and still
pending), and the average and maximum latency.

## Readiness test (`TestPosQosSla`)

`tests/posreadiness/qos/qos_test.go` carries the build tag `posreadiness`:

```bash
go test -tags posreadiness -run TestPosQosSla ./tests/posreadiness/qos
```

It starts an in-memory mini chain (`tests/posreadiness/harness`), funds an
account, runs `go run ./bench/posloader` against the mini chain for 25 seconds
at 3 transactions per minute (chosen to avoid RPC throttling), finalizes the
mempool every 200 ms, and then reads the default Prometheus registry:

- `nhb_mempool_pos_p95_finality_ms` is a histogram (buckets 50 ms to 12,800 ms).
  The test computes the 95th percentile as the upper bound of the bucket that
  contains the 95th-percentile sample and fails if it is above 5,000 ms. It also
  fails if the histogram has no samples.
- `nhb_mempool_pos_lane_fill` (gauge) must not exceed `1.0`.
- `nhb_mempool_pos_tx_enqueued_total` (counter) must be non-zero, and the
  finality histogram's sample count must be at least that many (no starved
  transactions).

The metrics are registered in `observability/metrics.go` (`Mempool()`), which also
registers `nhb_mempool_pos_lane_backlog{asset}`.
