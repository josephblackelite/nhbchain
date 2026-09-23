# Escrow Gateway REST API

The escrow gateway (`services/escrow-gateway`) is an HTTP service in front of the node's escrow features. It verifies signatures, relays escrow actions to the node as signed transactions, and keeps a local SQLite database (idempotency records, audit log, P2P offers). This document lists the endpoints exactly as implemented in `services/escrow-gateway/server.go`. Configuration and the signing envelopes are in [`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md).

The gateway has no path prefix and no fixed public base URL: routes are served at `/escrow/...` and `/p2p/...` on the address set by `ESCROW_GATEWAY_LISTEN` (default `:8081`). Any other path returns 404.

---

## 1. Authentication

### 1.1 API key + HMAC (every endpoint)

Every endpoint, `POST` and `GET`, calls `Authenticator.Authenticate` (`gateway/auth/auth.go`; the `GET` handlers do it through `authenticateRead`, `services/escrow-gateway/server.go`) and requires:

* `X-Api-Key`: the API key identifier.
* `X-Timestamp`: Unix seconds. The request must be within the allowed skew of the gateway clock (default 2 minutes; the skew is capped at 2 minutes). Within that window the timestamp must also be strictly greater than the last timestamp accepted for the same key, otherwise the request fails with `timestamp not increasing`.
* `X-Nonce`: per-request nonce. Reusing a `(timestamp, nonce)` pair for the same key fails with `nonce already used`.
* `X-Signature`: hex-encoded HMAC-SHA256 with the key's secret:

  ```text
  signature = hex(HMAC-SHA256(secret, timestamp + "\n" + nonce + "\n" + METHOD + "\n" + path + "\n" + body))
  ```

  `METHOD` is upper-cased. `path` is the request path; if there is a query string it is appended as `?` plus the query parameters sorted as strings (`CanonicalRequestPath`). Request bodies are limited to 1 MiB. For a `GET` the body is empty, so the signed string ends with an empty body. The timestamp and nonce rules apply to reads exactly as to writes: a read consumes a nonce and advances the key's last-accepted timestamp.

`GET /escrow/{id}`, `GET /p2p/offers` and `GET /p2p/trades/{id}` call `authenticateRead` first (`handleEscrowGet`, `handleListOffers`, `handleGetTrade`); without a valid API key and signature they answer `401` and write an audit-log row. They need no wallet signature and no `Idempotency-Key`.

### 1.2 Wallet signature (participant proof)

Escrow create, release, refund and dispute require a participant signature in addition to API key + HMAC:

* `X-Sig-Addr`: the signer's bech32 address.
* `X-Sig`: 65-byte hex signature (optional `0x`) over `keccak256` of a canonical JSON envelope. It is not EIP-191 wrapped and does not cover the HTTP request. The envelopes are listed in [`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md#signing-envelopes).

`POST /escrow/resolve` takes no wallet headers; it carries the arbitrators' signatures in the body.

The P2P offer endpoints use a different scheme, verified by `verifyWalletSignature`: `X-Sig-Addr`, `X-Sig`, `X-Timestamp`, `X-Nonce`, where the signature is an EIP-191 (`personal_sign`-style) signature over `keccak256` of

```text
METHOD|path|body|timestamp|nonce|resourceId
```

with `resourceId` empty for `POST /p2p/offers` and the lower-cased offer ID for `POST /p2p/accept`.

---

## 2. Idempotency

Every `POST` endpoint requires an `Idempotency-Key` header (400 if missing). The gateway hashes `(METHOD, canonical path, body)` and stores the response under `(api key, idempotency key)` in SQLite:

* Same key, same request hash: the stored status and body are returned again.
* Same key, different request hash: `409 Conflict`.

Only successful (or otherwise completed) responses are stored; errors returned before the save (validation, signature, node failure) are not cached.

---

## 3. Endpoints

### 3.1 Escrow

| Method and path | Auth | Description |
|-----------------|------|-------------|
| `POST /escrow/create` | API key + HMAC, wallet signature of the payer | Relay a `TxTypeDelegatedCreateEscrow`; returns `201` with `{"escrowId": "0x...", "payIntent": {...}}`. |
| `GET /escrow/{id}` | API key + HMAC | Returns the node's `escrow_get` result for the ID. |
| `POST /escrow/release` | API key + HMAC, wallet signature of the payee or mediator | Relay a `TxTypeDelegatedReleaseEscrow`. |
| `POST /escrow/refund` | API key + HMAC, wallet signature of the payer | Relay a `TxTypeDelegatedRefundEscrow`. |
| `POST /escrow/dispute` | API key + HMAC, wallet signature of the payer or payee | Relay a `TxTypeDelegatedDisputeEscrow`. |
| `POST /escrow/resolve` | API key + HMAC | Relay an arbitration decision with committee signatures. |

**`POST /escrow/create` body** (`EscrowCreateRequest`): `payer`, `payee` (bech32), `token`, `amount` (decimal string), `feeBps`, `deadline` (Unix seconds), `nonce` (positive integer), optional `mediator` (bech32), `meta` (hex string that decodes to exactly 32 bytes), `realm` (at most 64 characters). `payer`, `payee`, `token`, `amount`, `deadline` and `nonce` are required. `X-Sig-Addr` must equal `payer`. The escrow ID in the response is computed locally with the same rule as the chain (`keccak256(payer || payee || meta || nonce)`, see [`escrow.md`](./escrow.md)); it is returned once the transaction has been accepted by the node, not once it is in a block.

`payIntent` (`PayIntent`): `vault` (the chain's escrow vault address for the token), `token`, `amount`, `memo` (`ESCROW:` plus the upper-cased escrow ID) and `qr` (`nhb:<vault>?amount=...&memo=...&token=...`). The chain funds an escrow only through the payer's `TxTypeLockEscrow` transaction (`nhb-cli escrow fund`); a plain transfer to the vault address does not fund it.

**`release`, `refund`, `dispute` body** (`EscrowActionRequest`): `escrowId` (required), and for dispute an optional `reason`. The gateway loads the escrow with `escrow_get` and checks that `X-Sig-Addr` is an allowed party before relaying: payee or mediator for release, payer for refund, payer or payee for dispute. Responses are `202 Accepted`: `{"queued":true}` for release and refund, `{"ok":true}` for dispute. The status only means the transaction was accepted by the node.

**`POST /escrow/resolve` body** (`EscrowResolveRequest`): `escrowId`, `decision` (the decision JSON object, relayed byte-for-byte), `signatures` (array of hex strings). Response `202` with `{"queued":true}`. The gateway does not verify the quorum itself; the chain does (see [`escrow.md`](./escrow.md) section 5).

There is no `/escrow/{id}/events` endpoint. The `GET` route matches any path beginning `/escrow/`, so the whole remainder is treated as the ID.

### 3.2 P2P offers and trades

| Method and path | Auth | Description |
|-----------------|------|-------------|
| `POST /p2p/offers` | API key + HMAC, wallet signature of the seller | Store an offer in the gateway database. Returns `201` with the offer. |
| `GET /p2p/offers` | API key + HMAC | All offers, no filtering or paging. |
| `POST /p2p/accept` | API key + HMAC, wallet signature of the buyer | Always fails; see below. |
| `GET /p2p/trades/{id}` | API key + HMAC | Reads a trade row from the gateway database (404 if unknown). |

`POST /p2p/offers` body: `seller` (bech32), `baseToken`, `baseAmount`, `quoteToken`, `quoteAmount` (positive decimal strings; tokens `NHB` or `ZNHB`), optional `minAmount`, `maxAmount` (positive decimal strings), optional `terms`. The server assigns `offerId` (`OFF_` plus 32 upper-case hex characters), sets `active` to true and returns the stored offer. No node call is made and no event is emitted.

`POST /p2p/accept` validates the request and the offer, then calls the node client's `P2PCreateTrade`, which always returns the error `escrow-gateway: p2p trade creation is permanently retired -- use the P2P ZNHB market instead`. The handler answers `502 Bad Gateway`. Trade rows in the database are therefore only created by code paths that no longer run. There is no gateway endpoint to settle, dispute or resolve a trade. See [`trade.md`](./trade.md).

---

## 4. Webhooks

`main.go` starts a `WebhookWorker` on the webhook queue. Only events the gateway itself raises reach the queue, and the only such event is `escrow.created`, enqueued after a successful `POST /escrow/create`. The gateway has no feed of chain events (the watcher that polled a node event method was removed), so `escrow.funded`, `escrow.released` and the other escrow events are never delivered.

* Subscriptions are rows in the gateway's `webhooks` table (`api_key`, `event_type`, `url`, `secret`, `rate_limit` default 60, `active`). There is no REST endpoint to create them; an operator inserts rows into the SQLite database. With no matching row, nothing is delivered.
* Delivery is an HTTP `POST` with header `X-Webhook-Signature` (hex HMAC-SHA256 of the raw body with the subscription secret) and body:

  ```json
  {"type":"escrow.created","sequence":1234,"escrowId":"0x...","tradeId":"","attributes":null,
   "timestamp":"2026-01-01T00:00:00.000000000Z"}
  ```

  A `provider` object is added only when the event's attributes carry realm values (`realmScope`, `realmType`, `realmProfile`, `realmFeeBps`, `realmFeeRecipient`); the `escrow.created` event the gateway raises carries no attributes, so it never has one.
* A delivery is retried after a non-2xx response or a network error with backoff of 1s, 2s, 4s and so on, capped at 5 minutes, for at most 5 attempts (`maxWebhookAttempts`). Attempts are recorded in `webhook_attempts`. Each delivery has a 10-second HTTP timeout.
* Queue capacity, history size and entry lifetime come from `ESCROW_GATEWAY_QUEUE_CAP`, `ESCROW_GATEWAY_QUEUE_HISTORY` and `ESCROW_GATEWAY_QUEUE_TTL` (see [`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md)).

---

## 5. Errors

Errors are `{"error":"<message>"}` (double quotes in messages are replaced by single quotes). Status codes used by the handlers:

* `400`: validation failure, malformed JSON, missing `Idempotency-Key`, oversized body, realm constraint violation.
* `401`: API key/HMAC authentication failure (unknown key, bad signature, skew, nonce or timestamp replay), on any endpoint including the `GET`s.
* `403`: wallet signature missing or invalid, or signer not authorized for the action.
* `404`: unknown offer or trade.
* `409`: idempotency key reused with a different request; inactive offer.
* `500`: storage or internal failure.
* `502`: the node call failed (this includes `escrow_get` failures such as an unknown escrow, and every `POST /p2p/accept`).

Every `POST` handler, and a `GET` that fails authentication, writes an audit-log row (`audit_log` table: API key, method, path, request body, response status and body). There is no HTTP endpoint that reads it.
