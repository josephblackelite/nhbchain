# OTC Gateway API

All endpoints require OIDC SSO access tokens combined with a WebAuthn assertion. For the reference implementation this is represented by the headers:

- `Authorization: Bearer <subject>|<role>`
- `X-WebAuthn-Verified: true`
- `Idempotency-Key: <uuid>` (optional but recommended for POST requests)

Supported roles are `teller`, `supervisor`, `compliance`, `superadmin`, and `auditor`.

Base path: `/api/v1`

## `POST /invoices`
Create a new OTC invoice.

```json
{
  "branch_id": "uuid",
  "amount": 1234.56,
  "currency": "USD",
  "reference": "optional external reference"
}
```

- Roles: teller, supervisor, superadmin
- Response: `201 Created` with the created invoice object.

## `POST /invoices/{id}/receipt`
Register an uploaded receipt for the invoice and transition the order to `RECEIPT_UPLOADED`.

```json
{
  "object_key": "s3://bucket/path/to/object"
}
```

- Roles: teller, supervisor, superadmin
- Response: `200 OK` with `{ "status": "RECEIPT_UPLOADED" }`

## `POST /invoices/{id}/pending-review`
Advance the invoice to `PENDING_REVIEW` for compliance review.

- Roles: supervisor, compliance, superadmin
- Response: `200 OK`

## `POST /invoices/{id}/approve`
Approve the invoice, enforcing branch per-invoice limits and regional caps.

```json
{
  "notes": "Optional approval notes"
}
```

- Roles: supervisor, compliance, superadmin
- Response: `200 OK` with `{ "status": "APPROVED" }`
- Errors: `400 Bad Request` when limits are exceeded, `404 Not Found` if the invoice does not exist.

## `GET /invoices/{id}`
Retrieve invoice details, including receipts and decisions for auditors.

- Roles: auditor, supervisor, compliance, superadmin
- Response: `200 OK` with the invoice document.

## `POST /ops/otc/invoices/{id}/sign-and-submit`
Sign an approved invoice using the HSM-backed minter and submit the resulting voucher to the NHB swap RPC.

```json
{
  "recipient": "nhb1...",
  "amount": "1000000000000000000",
  "token": "NHB",
  "provider_tx_id": "optional-deterministic-id"
}
```

- Roles: superadmin
- Response: `200 OK` with `{ "status": "SUBMITTED" | "MINTED", "txHash": "0x...", "voucherHash": "0x...", "providerTxId": "...", "signature": "0x..." }`
- Errors:
  - `400 Bad Request` when maker-checker rules or branch caps are violated, or payload validation fails.
  - `409 Conflict` when the supplied `provider_tx_id` has already been processed for another invoice.
  - `503 Service Unavailable` when the signing service is not configured.

## Errors

Errors are returned as plain text with relevant HTTP status codes. Clients should treat non-2xx responses as failures.

## Idempotency

When `Idempotency-Key` is supplied, the service records the first response generated for a given key and replays it for any subsequent identical request. Idempotency is currently scoped to the entire path and method pair.
