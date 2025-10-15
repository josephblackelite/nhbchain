# Gateway Overview

> [!WARNING]
> The legacy `/rpc` compatibility endpoint is scheduled for removal. Review the [JSON-RPC decommission timeline](../migrate/deprecation-timeline.md) and migrate to the scoped service APIs before Phase D completes.

The API gateway acts as the single public edge for NHBChain traffic. Requests
arriving at `https://app.nhbcoin.com` are served by the gateway and are routed
internally to dedicated services:

| Public host | Purpose | Internal target |
| ----------- | ------- | ---------------- |
| `app.nhbcoin.com` | User-facing application and REST APIs | Gateway (this service) |
| `rpc.nhbcoin.net` | Legacy JSON-RPC compatibility | Gateway `/rpc` endpoint |
| `api.nhbcoin.net` | Historical JSON-RPC host (deprecated) | Gateway `/rpc` endpoint |
| `nhbcoin.net` | On-chain service endpoints (`/v1/...`) | Gateway reverse proxy |

The gateway supersedes the legacy JSON-RPC node in the service-oriented topology. Traffic is routed
according to the following prefixes:

- `/v1/lending/*` → `lendingd`
- `/v1/swap/*` → `swapd`
- `/v1/gov/*` → `governd`
- `/v1/consensus/*` → `consensusd`

Each backend is responsible for a single domain. The gateway enforces
cross-cutting concerns including authentication (JWT/OAuth bearer tokens),
per-route rate limits, tracing and metrics, and a compatibility layer for legacy
JSON-RPC methods.

## Observability

The gateway publishes Prometheus metrics at `/metrics` and emits OpenTelemetry
traces for every request. Metrics and tracing are enabled by default and can be
turned off in the gateway configuration.

## Configuration

The gateway is configured via YAML (or environment variables) to set backend
endpoints, auth secrets, and rate limits. The default ports for the internal
services are:

| Service | Default URL |
| ------- | ----------- |
| `lendingd` | `http://127.0.0.1:7101` |
| `swapd` | `http://127.0.0.1:7102` |
| `governd` | `http://127.0.0.1:7103` |
| `consensusd` | `http://127.0.0.1:7104` |

Override these with environment variables (`NHB_GATEWAY_LENDING_URL`, etc.) or in
`gateway.yaml`. These HTTP defaults exist strictly for local development. When
`NHB_ENV` is anything other than `dev`, every service endpoint **must** use
`https://` and present a valid certificate; the gateway now refuses to boot if a
plaintext endpoint is configured. Operators should provision TLS material for
each backend (or terminate TLS in front of the gateway and configure
mutual-TLS-terminating proxies) before rolling out to staging or production.

The CLI `--allow-insecure` escape hatch is likewise restricted to dev-only
loopback testing. The binary exits immediately if the flag is supplied without
`NHB_ENV=dev` or when binding to anything other than `127.0.0.1`/`::1`, and it
logs a warning whenever TLS is disabled.
