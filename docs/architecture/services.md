# Binaries and services

This page lists the executables built from this repository and the network
services they serve, as implemented in `cmd/` and `services/`.

## Node binaries

| Binary | Source | What it runs |
| --- | --- | --- |
| `nhb` | `cmd/nhb` | The all-in-one node: state processor, P2P server, BFT engine and the JSON-RPC/WebSocket server (`rpc.NewServer`). CI builds this binary (`.github/workflows/ci.yml`). Flags: `-config` (default `./config.toml`), `-genesis`, `-allow-autogenesis`, `-allow-migrate`. |
| `consensusd` | `cmd/consensusd` | State processor and BFT engine, exposing gRPC only. Flags: `-config`, `-genesis`, `-allow-autogenesis`, `-allow-migrate`, `-grpc` (default `127.0.0.1:9090`), `-p2p` (address of `p2pd`, default `localhost:9091`), `-allow-insecure`, and the duration overrides `-consensus-timeout-proposal`, `-consensus-timeout-prevote`, `-consensus-timeout-precommit` and `-consensus-timeout-commit` (`cmd/consensusd/main.go`). |
| `p2pd` | `cmd/p2pd` | The P2P server, exposing the internal `network.v1` gRPC service. Flags: `-config`, `-genesis`, `-allow-autogenesis`, `-grpc` (default `127.0.0.1:9091`), `-allow-insecure`. |

`-allow-insecure` is marked "DEV ONLY" in the flag help and permits plaintext
loopback connections.

## gRPC services

| Service | Proto package | Served by | Methods (from the `.proto`) |
| --- | --- | --- | --- |
| `ConsensusService` | `consensus.v1` | `consensusd` | `SubmitTxEnvelope`, `SubmitTransaction`, `GetValidatorSet`, `GetBlockByHeight`, `GetHeight`, `GetMempool`, `CreateBlock`, `CommitBlock`, `GetLastCommitHash` |
| `QueryService` | `consensus.v1` | `consensusd` | `QueryState`, `QueryPrefix` (server stream), `SimulateTx` |
| `NetworkService` | `network.v1` | `p2pd` | `Gossip` (bidirectional stream), `GetView`, `ListPeers`, `DialPeer`, `BanPeer` |
| `LendingService` | `lending.v1` | lending daemon (`services/lending`, `services/lendingd`) | see [lending gRPC](../api/lending-grpc.md) |
| `Query` and `Msg` | `gov.v1` | `governd` (`services/governd`) | `GetProposal`, `ListProposals`, `GetTally`; `SubmitProposal`, `Vote`, `SetPauses`, `Deposit` |
| `Realtime` | `pos.v1` | the node's RPC listener | `SubscribeFinality`; see [POS realtime](../api/pos-realtime.md) |

`swap.v1.SwapService` (`GetPool`, `ListPools`, `SwapExactIn`, `SwapExactOut`) and
the refund `Query` service in `proto/tx/tx.proto` are defined in `.proto` files but
no server in this repository registers them. The `pos.v1.Tx` and `pos.v1.Registry`
services are implemented in `rpc/pos_grpc.go` but are deliberately not registered
(see [POS gateway API](../api/gateway-pos.md)). There is no `swapd` binary in
`cmd/` or `services/`.

Default listen addresses in the code and sample configs: `consensusd`
`127.0.0.1:9090`, `p2pd` `127.0.0.1:9091`, `lendingd` `:50053`
(`services/lendingd/config/config.go`), `services/lending` `0.0.0.0:9444`,
`governd` `:50061` (`services/governd/config.yaml`), HTTP gateway `:8080`
(`gateway/config.yaml`). No default port table beyond these exists in code;
deployments set their own bind addresses.

## HTTP gateway

`cmd/gateway` (`-config`, `-compat-mode`, `-allow-insecure`) is a chi router
(`gateway/routes/router.go`). It serves `/healthz`, an optional JSON-RPC
compatibility handler on `/rpc`, `/metrics` when observability is enabled, and
one reverse-proxied route per entry in the config's `services` list, with
optional per-route rate limiting and JWT scope checks. The `/rpc` handler now
goes through the same rate limiter and authenticator middleware as the routes
it stands in for, so a caller needs a valid token to reach it
(`gateway/routes/router.go`). Routes named `lending`, `transactions` and
`consensus` also mount bridge handlers (`gateway/routes/lending.go`,
`transactions.go`, `wallet.go`).

## Protobuf layout

Each service owns its definitions under `proto/<domain>/v1` (`consensus`,
`network`, `lending`, `swap`, `gov`, `fees`) plus `proto/pos` and `proto/tx`.
`make proto` runs `go run ./tools/proto/gen.go`, which formats, lints, optionally
checks for breaking changes, and regenerates Go and TypeScript code. `make sdk`
runs `make proto` and then `go test ./...` in `sdk/`.

## Example clients

* `examples/clients/go/basic_consensus_client/main.go` dials a consensus service
  at `localhost:50051` with the Go SDK (`sdk/consensus`); adjust the address to
  your `consensusd` `-grpc` value.
* `examples/clients/ts/basic_consensus_client.ts` is the TypeScript equivalent.
