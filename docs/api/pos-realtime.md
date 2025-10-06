# POS realtime finality stream

The realtime service exposes live status transitions for POS payment intents.
A stream update is emitted when a signed intent transaction enters the
mempool (`pending`) and again when the enclosing block is BFT-finalized
(`finalized`). This allows cashier and reconciliation systems to surface
transaction outcomes without polling JSON-RPC.

## Transport endpoints

| Transport | URL pattern | Notes |
| --- | --- | --- |
| gRPC | `pos.v1.Realtime/SubscribeFinality` on the standard node RPC port | Requires HTTP/2 (TLS or h2c). |
| WebSocket | `/ws/pos/finality` on the node RPC origin | Supports optional `cursor` query for resume. |

Both transports stream the same payload. Each update is tagged with a
monotonic `cursor` string that can be supplied when reconnecting to request
backfill for any missed events.

### Update schema

All messages describe a `tx_update` event:

| Field | Type | Description |
| --- | --- | --- |
| `cursor` | string | Monotonic resume token (opaque). |
| `intentRef` | bytes/hex | POS intent reference. |
| `txHash` | bytes/hex | Transaction hash. |
| `status` | enum/string | `pending` (accepted) or `finalized` (BFT commit). |
| `blockHash` | bytes/hex | Present when `status=finalized`. |
| `height` | uint64 | Finalized block height, `0` while pending. |
| `ts`/`timestamp` | int64 | Block or enqueue timestamp (Unix seconds). |

### gRPC subscription

```protobuf
service Realtime {
  rpc SubscribeFinality(SubscribeFinalityRequest)
      returns (stream SubscribeFinalityResponse);
}

message SubscribeFinalityRequest {
  string cursor = 1; // optional resume token
}
```

Clients should maintain the latest `cursor` observed and re-issue it on a new
`SubscribeFinalityRequest` after transient failures.

Example using [`sdk/pos/examples/subscriber.ts`](../../sdk/pos/examples/subscriber.ts):

```bash
POS_REALTIME_GRPC=rpc.testnet.nhbcoin.net:9090 \
POS_REALTIME_WS=wss://rpc.testnet.nhbcoin.net/ws/pos/finality \
ts-node sdk/pos/examples/subscriber.ts
```

The sample streams via gRPC for `POS_SAMPLE_WINDOW_MS` milliseconds, captures
the most recent cursor, then reconnects over WebSocket to demonstrate
backfill.

### WebSocket subscription

Establish a connection to `/ws/pos/finality`. To resume from a previous
position supply `?cursor=<lastCursor>`. Updates are delivered as JSON objects:

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

If the server prunes history older than your cursor, the stream will begin
from the oldest retained event.

## Reconnection guidance

1. Persist the latest `cursor` string after every message.
2. On reconnect, include the stored cursor. The server replays any unseen
   updates (if available) before live data.
3. If the cursor is too old, the stream starts from the current head. Treat
   this as a signal to backfill via batch queries if required.

## Backward compatibility

Nodes that do not implement POS-RT-7 simply return `404` for the WebSocket
route and omit the `Realtime` gRPC service. Clients should detect this
condition and fall back to existing polling flows.
