# p2pd Service Overview

`p2pd` (`cmd/p2pd`) is the standalone peer-to-peer daemon used when consensus runs
separately in `consensusd`. It runs the P2P server from `p2p/` and exposes an
internal gRPC service, `network.v1.NetworkService` (`proto/network/v1`), which
`consensusd` connects to. The all-in-one `cmd/nhb` binary runs the same P2P server
in-process and does not use `p2pd`.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | `./config.toml` | Configuration file. |
| `--genesis` | empty | Genesis path (overrides `NHB_GENESIS` and config `GenesisFile`). |
| `--allow-autogenesis` | `false` | Allow automatic genesis creation (overrides `NHB_ALLOW_AUTOGENESIS`). |
| `--grpc` | `127.0.0.1:9091` | Listen address of the internal gRPC server. |
| `--allow-insecure` | `false` | Development only: permit plaintext on a loopback listener. |

`p2pd` opens the LevelDB at the configured `DataDir` to read the chain ID, genesis
hash and the `network.seeds` parameter, and keeps its peerstore and node identity
in `<DataDir>/p2p/`.

## What it does

- Builds the P2P server from `config.toml` (`[p2p]` limits, rate limits, ban and
  grey scores, timeouts, bootnodes, persistent peers, seeds, PEX flag). Static
  seeds are combined with the on-chain `network.seeds` registry read at start (see
  [seeds](../networking/seeds.md)).
- Uses the peerstore at `<DataDir>/p2p/peerstore`, so scores, bans and dial
  backoff survive restarts.
- Bridges gossip: messages received from peers are queued for the connected
  `consensusd` stream, and messages `consensusd` sends are broadcast to peers. If
  no stream is connected, or its queue is full (`StreamQueueSize`, default 128),
  inbound messages are dropped and counted. `RelayDropLogRatio` (default 0.1) sets
  the drop ratio that triggers a log line. The relay sends a heartbeat envelope on
  the stream every `PingIntervalSeconds`.

## Peer transport

Peers speak newline-delimited JSON over plain TCP with a signed handshake
([networking overview](../networking/overview.md)). `p2pd` does not use QUIC or
TLS toward peers. TLS and shared-secret authentication apply only to the gRPC link
to `consensusd`.

## gRPC service

| RPC | Purpose | Authentication |
| --- | --- | --- |
| `Gossip` (bidirectional stream) | Carries gossip and heartbeats between `p2pd` and `consensusd`. | Write authenticator. |
| `GetView` | Network view: counts, limits, identity, bootnodes, persistent peers, seeds, listen addresses. | Read authenticator. |
| `ListPeers` | Peer diagnostics (same data as `net_peers`). | Read authenticator. |
| `DialPeer` | Queue a dial to a node ID or `host:port`. Maps the P2P errors to `InvalidArgument`, `NotFound` or `FailedPrecondition`. | Write authenticator. |
| `BanPeer` | Ban a node ID for a number of seconds. | Write authenticator. |

Authentication is configured in `[network_security]` (see
[consensusd Getting Started](../consensusd/getting-started.md#security-configuration)).
`p2pd` refuses to start without a shared secret or client-certificate
authentication. `AllowUnauthenticatedReads = true` removes authentication from
`GetView` and `ListPeers` only.

## Operations

Logs use structured records for peer connect and disconnect, bans and seed
handling. See the [failover runbook](../runbooks/p2pd-failover.md) and
[networking operations](../networking/ops.md).
