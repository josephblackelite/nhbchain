# JSON-RPC Compatibility Decommission Timeline

> [!WARNING]
> The legacy JSON-RPC surface is in its sunset window. Monitor the phase schedule below and migrate to the REST and gRPC APIs before the final removal milestone.

The compatibility dispatcher that backs the `/rpc` endpoint is being retired in four phases over ninety days. Each stage tightens the defaults and culminates in the complete removal of the monolithic surface.

| Phase | Offset | Default behaviour | Operator flag | Key actions |
| ----- | ------ | ----------------- | ------------- | ----------- |
| Phase A – Compatibility warning window | T+0 | Compatibility **enabled** by default | `--compat-mode=disabled` (optional opt-out) | Emit HTTP warnings, publish docs banner, capture support feedback. |
| Phase B – Staging opt-out | T+30d | Compatibility **enabled** by default | `--compat-mode=disabled` in staging | Exercise the REST APIs, harden SDKs, update staging pipelines. |
| Phase C – Compatibility disabled by default | T+60d | Compatibility **disabled** by default | `--compat-mode=enabled` (temporary opt-in) | Production workloads must migrate; only stragglers use the flag. |
| Phase D – Compatibility removal | T+90d | Compatibility **removed** | Flag removed | Remove dispatcher, tag final release, and announce completion. |

## Operator guidance

- The gateway now accepts a CLI flag (`--compat-mode`) or environment override (`NHB_COMPAT_MODE`) with the values `enabled`, `disabled`, or `auto`.
- During Phase A and B the default remains `enabled`. Use `--compat-mode=disabled` in staging to verify that workloads are not tied to JSON-RPC.
- In Phase C the default flips to `disabled`. Opt in explicitly with `--compat-mode=enabled` for short-lived migrations.
- Phase D eliminates the dispatcher entirely; upgrade before this milestone.

## Client impact

All compatibility responses include the following headers to ensure automated clients see the warning:

- `Warning: 299 - "Monolithic JSON-RPC compatibility will be sunset in 90 days. Migrate to the service APIs and monitor the deprecation timeline."`
- `Link: <https://docs.nhbchain.net/migrate/deprecation-timeline>; rel="deprecation"; type="text/html"`
- `X-NHB-Compat-Phase: Phase A – Compatibility warning window`

SDKs, API clients, and Postman collections should surface these headers to downstream teams. If you operate shared tooling, coordinate the migration timeline with your stakeholders and raise support tickets early.
