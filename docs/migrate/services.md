# Migration Guide: Service-Oriented Topology

This guide describes how the separate binaries in this repository fit together when you move
from a single `nhb` node exposing JSON-RPC to the split topology. It lists only what the code
requires. For each service's endpoints see the [service directory](../services/index.md).

## Components

| Binary or service | Source | What it does |
| --- | --- | --- |
| `nhb` | `cmd/nhb` | Full node with JSON-RPC (`RPCAddress` in `config.toml`). |
| `consensusd` | `cmd/consensusd` | Consensus node with a gRPC API. Talks to `p2pd` over gRPC. |
| `p2pd` | `cmd/p2pd` | Peer-to-peer daemon serving `network.v1.NetworkService` to `consensusd`. |
| `gateway` | `cmd/gateway` | HTTP front end: REST and gRPC route groups plus the optional `/rpc` compatibility route. |
| `governd` | `services/governd` | Governance gRPC service. Builds, signs and submits transactions to the consensus node. |
| `lendingd` | `services/lendingd` | Lending service. Reaches the node through `node_rpc_url`. |

None of these services uses Postgres or Redis; the code has no client for either.

## 1. Consensus and `p2pd`

* Start `consensusd` with `--config` (default `./config.toml`), `--genesis`, `--grpc` (default
  `127.0.0.1:9090`) and `--p2p` (the `p2pd` address, default `localhost:9091`).
* Start `p2pd` next to it with the same `config.toml` and genesis. It serves its gRPC on
  `--grpc` (default `127.0.0.1:9091`). There is no separate `p2pd.toml`: it reads `config.toml`,
  including `ListenAddress` (the gossip listener), `Bootnodes`, `PersistentPeers` and the
  `[network_security]` section that protects the `consensusd` link.
* Open the gossip listener you configured in `ListenAddress` between validators. A peer address
  without a port is dialled on `26656` (`p2p/connmanager.go`).
* `consensusd` (like `nhb`) accepts `--allow-migrate`; see the
  [migration runbook](../runbooks/migrations.md) for what the state schema guard does.

## 2. Domain services

* `governd`: `--config` (default `services/governd/config.yaml`). Keys include `listen`,
  `consensus` (the consensus gRPC address), `chain_id`, the signer key (`signer_key`,
  `signer_key_file` or `signer_key_env`), `nonce_store_path`, `fee`, `tls`, `auth` and
  `consensus_client`.
* `lendingd`: `--config` (default `services/lending/config.yaml`). Keys include `listen`,
  `node_rpc_url`, `node_rpc_token`, `rate_limit_per_min`, `tls` and `auth`.
* `lendingd` restricts plaintext mode to loopback listeners or a dev environment
  (`services/lendingd/main.go`). `governd` refuses to dial the consensus node without TLS
  material or a shared secret unless `consensus_client.allow_insecure` is true
  (`services/governd/dial.go`).

## 3. Gateway

* Start `gateway` with `--config <yaml>`. Without a file it listens on `:8080`. TLS is required
  (`security.tlsCertFile`, `security.tlsKeyFile`) unless `NHB_ENV=dev` and `--allow-insecure` on
  a loopback address.
* Configure the upstream base URLs and authentication as described in
  [monolith to gateway](./monolith-to-gateway.md) and
  [gateway anonymous routes](./gateway-anonymous-routes.md). Set the HMAC secret
  (`auth.hmacSecret`) for the tokens your clients present.
* Decide whether to keep `/rpc` with `--compat-mode` (see the
  [decommission timeline](./deprecation-timeline.md)).
* `GET /healthz` on the gateway returns `ok`.

## 4. Cut over

1. Bring up `consensusd` and `p2pd`, and confirm the chain height advances.
2. Bring up `governd` and `lendingd`, then the gateway.
3. Point clients at the gateway. Clients that still send JSON-RPC to the old node can use the
   `/rpc` route (method table and what it needs from its upstreams in
   [monolith to gateway](./monolith-to-gateway.md)). The route requires a bearer token like the
   other routes, unless anonymous access is configured for it
   ([gateway anonymous routes](./gateway-anonymous-routes.md)).
4. Exercise your integrations end to end. The cookbooks in `docs/cookbooks` and the samples in
   `examples/` show first transactions and queries.
5. Stop the old JSON-RPC node when you no longer need it. The repository defines no waiting
   period.
