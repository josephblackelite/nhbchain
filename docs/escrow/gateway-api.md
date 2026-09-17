# Escrow Gateway REST API

The escrow gateway (`services/escrow-gateway`) exposes a small REST surface over the on-chain escrow and P2P trade primitives. It
creates escrows/trades via node RPC, tracks status locally, and delivers webhook notifications as on-chain events arrive. This
document describes the endpoints, authentication, idempotency, and webhook mechanisms actually implemented by the service.

---

## 1. Host & Deployment

The gateway is self-hosted; there is no fixed public base URL. It listens on `GATEWAY_PORT` (see
[`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md) for configuration) and is not versioned with a path prefix — routes are
served directly at the paths listed below (e.g. `POST /escrow/create`, not `POST /v1/escrow/create`).

---

## 2. Authentication & Signing

Two layers of authentication protect gateway requests:

1. **API key + HMAC.** Every write request must include:
   * `X-Api-Key` — the caller's API key identifier.
   * `X-Timestamp` — Unix seconds. Requests outside the configured skew window (default up to 2 minutes) are rejected.
   * `X-Nonce` — a per-request nonce; reuse of the same `(timestamp, nonce)` pair for an API key is rejected as a replay.
   * `X-Signature` — hex-encoded HMAC-SHA256, computed as:

     ```text
     signature = hex(HMAC-SHA256(api_secret, timestamp + "\n" + nonce + "\n" + METHOD + "\n" + path + "\n" + body))
     ```

     `METHOD` is upper-cased and `path` is the canonical request path (query parameters sorted). This is implemented in
     `gateway/auth/auth.go` (`ComputeSignature`).

2. **Wallet signature.** Privileged escrow actions (`/escrow/release`, `/escrow/refund`, `/escrow/dispute`, `/escrow/resolve`,
   `/p2p/offers`, `/p2p/accept`) additionally require a wallet signature proving control of the payer/payee/mediator/seller/buyer
   address:
   * `X-Sig-Addr` — the signer's bech32 address (`nhb1…`/`znhb…`).
   * `X-Sig` — hex-encoded, 65-byte EIP-191 signature (`hex(eip191_sign(keccak256(payload)))`).
   * `X-Timestamp` / `X-Nonce` — reused from the HMAC layer.

   The signed payload is the pipe-joined string:

   ```text
   METHOD|path|body|timestamp|nonce|resourceId
   ```

   where `resourceId` is the escrow ID, trade/offer ID, or an empty string when the action has no single resource (e.g. offer
   creation). This is implemented in `services/escrow-gateway/server.go` (`verifyWalletSignature`).

---

## 3. Idempotency

Every mutating endpoint (`POST /escrow/create`, `/escrow/release`, `/escrow/refund`, `/escrow/dispute`, `/escrow/resolve`,
`/p2p/offers`, `/p2p/accept`) requires an `Idempotency-Key` header. The gateway hashes `(method, path, body)` and stores the
response keyed by `(api_key, idempotency_key)`:

* A retry with the same key and identical request returns the original cached response and status code.
* A retry with the same key but a different request body returns `409 Conflict`.

---

## 4. Endpoints

### 4.1 Escrow

| Method & Path | Description | Auth |
|----------------|-------------|------|
| `POST /escrow/create` | Create an escrow and return a pay intent. | API key + HMAC. |
| `GET /escrow/{id}` | Fetch escrow status/detail. | API key + HMAC. |
| `POST /escrow/release` | Release funds to payee. | API key + HMAC + wallet signature (payee or mediator). |
| `POST /escrow/refund` | Refund payer. | API key + HMAC + wallet signature (payer). |
| `POST /escrow/dispute` | Flag a dispute. | API key + HMAC + wallet signature (payer or payee). |
| `POST /escrow/resolve` | Resolve a disputed escrow via a realm arbitration-committee decision. | API key + HMAC + a quorum of the escrow's frozen realm-committee signatures over a signed decision envelope (not a single payer/payee/mediator wallet signature) — see [`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md)'s Authentication section for the exact envelope shape and signing contract. |

`release`/`refund`/`resolve` responses are `202 Accepted` with `{"queued": true}` (the node call is made synchronously, but the
effect is reported as queued); `dispute` responds `202 Accepted` with `{"ok": true}`. `GET /escrow/{id}` returns the escrow struct
as-is from the node — there is no separate `/escrow/{id}/events` endpoint. The path is matched by prefix (`/escrow/` +
remainder-as-ID), so no additional path segments are supported after the ID.

### 4.2 P2P offers & trades

| Method & Path | Description | Auth |
|----------------|-------------|------|
| `POST /p2p/offers` | Seller creates an offer. | API key + HMAC + wallet signature (seller). |
| `GET /p2p/offers` | List all offers (no pagination or filtering). | API key + HMAC. |
| `POST /p2p/accept` | Buyer accepts an offer; gateway creates the dual-lock trade/escrows and returns pay intents. | API key + HMAC + wallet signature (buyer). |
| `GET /p2p/trades/{id}` | Fetch trade status/detail. | API key + HMAC. |

There is no endpoint to settle, dispute, or resolve a trade over REST — those actions go through the node's `p2p_settle`,
`p2p_dispute`, and `p2p_resolve` RPC methods directly (see [`escrow.md`](./escrow.md) §5.2).

---

## 5. Webhooks

There is no self-service subscription endpoint (no `POST /webhooks`). Webhook targets are rows in the gateway's local `webhooks`
table (`api_key`, `event_type`, `url`, `secret`, `rate_limit`, `active`), registered by whoever operates the gateway rather than
through a public API.

Delivery, implemented in `services/escrow-gateway/webhook.go`:

* Header: `X-Webhook-Signature` — hex-encoded HMAC-SHA256 of the raw JSON body, signed with the subscription's stored secret.
* Payload:

  ```json
  {
    "type": "escrow.trade.settled",
    "sequence": 1234,
    "escrowId": "0x...",
    "tradeId": "0x...",
    "attributes": { "...": "..." },
    "timestamp": "2024-03-02T18:45:11.000000000Z",
    "provider": { "scope": "platform", "type": "...", "profile": "...", "feeBps": 100, "feeRecipient": "nhb1..." }
  }
  ```

  `provider` is only present when the underlying event carries realm/provider attributes. Event `type` values mirror the on-chain
  event namespace (`escrow.*` and `escrow.trade.*` — see [`escrow.md`](./escrow.md) §4), plus a gateway-originated `escrow.created`
  fired immediately when `POST /escrow/create` succeeds.
* Delivery is at-least-once with exponential backoff (1s, 2s, 4s, ... capped at 5 minutes), up to 5 attempts per event, subject to a
  per-subscription rate limit. There is no `retry_policy` field on the subscription payload and no dead-letter/email escalation.

---

## 6. Error Handling

Errors are plain JSON: `{"error": "<message>"}`. There is no `trace_id`, structured `code` field, or audit-log endpoint. Status
codes used by the handlers:

* `400` — validation errors (missing/invalid parameters, missing `Idempotency-Key`).
* `401` — authentication failure (missing/invalid API key, HMAC signature, timestamp skew, or nonce replay).
* `403` — wallet signature missing, invalid, or from a signer not authorized for the action.
* `404` — escrow, offer, or trade not found.
* `409` — idempotency key reused with a different request body.
* `502` — the underlying node RPC call failed.
* `500` — internal gateway error (e.g. storage failure).
