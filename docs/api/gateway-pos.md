# POS gateway HTTP API

The POS gateway exposes a lightweight HTTP surface for POS terminals and
merchant middleware to submit signed NHB Pay intents and poll their lifecycle
status. The gateway accepts the canonical NHB Pay URI payload described in
[the POS intent spec](../specs/nhb-pay.md) and proxies submissions to the node's
gRPC transaction service.

All endpoints live under the `/api/pos` prefix and require TLS in production.
Unless stated otherwise the gateway returns JSON responses and the standard
`Content-Type: application/json` header.

## `POST /api/pos/intents`

Submit a signed NHB Pay intent. The gateway validates the payload, enqueues it
through the POS Tx gRPC service, and returns an authorization reference that can
be used for follow-up capture or void operations.

### Request body

```json
{
  "intentRef": "0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01",
  "uri": "nhbpay://intent/2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01?amount=15.25&currency=USD&expiry=1707436800&merchant=nhb1m0ckmerchantaddre55&device=kiosk-7&paymaster=nhb1sponsorship&sig=5b0481e43cbb27c4c76bf0fa104d8a2ffb329a84797d0c0edc55fb6a2dcef0125c7d4090560ce10a4bf845ba1b4c745cf3e5012ef0d8c2a8d98d00ab91c5dd1a",
  "payer": "nhb1samplepayer"
}
```

| Field | Type | Description |
| --- | --- | --- |
| `intentRef` | string | Lowercase hex-encoded 32 byte reference. |
| `uri` | string | Canonical NHB Pay URI containing all metadata and merchant signature. |
| `payer` | string | NHB address of the payer/customer wallet. |
| `metadata` | object (optional) | Additional device or cashier metadata echoed back in status responses. |

The gateway derives the amount, currency, expiry, and merchant address from the
URI. Requests missing mandatory query parameters or with invalid signatures are
rejected with `400 Bad Request`.

### Response

```json
{
  "authorizationId": "auth_01hatz8k8k8c8n3qsk4x3k8s1x",
  "intentRef": "0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01",
  "status": "pending",
  "submittedAt": "2024-02-08T12:00:00Z"
}
```

| Field | Type | Description |
| --- | --- | --- |
| `authorizationId` | string | Reference returned by the POS gRPC Tx service. |
| `intentRef` | string | Echoes the submitted intent reference. |
| `status` | string | Initial lifecycle status (`pending` on success). |
| `submittedAt` | RFC3339 string | Timestamp the gateway accepted the submission. |

Successful submissions return HTTP `202 Accepted` along with the body above.

The gateway returns the following status codes:

| Code | Description |
| --- | --- |
| `202` | Intent accepted and forwarded to the network. |
| `400` | Malformed request, missing signature, or replayed intent. |
| `409` | Intent already consumed on-chain. |
| `422` | Intent expired. |
| `500` | Upstream gRPC or consensus error. |

## `GET /api/pos/intents/{intentRef}`

Fetch the lifecycle status for a previously submitted intent. This endpoint
combines the gateway submission log with realtime finality updates streamed from
`pos.v1.Realtime`.

### Path parameters

| Parameter | Description |
| --- | --- |
| `intentRef` | Lowercase hex string identifying the intent. |

### Response

```json
{
  "intentRef": "0x2d8c7fd3e1a94f4c998e4cfedc3a4567bb12aa09887766554433221100ff9a01",
  "authorizationId": "auth_01hatz8k8k8c8n3qsk4x3k8s1x",
  "status": "finalized",
  "merchant": "nhb1m0ckmerchantaddre55",
  "amount": "15.25",
  "currency": "USD",
  "payer": "nhb1samplepayer",
  "paymaster": "nhb1sponsorship",
  "device": "kiosk-7",
  "submittedAt": "2024-02-08T12:00:00Z",
  "finalizedAt": "2024-02-08T12:00:05Z",
  "txHash": "0xe314f9f6f1c80c7a6f332cb4988b0d0d7aab70f6ea51a6f7cd2d6fef3b8c2c77",
  "height": 145902,
  "cursor": "pos-finality-145902-7"
}
```

`status` transitions through the following states:

| Status | Meaning |
| --- | --- |
| `pending` | Intent accepted and awaiting block inclusion. |
| `finalized` | Intent finalized in a BFT block. |
| `rejected` | Downstream node rejected the transaction (includes `errorCode`). |
| `expired` | Gateway dropped the intent because `expiry` elapsed before acceptance. |

If the intent is unknown the gateway returns `404 Not Found`.

### Polling strategy

Gateways retain submission metadata for at least 24 hours. Terminals SHOULD
prefer the realtime gRPC/WebSocket stream for live updates and only poll the
status endpoint during recovery or when a terminal cannot maintain a streaming
connection.

## Error schema

Errors use the following JSON structure:

```json
{
  "error": {
    "code": "INTENT_EXPIRED",
    "message": "intent expiry 1707436800 is in the past"
  }
}
```

| Field | Description |
| --- | --- |
| `code` | Stable identifier for programmatic handling. |
| `message` | Human-readable description. |

Common error codes include `INVALID_SIGNATURE`, `INTENT_CONSUMED`,
`INTENT_EXPIRED`, and `UPSTREAM_UNAVAILABLE`.
