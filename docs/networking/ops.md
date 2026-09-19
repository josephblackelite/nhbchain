# Networking Operations

Operational notes for the P2P layer, taken from the code. Settings live in the
`[p2p]` section of `config.toml` unless stated otherwise (see the repo
`config.toml` for the values it ships with).

## Ports and firewalls

- **P2P: one TCP port.** `ListenAddress` (repo `config.toml`:
  `127.0.0.1:6001`; the validator bootstrap script writes `0.0.0.0:6001`). The
  node needs inbound TCP on that port to accept peers and outbound TCP to dial
  them. The P2P code uses no UDP (`p2p/server.go`, `Start`).
- **JSON-RPC: `RPCAddress`** (repo `config.toml`: `127.0.0.1:8545`). Bind it to
  loopback and put TLS termination or a proxy in front of it if it must be
  reachable; see [security.md](security.md#rpc-perimeter).
- **`p2pd` gRPC: `--grpc`** (default `127.0.0.1:9091`) and **`consensusd` gRPC:
  `--grpc`** (default `127.0.0.1:9090`); see
  [consensusd Getting Started](../consensusd/getting-started.md).
- **Set `[p2p] ExternalAddress`** (`host:port`) when `ListenAddress` is
  `0.0.0.0`. A peer only learns a dialable address for you from the
  `listenAddrs` in your handshake, and an unspecified host is filtered out. The
  validator bootstrap script auto-detects it.
- Behind NAT, forward the TCP port to the node by hand. The node logs a notice at
  start-up (`PUBLIC NODE SETUP REQUIRED`) and never attempts UPnP or NAT-PMP.

## Bootnodes and persistent peers

- `Bootnodes` and `PersistentPeers` are `host:port` strings. Both are trimmed,
  de-duplicated and sorted at start. The node dials each entry at start-up and,
  if a dial fails or a persistent outbound peer disconnects, retries with a
  delay that starts at `DialBackoffSeconds` (30) and doubles up to one minute.
- Addresses in either list are treated as persistent: no rate limiting for that
  peer, no pruning by the connection manager, and no score-based ban. Handshake
  violations and operator bans still apply. A peer is treated as persistent by
  node ID only after it has been reached via one of these addresses or is in the
  config (see [overview.md](overview.md#handshake-protocol-version-1)).
- The value must be `host:port`, for example `<bootnode-host>:6001`; an
  `enode://` URI does not work (`p2p/server.go` `defaultDialer` passes the
  string to `net.Dial("tcp", ...)`).

## Peerstore

The peerstore is a LevelDB database at `<DataDir>/p2p/peerstore`, created on
first run together with `<DataDir>/p2p/node_key.json` (the node identity).
LevelDB is the only backend in the code (`p2p/peerstore.go`). Records are
described in [overview.md](overview.md#peerstore).

- **Backup or restore:** stop the node, copy or replace the `peerstore`
  directory, start the node. The server loads all records at start.
- **Reset:** stop the node and delete `<DataDir>/p2p/peerstore`. The node
  identity in `node_key.json` is separate; deleting that file gives the node a
  new node ID.

## `p2pd` and `consensusd` gRPC bridge

When the node runs as `p2pd` plus `consensusd`, the two are linked by the
`network.v1.NetworkService` gRPC service (`Gossip` stream, `GetView`, `ListPeers`,
`DialPeer`, `BanPeer`). Authentication and TLS for that link are configured in
`[network_security]`; see
[consensusd Getting Started](../consensusd/getting-started.md#security-configuration).
`p2pd` requires either a shared secret or client-certificate authentication, and
plaintext only with `AllowInsecure = true` plus `--allow-insecure` on a loopback
listener (`network.BuildServerSecurity`). With `AllowUnauthenticatedReads = true`,
`GetView` and `ListPeers` skip authentication; `Gossip`, `DialPeer` and
`BanPeer` stay protected. See also the [failover runbook](../runbooks/p2pd-failover.md).

## Listener binding and the production config check

`scripts/verify_prod_config.sh -c <config.toml>` checks a config against a
production profile and exits non-zero on violations. The network-related rules
are: `[network_security] AllowInsecure = false`, `RPCAllowInsecure = false`, the
network and RPC TLS file paths set, and `ListenAddress` and `RPCAddress` not bound
to an unspecified address. The repo `config.toml` itself does not satisfy this
profile (for example it sets `RPCAllowInsecure = true` behind an nginx proxy).

Without RPC TLS certificates, the RPC server refuses to start unless
`RPCAllowInsecure = true` (`rpc/http.go` lines 840-843, error "TLS is required
for RPC server"). With it set, it binds plaintext only to a loopback address; an
unspecified address is accepted only with `RPCAllowInsecureUnspecified = true`,
and every insecure bind increments
`nhb_security_insecure_binds_total{service="rpc",loopback=...}` (lines 844-865).

## Monitoring

- **Logs:** connection, disconnection, rate-limit, ban and handshake-failure
  messages are listed in [security.md](security.md#log-messages).
- **RPC:** `p2p_info` for counts, limits, node identity and seeds; `net_peers`
  (or `p2p_peers`) for per-peer state, score, failures and ban expiry; see
  [net-rpc.md](net-rpc.md).
- **Metrics:** see [observability.md](observability.md).

## Configuration changes

`config.toml` is read at start; changing it requires a restart. The `network.seeds`
registry is likewise read at start (see [seeds.md](seeds.md)).

## Testing a small mesh locally

`p2p/integration/mesh_test.go` starts several in-process servers, one of them on a
different chain ID:

```bash
go test ./p2p/integration -run TestMiniMeshIntegration -count=1
```
