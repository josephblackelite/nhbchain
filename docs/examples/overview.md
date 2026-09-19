# Examples workspace

`/examples` is a Yarn v1 workspace plus a few standalone projects. This page covers the workspace itself, the shared SDK and the environment file. Start with the [index](README.md), which lists which examples still work against the current node.

## Layout

Workspace members (`workspaces` in `examples/package.json`):

| Path | What it is |
| --- | --- |
| `lib-sdk/` | `@nhb/examples-lib-sdk`: small JS helpers (see below). |
| `apps/status-dashboard/` | Plain Node HTTP server, default port 4300. |
| `apps/network-monitor/` | Plain Node HTTP server, default port 4301. |
| `wallet-lite/` | Next.js app, see [wallet-lite.md](wallet-lite.md). |
| `p2p-mini-market/` | Next.js app, see [p2p-mini-market.md](p2p-mini-market.md). |
| `merchant-loyalty-console/` | Next.js app with its own [README](../../examples/merchant-loyalty-console/README.md). |
| `escrow-checkout/widget`, `escrow-checkout/merchant-demo` | See [escrow-checkout.md](escrow-checkout.md). |
| `creator-studio/` | Next.js app, see [creator-studio.md](creator-studio.md). |

Not workspace members, each with its own `package.json`: `freelance-board/` ([freelance-board.md](freelance-board.md)) and `lending-dapp/` (its [README](../../examples/lending-dapp/README.md)).

Also present, not part of the workspace: Go and TypeScript programs under `cookbook/`, `docs/`, `clients/`, `gov/`, `queries/`, `swap/`, `txs/` and `lending/`; Postman collections in `postman/`; Docker Compose files in `compose/`; `gateway/openapi.yaml`.

## Quickstart

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

`yarn dev` runs `scripts/dev.js`, which starts exactly three apps with `concurrently`: `@nhb/status-dashboard`, `@nhb/network-monitor` and `@nhb/p2p-mini-market`. If any of them exits, the others are killed (`killOthers: ['failure', 'success']`). The other workspace members are started individually with `yarn workspace <name> run dev` (or `yarn dev` inside their directory). To add an app to `yarn dev`, add an entry to the `apps` array in `scripts/dev.js`.

`yarn test`, `yarn build` and `yarn lint` are `yarn workspaces run test|build|lint` (`scripts` in `examples/package.json`).

## Status dashboard and network monitor

Both load `examples/.env` (`dotenv` with path `../../.env` relative to the app). Both respond to any unmatched path with a plain-text route list.

Status dashboard (`STATUS_DASHBOARD_PORT`, default `4300`):

- `GET /health` returns `{ ok: true, service: 'status-dashboard', port }`.
- `GET /rpc-status` calls the RPC method `status` through `rpcClient`. The node's dispatcher (`rpc/http.go`) has no `status` method: it answers HTTP 404 (`unknown method status`, code -32601), the client throws `RPC request failed with status 404`, and the app returns HTTP 502.
- `GET /demo-signature` returns a UUID idempotency key, the header map for it, and the Bech32 decoding of `NHB_WALLET_ADDRESS` if set.

Network monitor (`NETWORK_MONITOR_PORT`, default `4301`):

- `GET /health`.
- `GET /metrics` echoes `NHB_RPC_URL`, `NHB_WS_URL`, `NHB_API_URL` and `NHB_CHAIN_ID`. It does not collect any metrics.
- `POST /simulate-sign` returns the request body, a timestamp, an HMAC signature computed with `NHB_API_SECRET` (falling back to the literal `demo-api-secret`), and, if `NHB_WALLET_PRIVATE_KEY` is set, a secp256k1 signature of the JSON body.

## Shared SDK (`lib-sdk`)

Exports (`lib-sdk/src/index.js`):

- `rpcClient({ baseUrl, apiKey, apiSecret, chainId, maxRetries = 2, fetchImpl })` returns `{ request(method, params, options) }`. It POSTs JSON-RPC 2.0, retries failed requests with exponential backoff (200 ms, 400 ms, ...), and times out after 10 s by default. It sends `x-nhb-timestamp` and `x-nhb-idempotency-key` on every request, plus `x-nhb-api-key`, `x-nhb-chain-id` and `x-nhb-signature` when the corresponding options are set.
- `hmacSign(body, secret, timestamp)` returns hex `HMAC-SHA256(secret, timestamp + ":" + body)`.
- `walletSig(message, privateKey)` returns a 64-byte secp256k1 signature (hex) over the SHA-256 of the message.
- `createIdempotencyKey()`, `idempotencyHeader(key)`, and `bech32Helpers` (`encode`, `decode`, `toWords`, `fromWords`).

The `x-nhb-*` request headers are a convention of these examples. No server in this repository reads `x-nhb-api-key`, `x-nhb-timestamp`, `x-nhb-signature`, `x-nhb-idempotency-key` or `x-nhb-chain-id`. The node's own request authentication is different:

- Privileged RPC methods (including `nhb_sendTransaction`) require `Authorization: Bearer <token>`; the node verifies it as a JWT (`requireAuth` in `rpc/http.go`; the checked-in `config.toml` sets `[RPCJWT]` to HS256 with the secret read from the env var named by `HSSecretEnv`, issuer `nhb-rpc`, audience `wallets`).
- The public swap methods listed in `isPublicSwapMethod` (`rpc/http.go`) use the `X-Api-Key`, `X-Timestamp`, `X-Nonce` and `X-Signature` headers verified by `gateway/auth`.

## Environment file

`examples/.env.example` (copy to `.env`; `.gitignore` in `examples/` covers it):

| Variable | Default in `.env.example` | Read by |
| --- | --- | --- |
| `NHB_RPC_URL` | `https://api.nhbcoin.net/rpc` | dashboard, monitor, wallet-lite, p2p-mini-market, creator-studio, loyalty console |
| `NHB_RPC_TOKEN` | `demo-token` | Next.js server routes, sent as `Authorization: Bearer` |
| `NHB_WS_URL` | `wss://api.nhbcoin.net/ws` | monitor (echo only); wallet-lite and p2p-mini-market parse it but do not open a socket |
| `NHB_API_URL` | `https://gw.nhbcoin.net` | monitor (echo only); wallet-lite profile route |
| `NHB_CHAIN_ID` | `14699254016670310680` | forwarded as a header by dashboard, wallet-lite, p2p-mini-market |
| `NHB_API_KEY`, `NHB_API_SECRET` | `demo-key`, `demo-secret` | dashboard, monitor (HMAC demo) |
| `NHB_WALLET_PRIVATE_KEY`, `NHB_WALLET_ADDRESS` | placeholders | monitor (`walletSig`), dashboard (`/demo-signature`) |
| `STATUS_DASHBOARD_PORT`, `NETWORK_MONITOR_PORT` | `4300`, `4301` | dashboard, monitor |

`NHB_RPC_TRUSTED_PROXIES`, `NHB_RPC_TRUST_PROXY_HEADERS`, `NHB_MEMPOOL_MAX_TX`, `NHB_RPC_TLS_CERT` and `NHB_RPC_TLS_KEY` also appear in `.env.example`, but no code in this repository reads them. The node reads the equivalent settings from `config.toml`: `RPCTrustedProxies`, `RPCTrustProxyHeaders`, `RPCTLSCertFile`, `RPCTLSKeyFile` (`config/config.go`) and `[mempool] MaxTransactions` (default 4000 when unset; `DefaultMempoolMaxTransactions`).

The chain ID the node accepts is fixed in code: `0x4e4842` (5130306), `nhbChainID` in `core/types/transaction.go`. `nhb_sendTransaction` rejects any other value with `transaction chainId does not match NHBCoin network`. The `NHB_CHAIN_ID` value in `.env.example` is not that number; the example apps only forward it as a header, and the node never reads that header.

The RPC hostnames above are what the example config ships with. The node itself listens on `RPCAddress` from `config.toml` (`127.0.0.1:8545` in the checked-in file) and serves JSON-RPC on `/`, plus WebSocket endpoints at `/ws/pos/finality` and `/ws/explorer` (`Serve` in `rpc/http.go`).

## Troubleshooting

- Missing `.env`: the status dashboard throws ``baseUrl` is required to create an RPC client.` at startup; the network monitor starts and echoes `undefined` values; wallet-lite and p2p-mini-market throw a configuration error on the first server request (their zod schemas require `NHB_RPC_URL`, `NHB_RPC_TOKEN` and `NHB_CHAIN_ID`; wallet-lite also requires `IDENTITY_EMAIL_SALT` and the `IDENTITY_GATEWAY_*` variables).
- `EADDRINUSE`: change `STATUS_DASHBOARD_PORT` or `NETWORK_MONITOR_PORT`, or stop the process holding the port.
- `walletSig` throws `Expected a 32-byte hex private key.` unless the key is 64 hex characters (a leading `0x` is optional).
