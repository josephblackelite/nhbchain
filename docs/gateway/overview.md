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
endpoints, auth secrets, and rate limits. When authentication is enabled the
gateway now defaults to rejecting anonymous requests unless specific path
prefixes are listed under `auth.optionalPaths`. This ensures operators
consciously opt in to any unauthenticated traffic and mirrors the tightened
defaults in `gateway/config.Config`.

```yaml
auth:
  enabled: true
  hmacSecret: ${GATEWAY_HMAC_SECRET}
  allowAnonymous: true
  optionalPaths:
    - /v1/lending/markets
    - /v1/lending/markets/get
```

The example above preserves anonymous access to the lending market catalogue and
market detail lookup endpoints while leaving every other route gated by bearer
tokens. Omit the block or set `allowAnonymous: false` to require authentication
for the entire surface.

The default ports for the internal services are:

| Service | Default URL |
| ------- | ----------- |
| `lendingd` | `http://127.0.0.1:7101` |
| `swapd` | `http://127.0.0.1:7102` |
| `governd` | `http://127.0.0.1:7103` |
| `consensusd` | `http://127.0.0.1:7104` |

Override these with environment variables (`NHB_GATEWAY_LENDING_URL`, etc.) or in
`gateway.yaml`.
