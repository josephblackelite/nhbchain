# Network RPC Endpoints

The NET-2F API exposes operator-focused JSON-RPC methods under the existing
HTTP listener. All examples assume the daemon is reachable at
`http://127.0.0.1:8080` and that the `NHB_RPC_TOKEN` environment variable is set
when authentication is required.

## `net_info`

Returns high-level node and network metadata for observability dashboards.

### Request

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"net_info","params":[]}' \
  http://127.0.0.1:8080/
```

### Response

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "nodeId": "0xabcdef...",
    "peerCounts": {
      "total": 8,
      "inbound": 5,
      "outbound": 3
    },
    "chainId": 187001,
    "genesisHash": "7bf4...",
    "listenAddrs": [
      "0.0.0.0:6001",
      "[::]:6001"
    ]
  }
}
```

## `net_peers`

Returns enriched per-peer state combining live connections, peerstore metrics
and reputation scores.

### Request

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"net_peers","params":[]}' \
  http://127.0.0.1:8080/
```

### Response

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": [
    {
      "nodeId": "0xabc...",
      "addr": "198.51.100.10:38766",
      "direction": "outbound",
      "state": "connected",
      "score": 12,
      "lastSeen": "2024-04-19T15:55:04Z",
      "fails": 0
    },
    {
      "nodeId": "0xdef...",
      "addr": "203.0.113.44:38766",
      "direction": "",
      "state": "banned",
      "score": -120,
      "lastSeen": "2024-04-18T11:22:33Z",
      "fails": 4,
      "bannedUntil": "2024-04-19T16:00:00Z"
    }
  ]
}
```

`state` is one of `connected`, `dialing`, `known`, `tracked`, or `banned`.

## `net_dial`

Queues a manual outbound dial to an address or known node ID. This method
requires authentication.

### Parameters

```json
{
  "target": "0xabc123..." // Node ID or host:port
}
```

### Example

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":7,"method":"net_dial","params":[{"target":"203.0.113.44:38766"}]}' \
  http://127.0.0.1:8080/
```

Successful responses return `{ "ok": true }`. The dial respects peerstore
backoff windows and active bans, so it may complete asynchronously.

### Error Codes

| Condition                | HTTP | Code              | Message        |
| ------------------------ | ---- | ----------------- | -------------- |
| Invalid target/address   | 400  | `-32040`          | `invalid_params` |
| Unknown node ID          | 404  | `-32041`          | `unknown_peer` |
| Target currently banned  | 409  | `-32042`          | `peer_banned`  |

## `net_ban`

Applies an immediate operator ban to a peer. The peer is disconnected if it is
currently online and prevented from reconnecting until the ban expires. This
method requires authentication.

### Parameters

```json
{
  "nodeId": "0xabc123...",
  "secs": 7200 // optional, defaults to BanDurationSeconds
}
```

### Example

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":8,"method":"net_ban","params":[{"nodeId":"0xabc123...","secs":7200}]}' \
  http://127.0.0.1:8080/
```

### Error Codes

| Condition       | HTTP | Code     | Message        |
| --------------- | ---- | -------- | -------------- |
| Unknown node ID | 404  | `-32041` | `unknown_peer` |

All methods MAY return standard JSON-RPC errors (`-32600`, `-32601`, etc.) for
malformed requests.
