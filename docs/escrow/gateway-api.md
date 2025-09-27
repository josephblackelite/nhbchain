# Escrow Gateway REST API

The escrow gateway exposes a RESTful interface for merchants, wallets, and marketplaces to orchestrate trade lifecycles, monitor
events, and export settlement data. Task 5 introduces cursor pagination, rich filtering, SLA metrics, and strong idempotency
controls.

---

## 1. Base URL & Versions

* **Base URL (production):** `https://api.nhbcoin.net/escrow/v1`
* **Base URL (sandbox):** `https://sandbox.api.nhbcoin.net/escrow/v1`
* **Versioning:** The first path segment (`/v1`) tracks major API revisions. Breaking changes increment the segment. Minor additive
  changes use standard semantic version headers (`X-NHB-API-Version`).

---

## 2. Authentication & Signing

Three layers of authentication protect gateway requests:

1. **API key.** Every client receives an API key (`X-API-Key` header). Keys map to merchant accounts and enforce rate limits.
2. **HMAC signature.** Each request includes an HMAC signature in `X-Signature` computed as:

   ```text
   signature = Base64(HMAC-SHA256(api_secret, method + "\n" + path + "\n" + body + "\n" + timestamp))
   ```

   * `timestamp` is the value of the `X-Timestamp` header (RFC 3339). Requests older than 60 seconds are rejected.
3. **Wallet signature.** Mutating trade actions (settle, dispute, resolve) require a wallet signature that matches the on-chain
   caller. Include the Bech32 address in `X-Wallet-Address` and sign the canonical payload (`nhbgw:{method}:{path}:{body_hash}`).

> **Idempotency keys:** Send a UUID in the `Idempotency-Key` header for every POST/PUT/PATCH request. Replays return the original
> response with HTTP `409 Conflict`.

---

## 3. Pagination, Filtering & Sorting

All list endpoints return a cursor object:

```json
{
  "data": [...],
  "paging": {
    "next_cursor": "opaque-string-or-null",
    "prev_cursor": "opaque-string-or-null",
    "limit": 50
  }
}
```

* Pass `cursor` and optional `limit` to traverse pages (max `limit` = 200).
* Filtering is performed via query parameters. Examples:
  * `GET /trades?status=FUNDED&buyer=nhb1xyz...&created_after=2024-01-01T00:00:00Z`
  * `GET /escrows?token=ZNHB&disputed=true`
* Sorting defaults to `created_at desc`. Override with `sort=created_at.asc` or `sort=settled_at.desc`.

All filters are index-backed to meet SLA latency (<250 ms p95 for 100-row pages).

---

## 4. Rate Limits & SLA Metrics

* **Default rate limit:** 600 requests per minute per API key. Burst tokens reset every 10 seconds.
* **Idempotent retries:** Gateway tolerates client retries with the same `Idempotency-Key` without counting toward rate limits.
* **SLA metrics endpoint:** `GET /metrics/sla` returns current p50/p95 latency and error ratios per endpoint over the last hour.
  Use this to monitor compliance with contractual SLAs.
* **Alerts:** Merchants can configure webhook alert targets for SLA breaches (`POST /alerts/subscriptions`). Alerts deliver JSON
  payloads describing the breached metric.

---

## 5. Endpoints

### 5.1 Trades

| Method & Path | Description | Notes |
|---------------|-------------|-------|
| `GET /trades` | List trades with pagination & filters. | Supports `status`, `buyer`, `seller`, `offer_id`, `disputed`, `created_before/after`, `settled_before/after`. |
| `GET /trades/{trade_id}` | Retrieve trade detail. | Includes escrow snapshots, dispute timeline, latest SLA metrics. |
| `POST /trades` | Create trade (gateway-managed offer acceptance). | Optional. Most merchants use on-chain RPC directly. Requires wallet signature. |
| `POST /trades/{trade_id}/settle` | Trigger atomic settlement once both legs funded. | Requires buyer, seller, or gateway service wallet signature. Enforces idempotency. |
| `POST /trades/{trade_id}/dispute` | Mark trade as disputed. | Caller must be buyer or seller of record. Requires reason code. |
| `POST /trades/{trade_id}/resolve` | Arbitrator resolution (`outcome: release|refund`). | Requires arbitrator wallet signature. Accepts `resolution_memo`, optional `evidence_uri`. |
| `POST /trades/{trade_id}/cancel` | Cancel trade before settlement (dual refund). | Only available while status is `TRADE_INIT` or `TRADE_PARTIAL_FUNDED`. |
| `POST /trades/{trade_id}/expire` | Force deadline expiry after cutoff. | Public endpoint guarded by rate-limited API key. |

### 5.2 Escrows

| Method & Path | Description | Notes |
|---------------|-------------|-------|
| `GET /escrows` | List escrows by status, token, dispute flag. | Supports filters `status`, `token`, `payer`, `payee`, `deadline_before/after`, `disputed=true`. |
| `GET /escrows/{escrow_id}` | Fetch escrow detail. | Includes transaction hashes and event references. |
| `POST /escrows/{escrow_id}/fund` | Mark escrow as funded after deposit. | Requires payer wallet signature. Idempotent. |
| `POST /escrows/{escrow_id}/release` | Release funds to payee. | Allowed for payee, mediator, or arbitrator. |
| `POST /escrows/{escrow_id}/refund` | Refund payer. | Payer-only unless dispute resolved to refund outcome. |
| `POST /escrows/{escrow_id}/dispute` | Flag dispute on single-leg escrow. | Mirrors trade dispute behavior. |
| `POST /escrows/{escrow_id}/resolve` | Resolve single-leg dispute. | Arbitrator only. |

### 5.3 Settlement Exports

| Method & Path | Description |
|---------------|-------------|
| `GET /exports/settlements` | Returns CSV or JSON export of settled trades over a period. Filters: `settled_from`, `settled_to`, `merchant_id`, `token`. Responses include escrow IDs, fee amounts, on-chain tx hashes. |
| `POST /exports/settlements` | Asynchronous export. Submit payload specifying filters and delivery target (`s3`, `gcs`, or `webhook`). Poll using job ID via `GET /exports/settlements/{job_id}`. |

### 5.4 Alerts & Metrics

| Method & Path | Description |
|---------------|-------------|
| `GET /metrics/sla` | Current latency/error metrics per endpoint. |
| `GET /metrics/rate-limits` | Returns remaining quota for the current window. |
| `POST /alerts/subscriptions` | Register webhook targets for SLA breach notifications. |
| `DELETE /alerts/subscriptions/{id}` | Remove alert subscription. |

---

## 6. Webhooks

### 6.1 Subscription

Create webhook destinations via `POST /webhooks` with payload:

```json
{
  "url": "https://merchant.example/webhooks/nhb",
  "event_types": ["trade.settled", "trade.disputed", "escrow.resolved"],
  "secret": "<shared-secret>",
  "retry_policy": { "max_attempts": 8, "backoff_seconds": 15 }
}
```

Webhooks include:

* `X-NHB-Signature`: HMAC-SHA256 over the body using the shared secret.
* `X-NHB-Event-ID`: Stable UUID for deduplication.
* `X-NHB-Event-Time`: RFC 3339 timestamp of the originating chain event.

### 6.2 Event payloads

All webhook payloads share the envelope:

```json
{
  "event_id": "uuid",
  "event_type": "trade.settled",
  "event_time": "2024-03-02T18:45:11Z",
  "retry": 0,
  "data": { /* event-specific payload */ }
}
```

Event-specific payloads align with the on-chain event definitions (see `/docs/escrow/escrow.md`). Example for a trade settlement:

```json
{
  "event_id": "68c9e...",
  "event_type": "trade.settled",
  "event_time": "2024-03-02T18:45:11Z",
  "retry": 0,
  "data": {
    "trade_id": "0xabc...",
    "escrow_base_id": "0x123...",
    "escrow_quote_id": "0x456...",
    "buyer": "nhb1...",
    "seller": "nhb1...",
    "net_base_amount": "100.000000",
    "net_quote_amount": "50.000000",
    "fee_amounts": {
      "base": "0.500000",
      "quote": "0.250000"
    },
    "block_height": 1435021,
    "tx_hash": "0xdeadbeef..."
  }
}
```

### 6.3 Delivery semantics

* At-least-once delivery with exponential backoff.
* Retry attempts capped at `max_attempts`; merchants should store `event_id` to deduplicate.
* Webhook failure >24 hours triggers an email alert to the merchant's operations contact.

---

## 7. Error Handling

* HTTP 400 – validation errors (missing parameters, invalid filters).
* HTTP 401 – failed authentication (invalid API key or HMAC).
* HTTP 403 – caller lacks required wallet role (e.g., arbitrator required).
* HTTP 404 – resource not found (trade/escrow ID unknown to gateway).
* HTTP 409 – idempotency conflict (duplicate payload, original response replayed).
* HTTP 422 – business rule violation (trade not funded, cannot settle).
* HTTP 429 – rate limit exceeded.
* HTTP 500 – internal gateway error (automatically logged with trace ID `X-Trace-Id`).

Error responses include:

```json
{
  "error": {
    "code": "TRADE_NOT_FUNDED",
    "message": "Both legs must be funded before settlement.",
    "trace_id": "01HZY..."
  }
}
```

---

## 8. SLA & Observability Integration

* **Tracing:** Each request returns `X-Trace-Id`. Include this in support tickets for replay.
* **Metrics streaming:** Merchants can subscribe to streaming metrics via `GET /metrics/stream` (Server-Sent Events). Events emit
  near-real-time p95 latency, error counts, and success volume.
* **Audit logging:** `GET /audit/logs` exposes paginated audit trails including who invoked settlement/dispute operations, wallet
  address, and idempotency key. Logs can be filtered by merchant user.

---

## 9. Sandbox Differences

* Sandbox enforces lower rate limits (60 rpm) and uses a simulated arbitrator service that auto-resolves disputes after 10 minutes
  with alternating outcomes.
* Token balances are faucet-funded (`POST /sandbox/faucet`).
* Webhooks deliver to the same URL but with header `X-NHB-Sandbox: true`.

Use the sandbox in conjunction with the merchant tooling simulator documented in `/docs/commerce/merchant-tools.md`.
