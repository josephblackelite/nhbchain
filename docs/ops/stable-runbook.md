# Stable Engine Runbook

This runbook explains how to exercise the `/v1/stable/*` suite on localnet, publish transparency artifacts, and unpause the stable engine safely. Use it alongside the [Stable Funding API reference](../swap/stable-api.md) and swapd service documentation.

## Pre-flight Checklist

- [ ] Confirm the governance proposal that authorises enabling the stable engine has passed.
- [ ] Validate swapd configuration (`services/swapd/config.yaml`) contains the target asset(s). Keep `stable.paused=true` in readiness phases so ZNHB redemption stays off until the governance go-live window.
- [ ] Ensure OTEL exporters and log sinks are reachable; the regression suite relies on span IDs for traceability.

## Starting localnet

```bash
docker compose -f deploy/compose/docker-compose.yml up -d swapd
```

The command builds the swapd container and exposes port `7074`. When running the full regression plan use the convenience wrapper:

```bash
make up
```

`make down` tears the stack down and removes volumes once testing completes.

## Endpoint regression (`make audit:endpoints`)

With swapd running locally, execute:

```bash
make audit:endpoints
```

The target runs [Newman](https://github.com/postmanlabs/newman) via `npx`, hits every stable endpoint, and writes:

- `logs/audit-endpoints.log`: terminal transcript for compliance reviews.
- `artifacts/endpoints/newman-report.json`: raw request/response archive (importable into Postman or data lakes).

Override the base URL when targeting non-local deployments:

```bash
make audit:endpoints STABLE_BASE=https://swapd.internal.nhb
```

After the run completes export the artifacts to long-term storage alongside validator metrics.

## Operational changes

1. Toggle `stable.paused=false` and redeploy swapd once the change ticket is authorised.
2. Re-run `make audit:endpoints` to capture the now-200 responses and confirm quote→reserve→cashout flows populate the audit trail.
3. Monitor Grafana (`Stable ▸ Engine overview`) and alerting rules for anomalies.
4. File a signed change ticket attaching the Newman JSON, OTEL trace IDs, and governance approval references.

When drills require keeping redemptions disabled, revert the overlay to `stable.paused=true` and use the combined helper to verify both toggles:

```bash
go run ./examples/docs/ops/swap_pause_inspect \
  --db ./nhb-data \
  --consensus localhost:9090 \
  --swapd https://swapd.internal.nhb
```

The CLI prints `global.pauses.swap` plus the live `/v1/stable/status` response so responders can confirm swapd is still rejecting redemptions with `501 stable engine not enabled` during readiness windows.

## Troubleshooting

| Symptom | Likely cause | Mitigation |
| ------- | ------------ | ---------- |
| Newman exits with `ECONNREFUSED` | swapd not running or port blocked | Check `docker compose ps`, restart swapd, or point `STABLE_BASE` to the reachable host |
| Endpoints return HTTP 429 | Throttle window exceeded | Inspect `/admin/policy`, adjust via authorised process, retry after window resets |
| Cash-out intent missing `trace_id` | OTEL collector offline | Restore OTEL pipeline and retry intent creation |
| `stable engine not enabled` persists after unpausing | Config not mounted or swapd still reading cached config | Restart swapd container/pod and confirm config map version |

## Audit exports

When escalations occur, attach the following artefacts to the incident or compliance ticket:

- Newman report (`artifacts/endpoints/newman-report.json`).
- swapd logs filtered by `cashout intent created` and `stable.reserve_quote`.
- Grafana snapshot for the time window in question.
- Governance or policy change references authorising overrides.

Maintaining this paper trail satisfies the transparency appendix documented in the Stable Funding API reference.
