# Swap Gateway Technical Documentation

## 1. Overview

The Swap Gateway bridges fiat payments (e.g., USD) to on-chain ZNHB mints. It exposes a REST API that quotes conversion rates, creates payment orders, verifies payment webhooks, and submits signed mint vouchers to the NHB chain via JSON-RPC. This document covers architecture, message formats, operational flows, and guidance tailored to frontend engineers, auditors, regulators, investors, and consumers.

## 2. Components

| Component | Responsibility |
| --- | --- |
| Swap Gateway service | Hosts REST API, calculates quotes, manages order state, signs vouchers, and calls `swap_submitVoucher` on the node. |
| Price source | Determines fiat → ZNHB rate. Development uses a fixed price (`fixed:<rate>`); production may integrate CoinGecko or oracles. |
| Order store | In-memory map (pluggable for SQLite/Postgres) keyed by `reference`/`orderId` for idempotency. Tracks status transitions. |
| Node RPC | Receives vouchers and signatures. Executes minting via on-chain module introduced in SWAP-2. |
| NowPayments (or equivalent fiat processor) | Sends payment webhook with HMAC-authenticated payload once the fiat invoice is paid. |

## 3. Environment Configuration

| Variable | Description |
| --- | --- |
| `SWAP_PORT` | HTTP listen port (default `8090`). |
| `SWAP_NODE_RPC_URL` | JSON-RPC endpoint for submitting vouchers. |
| `SWAP_CHAIN_ID` | NHB chain ID for voucher hashing. |
| `SWAP_PAYMENT_HMAC_SECRET` | Shared secret for verifying webhook HMAC. Empty disables verification (dev only). |
| `SWAP_PRICE_SOURCE` | Price backend spec (e.g., `fixed:0.10`). |
| `MINTER_ZNHB_ADDRESS` | Bech32 NHB address of the signer. Must match recovered signature. |
| `MINTER_ZNHB_PRIVKEY` | Hex-encoded secp256k1 private key used to sign vouchers (KMS/HSM recommended in prod). |

## 4. REST API Endpoints

### 4.1 `POST /swap/quote`

**Request**
```json
{
  "fiat": "USD",
  "amountFiat": "100.00"
}
```

**Response**
```json
{
  "fiat": "USD",
  "amountFiat": "100.00",
  "rate": "0.10",
  "znHB": "1000000000000000000000"
}
```

The gateway parses decimal inputs, enforces USD for the fixed source, and converts the fiat amount to wei (`amountFiat / rate * 1e18`). Non-integral conversions trigger an error.

### 4.2 `POST /swap/order`

Creates or retrieves an order keyed by `reference` for idempotency.

**Request**
```json
{
  "fiat": "USD",
  "amountFiat": "100.00",
  "recipient": "nhb1...",
  "reference": "SWP_123"
}
```

**Response**
```json
{
  "orderId": "SWP_123",
  "payUrl": "https://pay.dev/checkout/SWP_123",
  "expected": "100.00",
  "recipient": "nhb1...",
  "fiat": "USD",
  "amountWei": "1000000000000000000000",
  "rate": "0.10"
}
```

Orders start with status `PENDING`. In production, `payUrl` should point to the fiat processor checkout session.

### 4.3 `POST /webhooks/payment`

Processes payment notifications after HMAC verification.

**Headers**
```
X-HMAC: <hex(hmac_sha256(body, SWAP_PAYMENT_HMAC_SECRET))>
```

**Body**
```json
{
  "orderId": "SWP_123",
  "fiat": "USD",
  "amountFiat": "100.00",
  "paid": true,
  "txRef": "NOWP-001"
}
```

Workflow:
1. HMAC validation (required for non-empty secret).
2. Order lookup and state validation (`PENDING` or `PAID`).
3. Voucher assembly (random nonce, `expiry = now + 15 min`).
4. secp256k1 signature using minter key. Recovered signer must equal `MINTER_ZNHB_ADDRESS`.
5. JSON-RPC request `swap_submitVoucher` with payload `{"voucher": <VoucherV1>, "sig": "0x..."}`.
6. Order status updates to `MINT_SUBMITTED` with minted wei recorded.

**Response**
```json
{
  "ok": true,
  "submitted": true,
  "minted": "1000000000000000000000"
}
```

### 4.4 `GET /orders/{orderId}`

Returns order snapshot (status, minted amount, pay URL, etc.). Possible statuses: `PENDING`, `PAID`, `MINT_SUBMITTED`, `MINTED` (reserved for future confirmation events).

## 5. Voucher Specification

```json
{
  "domain": "NHB_SWAP_VOUCHER_V1",
  "chainId": 187001,
  "token": "ZNHB",
  "recipient": "nhb1...",
  "amount": "1000000000000000000000",
  "fiat": "USD",
  "fiatAmount": "100.00",
  "rate": "0.10",
  "orderId": "SWP_123",
  "nonce": "d6b2f3c4...",
  "expiry": 1735689600
}
```

Hashing uses the deterministic string template:
```
NHB_SWAP_VOUCHER_V1|chain=<chainId>|token=<token>|to=<recipient_hex>|amount=<amount>|fiat=<fiat>|fiatAmt=<fiatAmount>|rate=<rate>|order=<orderId>|nonce=<nonce>|exp=<expiry>
```

`recipient_hex` is the 20-byte NHB address decoded from bech32 and lowercased hex. The SHA3 Keccak-256 digest feeds secp256k1 signing. Gateways must reject signatures that do not recover to `MINTER_ZNHB_ADDRESS`.

## 6. JSON-RPC Submission

`swap_submitVoucher` request:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "swap_submitVoucher",
  "params": [
    {
      "voucher": { ... },
      "sig": "0x<65-byte-signature>"
    }
  ]
}
```

Responses follow JSON-RPC 2.0 semantics. Any non-200 status or RPC error must be surfaced to webhook callers for retry logic. Future iterations may include idempotency tokens or asynchronous mint confirmations.

## 7. Order Lifecycle

1. **Quote** — Frontend fetches conversion to display expected ZNHB output.
2. **Order creation** — Frontend submits recipient address and reference. Gateway stores pending order and returns `payUrl`.
3. **Fiat payment** — Customer completes checkout on NowPayments (or chosen PSP).
4. **Webhook** — PSP POSTs to `/webhooks/payment`. Gateway verifies HMAC, signs voucher, submits to node, updates status to `MINT_SUBMITTED`.
5. **Mint finalization** — Node mints ZNHB, records the `orderId` replay nonce, and emits a `swap.minted` event with `{orderId, recipient, amount, fiat, fiatAmount, rate}`. Downstream services can poll or subscribe to confirm and mark the order `MINTED`.

## 8. Frontend Implementation Guide

### 8.1 UX Flow

1. **Collect recipient** — Accept NHB bech32 address or username (optionally resolve via identity module).
2. **Amount entry** — Let users input fiat amount in USD. Fetch `/swap/quote` to display minted ZNHB.
3. **Order create** — POST `/swap/order` with a unique `reference` (UUID). Store `orderId` and `payUrl` from response.
4. **Redirect to PSP** — Launch NowPayments checkout using `payUrl`. Provide instructions for wallet credit timeline.
5. **Status polling** — Poll `/orders/{orderId}` to surface transitions (`PENDING` → `MINT_SUBMITTED`). Once minted, display minted wei (convert to ZNHB with 18 decimals).
6. **Error handling** — On quote/order failures, show user-friendly message (e.g., unsupported fiat, invalid address). For webhook errors, provide support fallback as backend will log failure.

### 8.2 Security Considerations for Frontend

- Validate bech32 addresses client-side before calling the gateway.
- Generate cryptographically random `reference` to avoid collisions.
- Use HTTPS and include CSRF protections if embedding order creation in web apps.
- Do not expose minter private key or node credentials to the browser; all signing occurs server-side.

## 9. Operational & Security Notes

- **HMAC secrets** should be rotated regularly. Failed verifications respond with HTTP 401.
- **Key custody** — Production deployments should load `MINTER_ZNHB_PRIVKEY` from HSM/KMS. The reference implementation uses env vars for dev.
- **Rate validation** — For live price feeds, enforce rate freshness and maximum slippage to protect consumers.
- **Observability** — Enable structured logging for order events, webhook processing, and RPC interactions. Consider Prometheus metrics for monitoring latency and errors.
- **Idempotency** — Duplicate webhooks must be safe. The store transition guard rejects already-processed orders.

## 10. Compliance & Transparency (Auditors, Regulators, Investors, Consumers)

- **Traceability** — Each voucher includes fiat amount, rate, and PSP reference (`orderId`, `txRef`). Logs should correlate voucher submissions with fiat receipts.
- **Audit trails** — Persist order state transitions and voucher payloads in durable storage (extend `orderStore` to SQLite/Postgres). Attach PSP transaction IDs and blockchain tx hashes once available.
- **Consumer disclosures** — Communicate conversion rate, fees, mint timing, and refund policies at checkout and in post-payment confirmations.
- **Regulatory controls** — Integrate KYC/AML screening upstream of order creation if required. Enforce per-user limits and sanction screening before submitting vouchers.
- **Risk management** — Implement PSP webhook retry validation (e.g., replay protection with nonce expiry) and monitor for mismatched amounts or fiat currencies.

## 11. Extensibility

- **Alternative price sources** — Implement additional `priceSource` strategies (CoinGecko, oracle) in `quote.go` while preserving interface contract.
- **Persistent storage** — Swap in SQLite/Postgres by implementing an `orderStore` backed by database transactions.
- **Asynchronous mint confirmation** — Subscribe to node events to transition `MINT_SUBMITTED` → `MINTED` and notify clients.
- **Multiple tokens** — Parameterize `token` and `rate` logic to support other NHB assets.

## 12. Testing & Local Development

1. `go test ./services/swap-gateway/...` — runs unit tests (quote math, HMAC verification, voucher hashing & signing, webhook dry-run).
2. `go run ./services/swap-gateway` — launches local server on `SWAP_PORT`.
3. `curl` examples:
   - `curl -X POST localhost:8090/swap/quote -d '{"fiat":"USD","amountFiat":"100.00"}' -H 'Content-Type: application/json'`
   - `curl -X POST localhost:8090/swap/order ...`
   - Simulate webhook by computing HMAC with `SWAP_PAYMENT_HMAC_SECRET` and POSTing to `/webhooks/payment`.

## 13. Glossary

- **ZNHB** — Stable asset minted on NHB chain.
- **Voucher** — Off-chain signed instruction authorizing mint.
- **PSP** — Payment Service Provider (e.g., NowPayments).
- **Wei** — Smallest unit (1e-18 ZNHB).

## 14. Audience-Specific Guidance

### 14.1 Frontend Developers

**User journey**

1. Collect recipient NHB address (or username → address lookup) and fiat amount. Validate format locally (`>= $1.00`, two decimals).
2. Call `/swap/quote` and display the converted ZNHB amount plus a 60-second countdown. Cache the `rate` so subsequent requests are consistent.
3. POST `/swap/order` with a UUID `reference`. Persist `orderId`/`payUrl` in local storage so the session survives refresh.
4. Redirect to the payment processor using `payUrl`. Provide inline instructions covering settlement expectations.
5. Poll `/orders/{orderId}` every 5–10 seconds. Update UI states (`PENDING`, `PAID`, `MINT_SUBMITTED`) and surface the minted wei once available.
6. Present a receipt summarizing fiat, rate, minted wei, payment reference, and an explorer link.

**State machine**

| State | Trigger | UI Treatment |
| --- | --- | --- |
| `idle` | Landing page load | Inputs active, CTA disabled until validation succeeds. |
| `quoting` | `/swap/quote` request | Disable inputs, show spinner. |
| `quoteReady` | Quote response | Show rate + countdown, enable continue CTA. |
| `ordering` | `/swap/order` request | Disable CTA, show progress indicator. |
| `awaitingPayment` | Order created | Emphasize `payUrl`, display reference code. |
| `pendingMint` | Webhook indicates paid | Show minted amount placeholder, highlight processing state. |
| `complete` | RPC submission success | Render success banner, display explorer link & receipt download. |
| `error` | Any failure | Provide actionable error messaging and retry options. |

**Validation & security**

- Validate NHB bech32 addresses client-side; surface precise errors when checksum fails.
- Normalize fiat strings to `.` decimal separator before sending.
- Enforce per-order limits aligned with compliance policies (e.g., `$1–$2,500`).
- Implement exponential backoff on failed polling or RPC requests to avoid flooding the gateway.
- Use HTTPS, same-site cookies, and CSRF tokens for authenticated treasury consoles. Never expose minter keys or RPC credentials to the browser.

### 14.2 Auditors

- Confirm the minter key registered on-chain (`SetTokenMintAuthority`) matches the signing key configured in the gateway environment.
- Review persisted order records and verify each `orderId` maps 1:1 with a `swap.minted` event and PSP receipt.
- Inspect state proofs (e.g., trie snapshots) to ensure `swap/order/<orderId>` entries are created for every mint, preventing double spends.
- Reconcile nightly mint exports against fiat settlement reports; investigate any orphan vouchers or missing webhooks.
- Ensure voucher logs include nonce, signature, timestamp, and PSP reference for downstream audit sampling.

### 14.3 Regulators

- Ensure KYC/AML checks occur before order creation when mandated; the gateway can be extended to block requests that lack verified identities.
- Retain voucher JSON, webhook payloads, and emitted event logs for the jurisdiction’s required retention period.
- Leverage the deterministic voucher hash and `txHash` returned by the node to trace fiat inflows to on-chain issuance for reporting.
- Document refund and dispute processes, including timelines and communication channels with the PSP.
- Maintain sanctions screening logs and per-customer exposure limits tied to `orderId` for supervisory review.

### 14.4 Investors

- Monitor aggregate swap volume by indexing `swap.minted` events (amount, fiat, rate) to understand revenue and token distribution trends.
- Track failure rates (invalid vouchers, unauthorized signers) as operational risk indicators for the business.
- Observe liquidity coverage by comparing fiat receivables vs. minted supply and publish quarterly attestation reports from independent auditors.
- Measure customer acquisition cost by correlating marketing channels with order creation volume and completion rate.

### 14.5 Consumers

- The checkout displays the exact conversion rate and expected on-chain amount; funds typically arrive once the fiat payment clears (minutes for cards, longer for bank transfers).
- Customers can verify receipt by querying the wallet address through existing NHB explorers or RPC `nhb_getBalance`.
- Support channels should educate users on safe address handling and the irreversibility of on-chain mints once `swap_submitVoucher` succeeds.
- Provide FAQs covering settlement timing, refund scenarios, and wallet safety tips.
- Encourage users to enable multi-factor authentication on any custodial wallet accounts tied to swaps.

## 15. Change Log

- **SWAP-1** — Introduced Swap Gateway with quoting, order creation, webhook signing, voucher submission, and documentation.
- **SWAP-2** — Added on-chain voucher verification, replay protection, `swap.minted` events, and expanded audience-specific guidance.

## 16. Incident Response & Monitoring

- **Alerting** — Page operators on webhook signature failures, RPC submission errors, or mint latencies exceeding 60 seconds.
- **Circuit breakers** — Provide operational toggles to pause new orders while honoring in-flight redemptions; surface status to clients in real time.
- **Runbooks** — Maintain step-by-step recovery guides for PSP outages, node downtime, and signing key rotation.
- **Post-incident reviews** — Document timelines, impacted order IDs, root cause, and mitigation steps. Share summaries with regulators, investors, and affected partners.
