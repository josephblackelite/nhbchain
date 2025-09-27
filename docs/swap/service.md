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

`swapd` exposes three primary endpoints:

| Method | Path                    | Description                              |
| ------ | ----------------------- | ---------------------------------------- |
| GET    | `/healthz`              | Basic liveness check.                    |
| GET    | `/admin/policy`         | Retrieve the active throttle policy.     |
| PUT    | `/admin/policy`         | Replace the throttle policy.             |
| POST   | `/admin/throttle/check` | Reserve capacity for mint/redeem events. |

All admin endpoints accept/return JSON and should be protected by upstream ingress controls. The throttle check endpoint
returns `{ "allowed": true }` when the request is within policy limits.
