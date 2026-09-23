# POS realtime finality stream

The node streams status transitions for transactions that carry an `intentRef`
(`core/pos_stream.go`, `rpc/stream_finality.go`, `rpc/ws.go`):

* `pending` is published when an NHB or ZNHB transfer (`TxTypeTransfer`,
  `TxTypeTransferZNHB`) with an `intentRef` is admitted to this node's mempool.
* `finalized` is published when a block containing a transaction with an
  `intentRef` (any transaction type) is committed.

## Transports

| Transport | Endpoint | Notes |
| --- | --- | --- |
| gRPC | `pos.v1.Realtime/SubscribeFinality` on the node's RPC address | Served by the same listener as JSON-RPC (`grpcHandler` in `rpc/http.go`), so it needs HTTP/2 (TLS or h2c). |
| WebSocket | `/ws/pos/finality` on the RPC address | Optional `?cursor=<n>` query. Any browser origin is accepted unless `RPCWebSocketOrigins` lists host patterns (`streamAcceptOptions`, `rpc/ws.go`); the client-address allowlist applies. At most 256 streams are open at once and 8 per client address by default (`RPCWebSocketMaxConnections`, `RPCWebSocketMaxPerIP`); beyond that the upgrade is refused with HTTP 429 `too many open streams`. |

Neither transport requires the JWT used for privileged JSON-RPC methods.

## Update fields

Each update has a `cursor`, which is the decimal string of a per-process,
monotonically increasing sequence number.

gRPC (`proto/pos/realtime.proto`, `FinalityUpdate`):

| Field | Type | Description |
| --- | --- | --- |
| `cursor` | string | Sequence number as a decimal string. |
| `intent_ref` | bytes | The transaction's `intentRef`. |
| `tx_hash` | bytes | Transaction hash. |
| `status` | enum | `FINALITY_STATUS_PENDING` or `FINALITY_STATUS_FINALIZED`. |
| `block_hash` | bytes | Set when finalized. |
| `height` | uint64 | Block height when finalized, `0` while pending. |
| `timestamp` | int64 | Enqueue time (pending) or block timestamp (finalized), unix seconds. |

WebSocket text frames are JSON (`finalityUpdatePayload`):

```json
{
  "type": "tx_update",
  "cursor": "42",
  "intentRef": "0x1234...",
  "txHash": "0xabcd...",
  "status": "finalized",
  "block": "0xdead...",
  "height": 10542,
  "ts": 1702855012
}
```

`block` and `height` are omitted while pending.

## Resuming

`SubscribeFinalityRequest.cursor` (gRPC) and `?cursor=` (WebSocket) are parsed as
an unsigned decimal integer; an empty or unparseable value is treated as `0`. On
connect the node first sends every retained update with a sequence greater than
the cursor, then live updates.

The node retains only the most recent 2048 updates (`posFinalityHistoryLimit`),
in memory. After a process restart the sequence and history start over, so a
cursor from before the restart is meaningless. If a cursor is older than the
oldest retained entry, the stream starts at the oldest retained entry; there is no
signal that updates were missed, so reconcile with
`pos_getAuthorizationByIntentRef` ([gateway-pos.md](gateway-pos.md)) after a
gap. Live delivery to a subscriber is non-blocking: if a subscriber's 32-slot
buffer is full the update is dropped for that subscriber.

Only intents that pass through this validator are visible in its stream. Use
`sdk/pos/examples/subscriber.ts` as a client reference; it reads
`POS_REALTIME_GRPC`, `POS_REALTIME_WS`, `POS_SAMPLE_WINDOW_MS` and `POS_CURSOR`
from the environment (defaults `localhost:9090`,
`ws://localhost:8545/ws/pos/finality`, `10000`).
