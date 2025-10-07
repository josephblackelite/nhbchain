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
- `admin`: security block configuring bearer tokens, TLS certificates, and optional mutual-TLS client validation for the admin API.

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

All admin endpoints require either a valid bearer token or a mutually-authenticated TLS connection. Requests without
credentials receive `401 Unauthorized` before reaching the business logic. Configure the security block in `services/swapd/config.yaml`:

```yaml
admin:
  bearer_token_file: /var/run/secrets/swapd-admin-token
  tls:
    cert: /etc/swapd/tls/tls.crt
    key: /etc/swapd/tls/tls.key
  mtls:
    enabled: true
    client_ca: /etc/swapd/tls/client-ca.crt
```

TLS is enabled by default whenever certificate and key paths are present. Disable it only for local development by setting
`admin.tls.disable: true`. When `mtls.enabled` is true and a client CA bundle is supplied, the server requires verified client
certificates signed by that authority.

Example authenticated requests:

```bash
curl https://swapd.example.com/admin/policy \
  --cert client.pem --key client.key \
  --cacert ca.pem
```

```bash
curl https://swapd.example.com/admin/throttle/check \
  -H 'Authorization: Bearer ${SWAPD_ADMIN_TOKEN}' \
  -H 'Content-Type: application/json' \
  -d '{"action":"mint"}' \
  --cacert ca.pem
```

All admin endpoints accept/return JSON. The throttle check endpoint returns `{ "allowed": true }` when the request is within
policy limits.

## Stable Engine Preview

The `/v1/stable/*` endpoints are published alongside the admin API and are currently guarded by a feature flag. When
`stable.paused=true` (the default in configuration), each endpoint returns `501 Not Implemented` with
`{"error": "stable engine not enabled"}`. The [Stable Funding API reference](stable-api.md) documents the success-path
contracts that will activate once the engine is unpaused.

### Pausing ZNHB redemption

Two independent switches must be considered when enabling or disabling redemptions:

* **On-chain:** `global.pauses.swap` (set via governance) halts swap module execution. When paused, any redemption transactions
  submitted by `swapd` will fail even if the HTTP API is reachable.
* **Service-side:** `swapd.stable.paused` (YAML `stable.paused`) keeps the `/v1/stable/*` endpoints disabled with a `501` response
  to prevent new intents from being created. Keep this flag set to `true` during readiness phases so OTC desks cannot cash out until go-live.

Production operations typically flip the service flag first to drain live traffic, then pause the on-chain module. Restoring
redemptions requires clearing both toggles so `swapd` can submit transactions and consensus will execute them. Use the helper to
inspect both switches in one command:

```bash
go run ./examples/docs/ops/swap_pause_inspect \
  --db ./nhb-data \
  --consensus localhost:9090 \
  --swapd https://swapd.internal.nhb
```

The CLI prints the current `global.pauses.swap` value and whether `/v1/stable/status` is returning a `501 stable engine not enabled`
payload (paused) or live counters (active).

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
