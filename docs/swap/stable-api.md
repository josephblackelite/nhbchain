# Stable Funding API

The stable swap engine powers voucher liquidity, reserves cash-out intents, and records receipts for downstream treasury teams. The engine now runs in production for OTC desks whenever `swapd` is started with `stable.paused = false`. Keep the flag at `true` during readiness phases so cash-out requests fail fast with `501 stable engine not enabled` while governance finalises launch approvals. A small number of guard rails remain (soft quotas and throttles are described below), but the HTTP surface is live and wired directly to the in-memory `stable.Engine`.

This document captures the HTTP surface for `/v1/stable/*`, expected request/response shapes, rate limits, and the transparency hooks that bind quotes to reservations, cash-out intents, and banking receipts. Where applicable we also call out the behaviour when the engine is disabled so that playbooks remain accurate for dry runs.

## Authentication

Stable API requests are authenticated with per-partner HMAC headers. Each desk receives an API key and shared secret during onboarding. Every HTTP request must include the following headers:

| Header | Purpose |
| ------ | ------- |
| `X-Api-Key` | Identifies the partner making the request. |
| `X-Timestamp` | Unix timestamp (seconds) when the signature was produced. |
| `X-Nonce` | Unique nonce for replay protection. |
| `X-Signature` | Hex-encoded HMAC-SHA256 signature covering the request. |

The signature is computed as `HMAC_SHA256(secret, timestamp + "\n" + nonce + "\n" + method + "\n" + path + "\n" + body)` using the canonical request path (no query string) and the exact request body bytes. Swapd enforces a 1 MiB signing limit, a two minute timestamp skew window, and persistent nonce tracking. Requests with missing headers, reused nonces, or invalid signatures are rejected with `401 unauthorized`.

API keys are allow-listed in `swapd` configuration. Requests carrying an unknown key return `403 forbidden` with `{"error": "partner not allowed"}`. Desks should monitor for these responses during rollout—they usually indicate a mismatched credential or configuration drift between staging and production.

## Endpoints at a Glance

| Method | Path                  | Description                                            | Response when enabled | Response when paused | Rate limits* |
| ------ | --------------------- | ------------------------------------------------------ | --------------------- | -------------------- | ------------ |
| POST   | `/v1/stable/quote`    | Calculate an indicative redemption price for vouchers | `200` with quote payload | `501` + `stable engine not enabled` | 30 requests/min per account |
| POST   | `/v1/stable/reserve`  | Reserve a previously issued quote for settlement      | `200` with reservation payload | `501` + `stable engine not enabled` | 15 reservations/min per account |
| POST   | `/v1/stable/cashout`  | Create a cash-out intent and receipt stub              | `200` with intent payload | `501` + `stable engine not enabled` | 10 intents/min per account |
| GET    | `/v1/stable/status`   | Provide in-memory counters for quotes and reservations | `200` with status snapshot | `501` + `stable engine not enabled` | 60 requests/min |
| GET    | `/v1/stable/limits`   | Advertise soft mint/redeem policies                    | `200` with limits payload | `501` + `stable engine not enabled` | 60 requests/min |

\*Soft limits are enforced through swapd's throttle registry; see the [operations runbook](../ops/stable-runbook.md) for override procedures.

Even when the engine is paused the collection described below asserts that responses stay consistent—any drift in status codes or error contracts will fail regression.

> **Note:** All responses include `trace_id` when the request carries OpenTelemetry span context. Treasury processors rely on this field to correlate API calls with downstream settlements.

## POST `/v1/stable/quote`

Request body:

```json
{
  "asset": "ZNHB",
  "amount": 100.0,
  "account": "merchant-123"
}
```

Successful response (engine enabled):

```json
{
  "quote_id": "q-1717787718000000000",
  "asset": "ZNHB",
  "price": 100,
  "expires_at": "2024-06-07T19:16:18Z",
  "trace_id": "102030405060708090a0b0c0d0e0f001"
}
```

Preview response (engine paused):

```json
{
  "error": "stable engine not enabled"
}
```

Audit trail linkage: quotes generate OpenTelemetry spans labelled `stable.price_quote` and emit meter samples to `swapd_stable_quote_latency`. The `trace_id` returned above corresponds to the span context propagated to treasury processors.

## POST `/v1/stable/reserve`

Request body:

```json
{
  "quote_id": "q-1717787718000000000",
  "amount_in": 100.0,
  "account": "merchant-123"
}
```

Successful response:

```json
{
  "reservation_id": "q-1717787718000000000",
  "quote_id": "q-1717787718000000000",
  "amount_in": 100,
  "amount_out": 100,
  "expires_at": "2024-06-07T19:16:18Z",
  "trace_id": "102030405060708090a0b0c0d0e0f001"
}
```

Preview response mirrors the `quote` endpoint, returning a `501` error with `stable engine not enabled`.

Telemetry: reservations log `stable.reserve_quote` spans with `reservation.id` attributes and attach to the quote span via `traceparent`. Failed reservations (expired, already consumed) set `status=Error` so that the audit dashboard surfaces anomalies.

## POST `/v1/stable/cashout`

Request body:

```json
{
  "reservation_id": "q-1717787718000000000"
}
```

Successful response:

```json
{
  "intent_id": "i-1717787724000000000",
  "reservation_id": "q-1717787718000000000",
  "amount": 100,
  "created_at": "2024-06-07T19:15:24Z",
  "trace_id": "102030405060708090a0b0c0d0e0f001"
}
```

Preview response returns the same `501` payload as other endpoints.

The cash-out intent closes the audit trail by linking reservation IDs to treasury receipts. Intent creation logs `stable.create_cashout_intent` spans, persists the intent in the swapd database, and emits structured logs (`cashout intent created`) that the audit pipeline ingests. Treasury operators attach an ACH/SWIFT hint to the intent when the downstream payment rail is confirmed; until then the API exposes the creation timestamp only.

## GET `/v1/stable/status`

Response (engine enabled):

```json
{
  "quotes": 1,
  "reservations": 1,
  "assets": 1,
  "updated_at": "2024-06-07T19:15:27Z"
}
```

Preview response:

```json
{
  "error": "stable engine not enabled"
}
```

`status` surfaces live counters and is wired to Grafana panels under `Stable ▸ Engine overview`. The endpoint remains read-only and does not require authentication on localnet fixtures.

## GET `/v1/stable/limits`

When enabled the limits endpoint echoes the soft mint/redeem thresholds configured in `services/swapd/config.yaml`:

```json
{
  "daily_cap": 1000000,
  "asset_caps": {
    "ZNHB": {
      "max_slippage_bps": 50,
      "quote_ttl_seconds": 60,
      "soft_inventory": 1000000
    }
  }
}
```

In preview the endpoint returns the `stable engine not enabled` error. Operations teams can cross-reference this payload with the governance proposals that authorise cap changes.

## Error Catalogue

| Status | Error message                 | Scenario                            | Mitigation |
| ------ | ----------------------------- | ----------------------------------- | ---------- |
| 400    | `invalid payload`             | Malformed JSON or missing fields    | Validate inputs client-side |
| 404    | `quote not found`             | Reserve/cashout on unknown quote    | Fetch latest quote from `/v1/stable/quote` |
| 409    | `quote expired`               | Attempting to reserve stale quote   | Regenerate quote before TTL lapses |
| 422    | `reservation not found`       | Cashout on consumed reservation     | Inspect `/v1/stable/status` counters |
| 429    | `partner quota exceeded`      | Partner-specific mint cap breached  | Pause new reservations or request a quota increase |
| 429    | `throttled`                   | Breach of per-account policy        | Contact operations or wait for rolling window |
| 501    | `stable engine not enabled`   | Preview mode guardrail              | Flip `stable.paused=false` and run regression suite |

## Quotas and Telemetry

Swapd enforces two layers of guard rails:

- `/v1/stable/limits` echoes the global soft caps configured under `policy.*` and `stable.assets`. These values govern aggregate desk activity across the programme.
- Per-partner quotas are tracked in the new `partner_quota_usage` table. Each reservation consumes from the desk's daily allowance; once exhausted, swapd returns `429 partner quota exceeded` and rolls back the reservation so ledgers remain unchanged. Quota counters survive process restarts and reset automatically at UTC day boundaries.

Stable counters (`/v1/stable/status`) increment as quotes, reservations, and intents are processed. They feed Grafana dashboards via OTLP metrics (`swapd_stable_quote_latency`, `swapd_stable_reserve_latency`, and `swapd_stable_cashout_intent_latency`) and structured logs (`cashout intent created`).

## OTC Partner Onboarding

To onboard a new OTC desk to the stable API:

1. **Submit desk profile:** Treasury reviews the desk's legal entity, settlement accounts, and compliance artefacts. Once approved, operations set `stable.paused = false` in the deployment environment and ensure the desk's account identifier matches the `account` field used in `quote`/`reserve` payloads.
2. **Provision credentials:** Operations add the desk to `stable.partners` in `services/swapd/config.yaml`, assigning an API key, shared secret, and daily quota. Swapd hot-reloads the configuration and begins accepting HMAC-authenticated requests immediately.
3. **Configure limits:** Tune `policy.mint_limit`/`policy.redeem_limit` and, if necessary, add a dedicated entry under `stable.assets` with bespoke `quote_ttl`, `max_slippage_bps`, or `soft_inventory`. The resulting global limits propagate automatically to `/v1/stable/limits`.
4. **Exchange connectivity details:** OTC desks provide network allow-list information and preferred telemetry endpoints. Operations update the gateway ACLs and share the OpenTelemetry trace requirements so partners can forward `traceparent` headers, enabling `trace_id` propagation end-to-end.

During smoke testing desks should verify a signed `GET /v1/stable/status` request succeeds and that `429 partner quota exceeded` surfaces once the configured allowance is consumed. Treasury can reset quotas by editing the partner configuration or waiting for the next UTC day boundary.

Partners should verify integration by calling `/v1/stable/status` and `/v1/stable/limits` after onboarding to confirm the live quotas before sending production traffic.

## Transparency Appendix

Auditors should be able to trace a voucher from creation through redemption:

1. **Voucher mint** → stored on-chain as part of the ledger (see `tests/ledger` fixtures).
2. **Quote issuance** → `/v1/stable/quote` returns `quote_id` + `trace_id`; span exported as `stable.price_quote`.
3. **Reservation** → `/v1/stable/reserve` ties the `quote_id` to an account and records `reservation_id`.
4. **Cash-out intent** → `/v1/stable/cashout` yields `intent_id`, linking reservations to treasury instructions.
5. **Receipt** → treasury uploads ACH/SWIFT receipt referencing `intent_id`; logs feed the audit data lake.

Governance controls include:

- Swapd configuration stored in Git and gated by multi-sig approvals.
- Throttle overrides tracked via `/admin/policy` with signed change tickets.
- Automated regression (`make audit:endpoints`) that captures request/response archives for every deployment.

Refer to the [Stable runbook](../ops/stable-runbook.md) for operational steps to flip the engine, replay logs, and export the Newman artifacts consumed by compliance. When new assets are listed, update both the configuration and this document so traceability remains intact.
