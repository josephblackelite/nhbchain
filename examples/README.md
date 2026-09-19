# NHB examples

Demo applications, a small JS helper library and Go/TypeScript sample programs. The guides live in [`docs/examples`](../docs/examples/README.md); start there, because several examples call RPC methods that the node has retired and no longer work.

## Prerequisites

- Yarn v1 (`packageManager` is `yarn@1.22.19` in `package.json`) and Node.js with a global `fetch`.
- Go at the version in the root `go.mod` for the Go programs. They belong to the root module, so run them from the repository root.

## Getting started

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

`yarn dev` (`scripts/dev.js`) starts three apps: `@nhb/status-dashboard` (port `STATUS_DASHBOARD_PORT`, default 4300), `@nhb/network-monitor` (`NETWORK_MONITOR_PORT`, default 4301) and `@nhb/p2p-mini-market`. Each of the first two exposes `GET /health`.

The sample `.env.example` points at `https://api.nhbcoin.net/rpc` (JSON-RPC), `wss://api.nhbcoin.net/ws` (WebSocket) and `https://gw.nhbcoin.net` (REST). See [docs/examples/overview.md](../docs/examples/overview.md) for every variable, which app reads it, and which entries in `.env.example` are not read by any code.

## Workspace layout

Workspace members (`workspaces` in `package.json`): `lib-sdk`, `apps/*`, `wallet-lite`, `p2p-mini-market`, `merchant-loyalty-console`, `escrow-checkout/*`, `creator-studio`.

Standalone, with their own `package.json`: `freelance-board`, `lending-dapp`.

Other directories: `cookbook/` (Go and JS RPC scripts), `docs/` (Go and TS snippets, `docs/ops` operator programs), `clients/`, `gov/`, `queries/`, `swap/`, `txs/`, `lending/`, `postman/`, `compose/`, `gateway/openapi.yaml`.

## Operator tooling

Go programs in `examples/docs/ops`. Run them from the repository root. `read_pauses`, `pause_toggle`, `quota_dump` and `swap_pause_inspect` open the node's data directory directly (`--db`, default `./nhb-data`) and read the latest block over gRPC (`--consensus`, default `localhost:9090`).

- `go run ./examples/docs/ops/read_pauses` prints the `system/pauses` parameter: `lending`, `swap`, `escrow`, `trade`, `loyalty`, `potso`, `transfer_nhb`, `transfer_znhb`, `staking`. If the parameter is unset it prints `no pause overrides set (all modules active)`.
- `go run ./examples/docs/ops/pause_toggle --authority <address> --module <name> --state pause|resume` builds a `gov.v1 MsgSetPauses` and submits it to governd (`--governance`, default `localhost:50061`). `--authority` and `--module` are required. `--state` accepts `pause`/`paused`/`on` and `resume`/`unpause`/`off`. The message type (`govv1.Pauses`) carries only `lending`, `swap`, `escrow`, `trade`, `loyalty` and `potso`.
- `go run ./examples/docs/ops/quota_dump --module <name> --address <nhb1...>` prints the quota counters for one address in one epoch. Optional `--epoch` (defaults to the current epoch) and `--epoch-seconds` (default 60).
- `go run ./examples/docs/ops/swap_pause_inspect` prints `global.pauses.swap` from the pause parameter and the outcome of a status request to the service URL given by `--swapd` (default `http://localhost:7074`).

## Developing new examples

1. Create a directory (for a workspace app, under `apps/`) with its own `package.json` and a `dev` script.
2. Add the package name to `apps` in `scripts/dev.js` if it should start with `yarn dev`, and to `workspaces` in `package.json` if it should be a workspace member.
3. Import helpers from `@nhb/examples-lib-sdk` (see the overview for what it exports).

## Tests

`yarn test` runs `yarn workspaces run test` (`scripts.test` in `package.json`); `yarn build` and `yarn lint` are wired the same way.

## Troubleshooting

- `.env` must exist; the status dashboard fails at startup without `NHB_RPC_URL`.
- `EADDRINUSE`: change `STATUS_DASHBOARD_PORT` or `NETWORK_MONITOR_PORT`.
- `yarn dev` runs long-lived processes; stop them with `Ctrl+C`.
