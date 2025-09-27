# NHB Examples Workspace

The `/examples` directory contains a Yarn workspace with runnable demo applications and a shared SDK for connecting to NHB RPC/REST gateways.

## Layout

- `examples/apps/` – UI/server examples that showcase different integration patterns. Each application owns its dependencies and a `dev` script.
- `examples/lib-sdk/` – Shared helper library that exposes RPC clients, signing helpers, and Bech32 utilities.
- `examples/scripts/` – Tooling that keeps the workspace consistent, including the `yarn dev` orchestrator.
- `examples/p2p-mini-market/` – Dual-lock escrow demo that accepts NHB ⇄ ZNHB trades and walks through funding, settlement, and dispute outcomes.

## Quickstart

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

The first run installs dependencies, generates the SDK build artifacts (if any), and starts all demo applications. Each app loads configuration from the shared `.env` file and watches for changes.

## Environment variables

| Variable | Description |
| --- | --- |
| `NHB_RPC_URL` | JSON-RPC endpoint for the NHB gateway (HTTP). |
| `NHB_WS_URL` | WebSocket JSON-RPC endpoint used for subscriptions and streaming. |
| `NHB_API_URL` | REST gateway that powers escrow, swap, and loyalty flows. |
| `NHB_CHAIN_ID` | Numeric chain identifier attached to signed requests. |
| `NHB_API_KEY` | Gateway API key used for authenticated requests. |
| `NHB_API_SECRET` | Secret used to compute the HMAC signature header. |
| `NHB_WALLET_PRIVATE_KEY` | Optional wallet private key for demo transactions. |
| `NHB_WALLET_ADDRESS` | Wallet Bech32 address that corresponds to the private key. |
| `STATUS_DASHBOARD_PORT` | HTTP port for the status dashboard example. |
| `NETWORK_MONITOR_PORT` | HTTP port for the network monitor example. |

### Default endpoints

The `.env.example` template points at the public NHB infrastructure so a fresh clone works out-of-the-box:

- `https://rpc.testnet.nhbcoin.net` for HTTP JSON-RPC calls
- `wss://rpc.testnet.nhbcoin.net/websocket` for WebSocket subscriptions
- `https://api.nhbcoin.net/escrow/v1` for REST and gateway integrations

Rotate the demo API credentials before deploying to production.

### Pointing to self-hosted infrastructure

If you are running your own RPC or gateway stack, update the `.env` file with your endpoints and chain ID. Each app reads the shared `.env`, so no other configuration changes are required. When fronting the services with custom domains:

1. Issue TLS certificates (ACM recommended on AWS) that cover your domains.
2. Update the `NHB_RPC_URL`, `NHB_WS_URL`, and `NHB_API_URL` values to use the new hostnames.
3. Restart `yarn dev` or reload the individual apps to pick up the changes.

> **Tip:** Never commit the `.env` file to version control. The `.env.example` template intentionally uses placeholder credentials.

### CORS and rate limits

- Enable CORS on the REST gateway for the origins that host your demo applications. For production, prefer an API gateway or reverse proxy that enforces stricter allowlists.
- Apply rate limits and Web Application Firewall (WAF) rules, especially on write paths such as swaps or escrow releases. The provided AWS notes in `examples/README.md` outline recommended ALB/NLB placements and Shield integration.

## Running locally

- `yarn dev` orchestrates every application with a single command. The workspace keeps ports configurable to avoid clashes with other services.
- `yarn test` will execute the `test` script for every workspace package.
- Add new applications inside `apps/` and update `scripts/dev.js` so that `yarn dev` includes them automatically.

## Security notes

- Rotate `NHB_API_SECRET` regularly and do not share real secrets in sample code.
- HMAC headers include timestamps and idempotency keys to prevent replay attacks. The SDK exposes helpers that generate compliant headers.
- When distributing demo applications, ensure you proxy RPC requests through controlled infrastructure.

## Common pitfalls

- Forgetting to copy `.env.example` to `.env` results in missing gateway configuration.
- Wrong ports cause `EADDRINUSE` errors. Adjust the `*_PORT` variables or stop the conflicting process.
- Make sure `NHB_WALLET_PRIVATE_KEY` is hexadecimal with the `0x` prefix; otherwise the signing helper throws an error.
