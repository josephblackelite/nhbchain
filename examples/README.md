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

### Local node configuration knobs

If you are pointing the workspace at a validator that you operate locally:

- Set `NHB_RPC_TRUSTED_PROXIES` to the IPs of any reverse proxies (for example,
  `127.0.0.1` when running Caddy or nginx on the same host). The node only
  honours forwarded client IPs from this list when `RPCTrustProxyHeaders` is
  enabled in `config.toml`.
- Leave `NHB_RPC_TRUST_PROXY_HEADERS=false` until you have verified the proxy
  chain strips inbound `X-Forwarded-For` headers from clients.
- Mirror your desired mempool ceiling in `NHB_MEMPOOL_MAX_TX` and update the
  node’s `[mempool] MaxTransactions` accordingly so tooling and documentation
  stay in sync.
- Provide `NHB_RPC_TLS_CERT` and `NHB_RPC_TLS_KEY` when testing HTTPS locally.
  The node loads these paths via `RPCTLSCertFile` / `RPCTLSKeyFile`.

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
