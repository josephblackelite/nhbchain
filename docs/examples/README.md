# Example Applications

This page indexes what is actually in the `examples/` directory of this repository and how to run it. Every path below exists in the repository; there is no separate examples repository to clone.

## Two kinds of examples

1. **Go and TypeScript snippets** that talk to the chain directly (JSON-RPC, or the consensus gRPC service through `sdk/consensus`, `sdk/lending`).
2. **Web demos** (Next.js and Express) that call HTTP services such as an escrow, swap or creator REST gateway. Those HTTP services are not implemented in this repository, so a demo of this kind needs a compatible service that you supply, and its request signing cannot be checked against the code here. See [escrow-checkout.md](./escrow-checkout.md) for a worked example of that boundary.

## Layout

| Path | What it is |
| --- | --- |
| `examples/package.json` | Yarn workspace root (`@nhb/examples`, `packageManager` `yarn@1.22.19`). Workspaces: `lib-sdk`, `apps/*`, `wallet-lite`, `p2p-mini-market`, `merchant-loyalty-console`, `escrow-checkout/*`, `creator-studio`. |
| `examples/scripts/dev.js` | `yarn dev` runs the `dev` script of three workspaces: `@nhb/status-dashboard`, `@nhb/network-monitor` and `@nhb/p2p-mini-market`. |
| `examples/apps/status-dashboard`, `examples/apps/network-monitor` | Small dev servers (`dev-server.js`). Their ports come from `STATUS_DASHBOARD_PORT` and `NETWORK_MONITOR_PORT` in `examples/.env.example` (4300 and 4301). |
| `examples/lib-sdk` | Shared JS helpers used by the web demos. |
| `examples/wallet-lite`, `examples/p2p-mini-market`, `examples/merchant-loyalty-console`, `examples/creator-studio` | Next.js demos, each with its own README. |
| `examples/escrow-checkout/widget`, `examples/escrow-checkout/merchant-demo` | React widget and Express merchant server, see [escrow-checkout.md](./escrow-checkout.md). |
| `examples/freelance-board`, `examples/lending-dapp` | Next.js projects that are not listed in the workspace `workspaces` array; install and run them from their own directory. |
| `examples/docs/go/first_transaction`, `examples/docs/go/price_oracle_publish`, `examples/docs/ts/*.ts` | Go and TypeScript snippets used by the developer docs. |
| `examples/docs/ops/read_pauses`, `pause_toggle`, `quota_dump`, `swap_pause_inspect` | Operator helpers, see below. |
| `examples/clients/go/basic_consensus_client`, `examples/clients/ts/basic_consensus_client.ts` | Minimal consensus gRPC client. |
| `examples/txs/go/borrow.go`, `examples/txs/ts/supply.ts`, `examples/lending/quickstart.ts`, `examples/swap/redeem.ts`, `examples/gov/`, `examples/queries/` | Single-purpose transaction, query and governance snippets. |
| `examples/cookbook/go/main.go`, `examples/cookbook/js/index.mjs` | The cookbook scripts. |
| `examples/gateway/openapi.yaml`, `examples/postman/*.json` | An OpenAPI description and Postman collections. |
| `examples/compose/` | Docker Compose files, including `mininet/` (see `docs/cookbooks/operators.md`) and `lendingd.yml`. |

## Running the web demos

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

`.env.example` defines `NHB_RPC_URL`, `NHB_RPC_TOKEN`, `NHB_WS_URL`, `NHB_API_URL`, `NHB_CHAIN_ID`, `NHB_API_KEY`, `NHB_API_SECRET`, `NHB_WALLET_PRIVATE_KEY`, `NHB_WALLET_ADDRESS` and the two port variables. Its values are demo placeholders. Note that the file sets `NHB_CHAIN_ID` to a decimal value that is not the chain ID the node accepts (`0x4e4842`, `types.NHBChainID()`; see the [wallet builder guide](../sdk/wallets.md)); set it to the real chain ID before signing anything with these demos.

To run a single demo, use its own script, for example `yarn workspace @nhb/escrow-merchant-demo dev`. The `lint` and `test` scripts of the two `escrow-checkout` packages are placeholders that only print a TODO.

## Running the Go snippets

The Go programs are part of the repository's main Go module (`go.mod`), so run them from the repository root:

```bash
go run ./examples/docs/go/first_transaction
```

`first_transaction` reads the consensus gRPC address from the `CONSENSUSD_GRPC_ADDR` environment variable (`examples/docs/go/first_transaction/main.go`). See each file for the other environment variables it reads. For the gRPC transport requirements (shared secret or client certificate, `--allow-insecure` on a loopback listener) see [`docs/sdk/go.md`](../sdk/go.md).

## Operator helpers

All four read the consensus data directory (`--db`, default `./nhb-data`) and the consensus gRPC endpoint (`--consensus`, default `localhost:9090`).

- `go run ./examples/docs/ops/read_pauses` dumps the `system/pauses` map.
- `go run ./examples/docs/ops/pause_toggle --module <name> --state pause` stages a `gov.v1` `MsgSetPauses` transaction; use `--state resume` to lift the pause. Modules: `lending`, `swap`, `escrow`, `trade`, `loyalty`, `potso`, `transfer_nhb`, `transfer_znhb`. Extra flags: `--governance` (governance gRPC endpoint, default `localhost:50061`) and `--authority` (governance authority address).
- `go run ./examples/docs/ops/quota_dump --module <name> --address nhb1...` inspects quota usage for one address (`--epoch`, `--epoch-seconds` optional).
- `examples/docs/ops/swap_pause_inspect` prints the on-chain `pauses.swap` value and then calls `GET /v1/stable/status` on an off-chain HTTP service whose base URL is set by a flag (default `http://localhost:7074`). That service is not part of this repository, so the second half of the output needs one you provide.

## Related pages

- [overview.md](./overview.md), [cookbook.md](./cookbook.md), [wallets.md](./wallets.md), [wallet-lite.md](./wallet-lite.md), [creator-studio.md](./creator-studio.md), [freelance-board.md](./freelance-board.md), [p2p-mini-market.md](./p2p-mini-market.md).
- [`docs/sdk/examples.md`](../sdk/examples.md) for the SDK example programs.
