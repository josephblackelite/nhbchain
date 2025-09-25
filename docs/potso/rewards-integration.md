# POTSO Rewards Accounting Integration

This guide explains how to consume POTSO reward payouts from the NHB node for
external accounting systems. It covers reward calculation guarantees, ledger
exports, webhook contracts, and operational checklists for finance teams.

## Reward Calculation Overview

* Rewards are calculated per epoch from the configured emission pool. Each
  participant's share is derived from their composite weight after normalising
  the epoch weights. Duplicate addresses are merged and weights must be
  non-negative.
* Payout amounts are determined by the formula `floor(epochPool * weight /
  totalWeight)` for each address. Integer division ensures deterministic
  rounding. The difference between the emission pool and the sum of payouts is
  stored as **rounding dust**.
* Rounding dust is preserved in an internal bucket and automatically added to
  the next epoch's pool. This guarantees that, over time, total payments across
  epochs match the theoretical emission totals.
* Every ledger entry includes a deterministic checksum based on
  `(epoch|address|amount)` that serves as an idempotency key for exports and
  payment reconciliation.

## Reward Ledger Schema

The reward ledger persists the following fields for each participant and epoch:

| Field        | Description                                                      |
|--------------|------------------------------------------------------------------|
| `epoch`      | Epoch number associated with the payout.                         |
| `address`    | 20-byte NHB account encoded as lowercase hex with `0x` prefix.   |
| `amount`     | Decimal string in wei precision (smallest ZNHB unit).            |
| `currency`   | Token symbol, currently `ZNHB`.                                  |
| `status`     | `ready` or `paid`.                                               |
| `generatedAt`| RFC3339 timestamp when the ledger entry was created.             |
| `updatedAt`  | RFC3339 timestamp for the last status change.                    |
| `paidAt`     | RFC3339 timestamp when marked paid (omitted for `ready`).        |
| `txRef`      | External transaction reference recorded during settlement.       |
| `checksum`   | Hex-encoded SHA-256 checksum `(epoch|address|amount)`.           |

## JSON-RPC Interfaces

### `potso_getRewards`

Fetch reward ledger entries. Parameters are optional filters:

```json
{
  "epoch": 123,
  "address": "0xabc…",
  "status": "ready",
  "page": { "cursor": "20", "limit": 50 }
}
```

Returns an array of `RewardEntry` objects sorted by `(epoch, address)` and a
`nextCursor` when more pages are available. If `address` is supplied the
response is scoped to that participant.

### `potso_exportRewards`

Generate a deterministic export for an epoch. Parameters:

* `format`: `"csv"` or `"jsonl"`.
* `epoch`: Target epoch number.
* `page` (optional): paginate large exports with the same cursor structure as
  `potso_getRewards`.

Response includes the serialised payload (base64 for CSV) or a temporary URL
backed by object storage, and the SHA-256 checksum of the export. Totals in the
export never exceed the configured epoch emission.

### `potso_markRewardsPaid`

Mark rewards settled using the idempotency key `(epoch,address,amount)`. Request
body:

```json
{
  "epoch": 123,
  "txRef": "L1-2024-05-31",
  "entries": [
    { "address": "0xabc…", "amount": "1000000000000000000" }
  ]
}
```

The method returns the count of entries transitioned from `ready` to `paid`.
Repeated calls with the same payload are safe. Metadata recorded for audit:

* `paidAt`: server timestamp (or client supplied, if provided).
* `txRef`: passthrough string used for cross-system reconciliation.
* `paidBy`: derived from the authenticated RPC caller.

## Export Formats

### CSV Schema (v1)

```
epoch,address,amount,currency,status,generated_at,checksum
```

* `epoch`: Unsigned integer.
* `address`: Lowercase hex NHB address with `0x` prefix.
* `amount`: Integer string in wei.
* `currency`: Token symbol (e.g. `ZNHB`).
* `status`: `ready` or `paid`.
* `generated_at`: RFC3339 timestamp (nanosecond precision).
* `checksum`: Hex SHA-256 of `(epoch|address|amount)`.

### JSONL Schema (v1)

Each line is a JSON object with the same fields as the CSV header. The payload
checksum reported by the RPC method is the SHA-256 hash of the raw JSONL bytes.

### Pagination

Both exporters accept pagination parameters. When `page.limit` is provided the
node emits deterministic slices of the ledger. Each response includes a
`nextCursor`. Clients can call subsequent pages until the cursor is empty.

## Webhook Contracts

Two webhook events allow external systems to react to ledger transitions. Every
request is signed with an HMAC-SHA256 header (`X-NHB-Signature`) using the shared
secret configured on the node. The header format is `sha256=<hex digest>` over
the UTF-8 request body.

| Event                | Payload Fields                                      |
|----------------------|-----------------------------------------------------|
| `potso.rewards.ready`| `epoch`, `count`, `exportUrls`, `checksum`, `generatedAt`, `deliveryId` |
| `potso.rewards.paid` | `epoch`, `count`, `txRef`, `paidAt`, `deliveryId`    |

Additional headers:

* `X-NHB-Event`: matches the event type.
* `X-NHB-Signature`: HMAC signature for verification.

### Retry & Idempotency

* The dispatcher retries failed deliveries up to five times with exponential
  backoff (2s → 4s → 8s … capped at 30s).
* Consumers should verify the `deliveryId` and checksum to deduplicate repeated
  notifications.
* When handling ready events use the supplied export URLs and checksum to
  download and verify the ledger slice.

### Signature Verification Example

```go
func verify(body []byte, signature string, secret []byte) bool {
    mac := hmac.New(sha256.New, secret)
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}
```

## Accounting Checklist

1. **Subscribe to webhooks** to receive `potso.rewards.ready` notifications.
2. **Download exports** immediately, verifying the checksum against the webhook
   payload.
3. **Import into accounting system**, applying the `checksum` field as a unique
   key to avoid double counting.
4. **Settle payouts** from treasury accounts and call `potso_markRewardsPaid`
   with the settlement batch and transaction reference.
5. **Archive exports** and reconciliation reports for audit. Include the
   epoch-level checksum in filings.
6. **Monitor retries**: configure alerting for webhook delivery failures on the
   node operator dashboard.

Following this workflow ensures reward distributions never exceed the emission
budget and downstream ledgers remain perfectly aligned with on-chain state.
