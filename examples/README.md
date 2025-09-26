# NHB Examples Workspace

This directory contains self-contained applications and a shared SDK that demonstrate how to interact with the NHB blockchain.

## Prerequisites

- [Node.js](https://nodejs.org/) 18 or newer
- [Yarn](https://classic.yarnpkg.com/lang/en/docs/install/) (v1)

## Getting started

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

`yarn dev` starts every application in the workspace. Each app watches the shared `.env` file and exposes a health endpoint.

The sample configuration defaults to the public NHB infrastructure:

- `https://api.nhbcoin.net/rpc` for JSON-RPC (HTTP)
- `wss://api.nhbcoin.net/ws` for JSON-RPC (WebSocket)
- `https://gw.nhbcoin.net` for REST, creator, escrow, swap, and loyalty flows

## Environment

The `.env` file is shared across every app in the workspace. See [docs/examples/overview.md](../docs/examples/overview.md) for an explanation of all variables and how to target self-hosted endpoints.

## Workspace layout

```
examples/
  apps/                 # runnable demo applications
  lib-sdk/              # shared JS helpers for NHB RPC and REST
  scripts/              # tooling shared by the workspace
```

## Developing new examples

1. Create a new directory inside `apps/` with its own `package.json`.
2. Add a `dev` script that starts the local UI/server.
3. Update `scripts/dev.js` with the new workspace entry so it is included in `yarn dev`.
4. Import utilities from `lib-sdk` for consistent RPC/HMAC behaviour.

## Running tests

Each workspace package can expose its own `test` script. To run the whole test matrix use:

```bash
yarn test
```

## Troubleshooting

- Ensure the `.env` file exists and contains the gateway credentials.
- Ports are configurable through environment variables (`STATUS_DASHBOARD_PORT`, `NETWORK_MONITOR_PORT`).
- `yarn dev` uses long-running processes; stop them with `Ctrl+C`.

## AWS deployment notes

- Host the example gateway workloads on **ECS Fargate** or **EKS** for managed scaling and IAM integration.
- Use **Route 53** records that map to your load balancers:
- `api.nhbcoin.net` → Application or Network Load Balancer for HTTP/WS JSON-RPC traffic.
- `gw.nhbcoin.net` → Application Load Balancer for REST and gateway APIs (escrow, loyalty, creator, swap).
- Request ACM certificates that cover `*.nhbcoin.net` and enable AWS Shield along with WAF rules. Rate-limit or geo/IP allowlist sensitive write paths.
