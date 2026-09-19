# NHBChain components

This page lists the programs in this repository and how they connect. Chain rules, parameters, the CLI and the JSON-RPC surface are described in the [repository README](../README.md).

## The node

`cmd/nhb` is the node. One process runs the BFT consensus engine, the P2P server and the JSON-RPC HTTP server (`nhb_*` and related methods). `deploy/systemd/nhb.service` runs it as `/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml`, the validator bootstrap script (`scripts/deployvalidator.sh`) installs that unit, and CI builds it.

## Split deployment

The consensus engine and the P2P server can also run as two programs that talk over gRPC. Both read the same TOML config format as `cmd/nhb`.

| Program | Serves | Defaults |
| --- | --- | --- |
| `cmd/consensusd` | gRPC `consensus.v1.ConsensusService` and `consensus.v1.QueryService` | `--grpc 127.0.0.1:9090`; connects to `p2pd` at `--p2p localhost:9091`; `--consensus-timeout-proposal`, `--consensus-timeout-prevote`, `--consensus-timeout-precommit` and `--consensus-timeout-commit` set the round timeouts |
| `cmd/p2pd` | gRPC `network.v1.NetworkService` | `--grpc 127.0.0.1:9091` |

The link between them is secured by the `[network_security]` section (TLS certificates, allowed client common names, a shared secret sent in a metadata header). `--allow-insecure` permits plaintext on loopback and is marked development only. `consensusd` does not serve the JSON-RPC methods; those come from `cmd/nhb`.

## HTTP gateway

`cmd/gateway` is an HTTP router configured by a YAML file (`-config`; the sample is `gateway/config.yaml`). Each route forwards a URL prefix to a named backend:

| Prefix | Backend name | Token required |
| --- | --- | --- |
| `/v1/lending` | `lendingd` | yes, scope `lending` |
| `/v1/swap` | `swapd` (no server for it is in this repository) | yes, scope `swap` |
| `/v1/gov`, `/gov.v1.Msg` | `governd` | yes, scope `gov` |
| `/gov.v1.Query` | `governd` | no |
| `/v1/transactions` | `consensusd` | yes |
| `/v1/consensus`, `/consensus.v1.ConsensusService` | `consensusd` | no |

Backend base URLs default to `http://127.0.0.1:7101` (`lendingd`), `:7102` (`swapd`), `:7103` (`governd`) and `:7104` (`consensusd`). Override them with `NHB_GATEWAY_LENDING_URL`, `NHB_GATEWAY_SWAP_URL`, `NHB_GATEWAY_GOV_URL` and `NHB_GATEWAY_CONSENSUS_URL`, or with `services` entries in the YAML. When `NHB_ENV` is not `dev`, every backend URL must start with `https://`. Rate limits are set per route key in the YAML (`rateLimits`); when the file defines none, the gateway uses 2, 1, 1 and 4 requests per second (bursts 20, 10, 10 and 40) for lending, swap, gov and consensus.

Other routes: `GET /healthz`, `/metrics` when observability is configured, `POST /v1/transactions/send` (forwards an `nhb_sendTransaction` request and accepts only transaction types `TxTypeTransfer` and `TxTypeTransferZNHB`), and a JSON-RPC compatibility route `/rpc` that maps method names such as `lending_*`, `swap_*`, `gov_*` and `consensus_*` to these REST routes (`gateway/compat/mapping.go`). Set the compatibility mode with `--compat-mode enabled|disabled|auto` or `NHB_COMPAT_MODE`.

## gRPC definitions

Protobuf sources are under `proto/`; `make proto` regenerates the Go stubs in `proto/` and the TypeScript clients in `clients/ts`.

| Proto package | Services | Server in this repository |
| --- | --- | --- |
| `consensus.v1` | `ConsensusService`, `QueryService` | `cmd/consensusd` |
| `network.v1` | `NetworkService` | `cmd/p2pd` |
| `gov.v1` | `Query`, `Msg` | `services/governd` |
| `lending.v1` | `LendingService` | `services/lending`, `services/lendingd` |
| `pos.v1` | `Realtime`, `Tx`, `Registry` | `Realtime` is served by the `cmd/nhb` RPC port; `Tx` and `Registry` are not registered anywhere |
| `swap.v1`, `tx.v1`, `nhb.fees.v1` | `SwapService`, `Query`, fee events | none |

## Auxiliary services

Under `services/`:

- `escrow-gateway`: HTTP service that submits the delegated escrow transactions (`TxTypeDelegatedCreateEscrow`, `TxTypeDelegatedReleaseEscrow`, `TxTypeDelegatedRefundEscrow`, `TxTypeDelegatedDisputeEscrow`) to a node with `nhb_sendTransaction` and pays their gas.
- `identity-gateway`: HTTP service; requests carry API key, signature, timestamp and idempotency headers.
- `governd`: gRPC server for the `gov.v1` API.
- `gov-keeper`: daemon that submits the governance finalize, queue and execute transactions once a proposal's timing allows, because nothing on-chain submits them.
- `lending`, `lendingd`: gRPC servers for the `lending.v1` API.
- `webhook`: Go package with a per-subscription delivery rate limiter.

## Clients

- `cmd/nhb-cli`: command-line client over JSON-RPC.
- `sdk/`: Go module `nhbchain/sdk` with gRPC clients (`sdk/consensus`, `sdk/network`, `sdk/gov`, `sdk/lending`, `sdk/swap`), JSON-RPC transaction helpers (`sdk/go/client`), and TypeScript wallet and identity-gateway helpers (`sdk/ts`).
- `clients/ts`: TypeScript clients generated from `proto/`.

Task-oriented walkthroughs with compiled code samples are in the [developer cookbook](./cookbooks/developers.md).
