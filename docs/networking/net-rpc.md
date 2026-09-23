# Network RPC Endpoints

Operator-facing JSON-RPC methods for the peer-to-peer layer. They are served by
the node's normal JSON-RPC listener (`RPCAddress`, `127.0.0.1:8545` in the repo
`config.toml`) as `POST` requests with a JSON body; handlers are in
`rpc/net_handlers.go` and `rpc/p2p_query_handlers.go`. The examples below use
`http://127.0.0.1:8545/`.

`net_dial`, `net_ban`, `net_peers` and `p2p_peers` require a bearer JWT
(`Authorization: Bearer <token>`): the first two change state, and the peer list
names every peer's address, score and ban state. `net_info` and `p2p_info` do not
need one. Proxy handling, rate
limits, timeouts and TLS for the listener are described in
[security.md](security.md#rpc-perimeter) and in the
[Network hardening playbook](../security/network-hardening.md).

When the node runs without a P2P server the methods answer HTTP 503 with code
`-32000` and message `unavailable`.

## `net_info`

Takes no parameters (any parameter returns HTTP 400, code `-32040`,
`invalid_params`).

```bash
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"net_info","params":[]}' \
  http://127.0.0.1:8545/
```

Result (`netInfoResult`):

```json
{
  "nodeId": "0x...",
  "peerCounts": { "total": 8, "inbound": 5, "outbound": 3 },
  "chainId": 18346390202490284624,
  "genesisHash": "fe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2",
  "listenAddrs": ["0.0.0.0:6001"]
}
```

`chainId` is the P2P chain ID (first 8 bytes of the genesis hash, big-endian).
`genesisHash` is the hex without a `0x` prefix. `listenAddrs` are the configured
`ListenAddress`, `ExternalAddress` and the bound listener address.

## `net_peers`

Requires auth. Takes no parameters. Returns an array of `PeerNetInfo` combining
live connections, peerstore entries and reputation records:

```json
[
  {
    "nodeId": "0xabc...",
    "addr": "198.51.100.10:38766",
    "direction": "outbound",
    "state": "connected",
    "score": 12,
    "lastSeen": "2026-01-01T00:00:00Z",
    "fails": 0
  },
  {
    "nodeId": "0xdef...",
    "addr": "203.0.113.44:6001",
    "direction": "",
    "state": "banned",
    "score": -120,
    "lastSeen": "2026-01-01T00:00:00Z",
    "fails": 4,
    "bannedUntil": "2026-01-01T01:00:00Z"
  }
]
```

`direction` is `inbound`, `outbound`, or empty for peers that are not connected.
`state` is one of `connected`, `dialing`, `known`, `tracked` or `banned`
(`Server.NetPeers`, `p2p/server.go`). `bannedUntil` appears only for banned peers.

## `p2p_info` and `p2p_peers`

`p2p_info` (no parameters) returns the full `NetworkView`:

```json
{
  "networkId": 18346390202490284624,
  "genesisHash": "fe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2",
  "counts": { "total": 8, "inbound": 5, "outbound": 3 },
  "limits": {
    "maxPeers": 64, "maxInbound": 60, "maxOutbound": 30,
    "rateMsgsPerSec": 50, "burst": 200, "banScore": 100, "greyScore": 50
  },
  "self": { "nodeId": "0x...", "protocolVersion": 1, "clientVersion": "nhbchain/node" },
  "bootnodes": ["<bootnode-host>:6001"],
  "persistentPeers": [],
  "seeds": [
    { "nodeId": "0x...", "address": "host:port", "source": "config" }
  ]
}
```

`seeds` lists the merged seed catalogue with each entry's `source` (`config`,
`registry.static`, or `dns:<domain>`) and optional `notBefore` / `notAfter`.
`p2p_peers` (no parameters, requires auth) returns exactly the same array as
`net_peers`. Both return HTTP 400 with code `-32602` (`p2p_peers`) or `-32040`
(`net_peers`) if given parameters.

## `net_dial`

Queues a manual outbound dial. Requires auth. One parameter object:

```json
{ "target": "0xNODEID" }
```

`target` is a node ID (resolved through the peerstore, then the seed list) or a
`host:port` address. Example:

```bash
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":7,"method":"net_dial","params":[{"target":"203.0.113.44:6001"}]}' \
  http://127.0.0.1:8545/
```

A success returns `{"ok": true}`; the dial itself runs asynchronously and waits
for the peerstore/backoff delay first. If the node is already connected to the
target it also returns `{"ok": true}`.

Parameter errors from the handler itself (not exactly one parameter object,
malformed JSON in it) return HTTP 400, code `-32040`, message `invalid_params`.

Errors from the dial itself (`writeNetError`, `rpc/net_handlers.go`):

- The P2P server's own errors, wrapped with detail, are mapped by kind:
  `p2p: unknown peer` (a node ID not in the peerstore or seed list) to HTTP 404 /
  `-32041` `unknown_peer`; `p2p: peer is banned` to HTTP 409 / `-32042`
  `peer_banned`; `p2p: empty dial target` and `p2p: invalid dial address` to
  HTTP 400 / `-32040` `invalid_params`. The error text is in `data`.
- When the service returns gRPC status errors (the gRPC network client in
  `network/client.go`), they are mapped to: `InvalidArgument` to HTTP 400 /
  `-32040` `invalid_params`; `NotFound` to HTTP 404 / `-32041` `unknown_peer`;
  `FailedPrecondition` to HTTP 409 / `-32042` `peer_banned`; `Unavailable` to
  HTTP 503 / `-32000` `unavailable`.
- Any other error is reported as HTTP 500, code `-32000`, message `server_error`.

## `net_ban`

Bans a peer and disconnects it if connected. Requires auth. One parameter object:

```json
{ "nodeId": "0xabc123...", "secs": 7200 }
```

`secs` is optional; `0` or omitted uses `PeerBanDuration` (`[p2p]
BanDurationSeconds`); a negative value returns HTTP 400 / `-32040`. The node ID
must be a connected peer, a peerstore entry or a seed, otherwise the call fails
with `p2p: unknown peer: <id>` (reported as described under `net_dial`). A
success returns `{"ok": true}`.

Malformed requests can also produce the standard JSON-RPC errors (`-32600`,
`-32601`, and so on).

## `sync_status`

Takes no parameters. Returns
`{"chainHeight": n, "snapshotHeight": n, "managerReady": bool}`; see
[sync.md](sync.md).
