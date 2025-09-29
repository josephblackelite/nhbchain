# Stable Funding API

The stable swap engine powers voucher liquidity, reserves cash-out intents, and records receipts for downstream treasury teams. Although the engine currently runs in "preview" mode (all endpoints return `501 Not Implemented` until operations unpause the feature flag), auditors need end-to-end visibility into the contracts that will gate production traffic.

This document captures the HTTP surface for `/v1/stable/*`, expected request/response shapes, rate limits, and the transparency hooks that bind quotes to reservations, cash-out intents, and banking receipts.

## Endpoints at a Glance

| Method | Path                  | Description                                            | Preview behaviour | Rate limits* |
| ------ | --------------------- | ------------------------------------------------------ | ----------------- | ------------ |
| POST   | `/v1/stable/quote`    | Calculate an indicative redemption price for vouchers | Returns 501 with `stable engine not enabled` error | 30 requests/min per account |
| POST   | `/v1/stable/reserve`  | Reserve a previously issued quote for settlement      | Returns 501 with `stable engine not enabled` error | 15 reservations/min per account |
| POST   | `/v1/stable/cashout`  | Create a cash-out intent and receipt stub              | Returns 501 with `stable engine not enabled` error | 10 intents/min per account |
| GET    | `/v1/stable/status`   | Provide in-memory counters for quotes and reservations | Returns 501 with `stable engine not enabled` error | 60 requests/min |
| GET    | `/v1/stable/limits`   | Advertise soft mint/redeem policies                    | Returns 501 with `stable engine not enabled` error | 60 requests/min |

\*Soft limits are enforced through swapd's throttle registry; see the [operations runbook](../ops/stable-runbook.md) for override procedures.

Even when the engine is paused the collection described below asserts that responses stay consistent—any drift in status codes or error contracts will fail regression.

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
  "quote_id": "q-1717706117123456000",
  "asset": "ZNHB",
  "price": 100.12,
  "expires_at": "2024-06-07T19:15:17Z",
  "trace_id": "1bde14ab-9f23-497d-a60c-018f4231a630"
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
  "quote_id": "q-1717706117123456000",
  "amount_in": 100.0,
  "account": "merchant-123"
}
```

Successful response:

```json
{
  "reservation_id": "q-1717706117123456000",
  "quote_id": "q-1717706117123456000",
  "amount_in": 100.0,
  "amount_out": 100.0,
  "expires_at": "2024-06-07T19:15:17Z",
  "trace_id": "a7d085b0-bdf9-4cc6-aa17-02dc28b09b9b"
}
```

Preview response mirrors the `quote` endpoint, returning a `501` error with `stable engine not enabled`.

Telemetry: reservations log `stable.reserve_quote` spans with `reservation.id` attributes and attach to the quote span via `traceparent`. Failed reservations (expired, already consumed) set `status=Error` so that the audit dashboard surfaces anomalies.

## POST `/v1/stable/cashout`

Request body:

```json
{
  "reservation_id": "q-1717706117123456000"
}
```

Successful response:

```json
{
  "intent_id": "i-1717706120456000",
  "reservation_id": "q-1717706117123456000",
  "amount": 100.0,
  "trace_id": "497e13ba-4b31-44ad-87d9-3ca0d0aa2a94",
  "receipt_hint": "ACH:T+1"
}
```

Preview response returns the same `501` payload as other endpoints.

The cash-out intent closes the audit trail by linking reservation IDs to treasury receipts. Intent creation logs `stable.create_cashout_intent` spans, persists the intent in the swapd database, and emits structured logs (`cashout intent created`) that the audit pipeline ingests.

## GET `/v1/stable/status`

Response (engine enabled):

```json
{
  "quotes": 1,
  "reservations": 1,
  "assets": 1,
  "updated_at": "2024-06-07T19:15:17Z"
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
| 429    | `throttled`                   | Breach of per-account policy        | Contact operations or wait for rolling window |
| 501    | `stable engine not enabled`   | Preview mode guardrail              | Flip `stable.paused=false` and run regression suite |

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
