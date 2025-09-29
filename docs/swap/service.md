# Swap Service (swapd)

`swapd` orchestrates off-chain components required to mint and redeem vouchers safely. The daemon consumes price feeds from
configured oracle providers, aggregates them into a canonical median, records history in its own database, and forwards the
update to consensus using the configured transaction publisher.

## Responsibilities

- Maintain a fan-in aggregator across pluggable oracle sources (NOWPayments, CoinGecko, and future integrations).
- Persist raw price samples and derived medians into `/var/data/swapd.sqlite` (or a Postgres DSN when supplied).
- Enforce mint/redeem throttles using rolling windows that operations teams can tune via admin endpoints.
- Surface a lightweight HTTP API for monitoring, policy management, and health checks.

## Configuration

The daemon loads configuration from a YAML file (default `services/swapd/config.yaml`). Key sections include:

- `listen`: HTTP listen address (default `:7074`).
- `database`: SQLite or Postgres DSN for persistence.
- `oracle`: polling cadence, freshness window, and minimum feed count required to compute a median.
- `sources`: list of upstream oracle adapters and their credentials.
- `pairs`: currency pairs that should be published.
- `policy`: mint/redeem throttle settings.

A sample configuration is available at `services/swapd/config.yaml`.

## Database

The default SQLite database stores three tables:

- `oracle_samples`: raw quotes observed from each source.
- `oracle_snapshots`: aggregated medians, feed metadata, and proof hashes.
- `throttle_*`: rate-limiter policies and event logs for mint/redeem requests.

Operational tooling can query these tables directly or export them to CSV for reconciliation.

## Admin API

`swapd` exposes three primary administrative endpoints:

| Method | Path                    | Description                              |
| ------ | ----------------------- | ---------------------------------------- |
| GET    | `/healthz`              | Basic liveness check.                    |
| GET    | `/admin/policy`         | Retrieve the active throttle policy.     |
| PUT    | `/admin/policy`         | Replace the throttle policy.             |
| POST   | `/admin/throttle/check` | Reserve capacity for mint/redeem events. |

All admin endpoints accept/return JSON and should be protected by upstream ingress controls. The throttle check endpoint
returns `{ "allowed": true }` when the request is within policy limits.

## Stable Engine Preview

The `/v1/stable/*` endpoints are published alongside the admin API and are currently guarded by a feature flag. When
`stable.paused=true` (the default in configuration), each endpoint returns `501 Not Implemented` with
`{"error": "stable engine not enabled"}`. The [Stable Funding API reference](stable-api.md) documents the success-path
contracts that will activate once the engine is unpaused.

### Rate limits

The following soft limits apply per account in preview and production mode:

| Endpoint                 | Soft limit                               |
| ------------------------ | ---------------------------------------- |
| `POST /v1/stable/quote`  | 30 requests per minute                   |
| `POST /v1/stable/reserve`| 15 reservations per minute               |
| `POST /v1/stable/cashout`| 10 intents per minute                    |
| `GET /v1/stable/status`  | 60 requests per minute                   |
| `GET /v1/stable/limits`  | 60 requests per minute                   |

Limits are enforced through the throttle registry and surfaced via `/admin/policy`.

### Request/response examples

Quotes, reservations, and cash-out intents follow these shapes:

```http
POST /v1/stable/quote
Content-Type: application/json

{
  "asset": "ZNHB",
  "amount": 100,
  "account": "merchant-123"
}
```

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "quote_id": "q-1717706117123456000",
  "asset": "ZNHB",
  "price": 100.12,
  "expires_at": "2024-06-07T19:15:17Z",
  "trace_id": "1bde14ab-9f23-497d-a60c-018f4231a630"
}
```

Preview deployments instead return:

```http
HTTP/1.1 501 Not Implemented
Content-Type: application/json

{"error": "stable engine not enabled"}
```

End-to-end examples for reservations, cash-out intents, and status snapshots live in the [Stable Funding API reference](stable-api.md) and the [Stable Engine Runbook](../ops/stable-runbook.md).

### Troubleshooting

| Symptom | Likely cause | Action |
| ------- | ------------ | ------ |
| `501 stable engine not enabled` after enabling config | Swapd still running with cached config | Restart swapd pod/container and verify mounted config |
| `quote not found` from `/v1/stable/reserve` | Quote expired before reservation attempt | Regenerate quote and confirm TTL in `/v1/stable/limits` |
| Missing `trace_id` in responses | OTEL exporter unreachable | Restore telemetry pipeline and re-run requests |

Use `make audit:endpoints` to reproduce and capture request/response logs when investigating incidents.
