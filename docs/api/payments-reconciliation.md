# Payments Gateway Reconciliation

The `payments-gateway` now exposes internal reconciliation endpoints for mint-side
commerce reporting. These endpoints are intended for operator dashboards, finance
pipelines, and partner support tooling that need more than single-invoice lookups.

## Endpoints

### `GET /invoices`

Returns invoice rows joined with their originating quote data.

Supported query parameters:

* `status`
* `recipient`
* `created_from`
* `created_to`
* `updated_from`
* `updated_to`
* `limit`

Example:

```http
GET /invoices?status=minted&recipient=nhb1report&limit=100
```

Response shape:

```json
{
  "total": 1,
  "items": [
    {
      "invoiceId": "9e2d...",
      "quoteId": "8ef3...",
      "recipient": "nhb1report",
      "status": "minted",
      "fiat": "USD",
      "token": "NHB",
      "amountFiat": "35",
      "amountToken": "7",
      "quoteExpiry": "2026-04-12T10:01:00Z",
      "createdAt": "2026-04-12T10:00:00Z",
      "updatedAt": "2026-04-12T10:00:10Z",
      "nowpaymentsId": "np-report-1",
      "nowpaymentsUrl": "https://nowpay/invoice/np-report-1",
      "txHash": "0xdeadbeef"
    }
  ]
}
```

### `GET /reconciliation/summary`

Aggregates invoice counts and fiat/token totals by status for the selected filter
window.

Example:

```http
GET /reconciliation/summary?created_from=2026-04-12T00:00:00Z&created_to=2026-04-13T00:00:00Z
```

Response shape:

```json
{
  "countByStatus": {
    "minted": 12,
    "pending": 3
  },
  "amountFiatByStatus": {
    "minted": "420.00",
    "pending": "95.00"
  },
  "amountTokenByStatus": {
    "minted": "84",
    "pending": "19"
  },
  "totalInvoices": 15,
  "mintedInvoices": 12,
  "pendingInvoices": 3,
  "errorInvoices": 0
}
```

### `GET /reconciliation/export`

Exports the filtered reconciliation rows in either JSON or CSV form.

Supported query parameters:

* every filter supported by `GET /invoices`
* `format=json|csv`

Examples:

```http
GET /reconciliation/export?format=json&status=minted
```

```http
GET /reconciliation/export?format=csv&created_from=2026-04-12T00:00:00Z
```

CSV columns:

* `invoice_id`
* `quote_id`
* `recipient`
* `status`
* `fiat`
* `token`
* `amount_fiat`
* `amount_token`
* `quote_expiry`
* `created_at`
* `updated_at`
* `nowpayments_id`
* `nowpayments_url`
* `tx_hash`

## Why this matters

These endpoints let operators and finance systems reconcile:

* quote issuance
* invoice creation
* NowPayments invoice references
* mint completion status
* final mint transaction hashes

That makes the mint rail substantially easier to monitor, export, and match against
external partner reports without replaying one invoice at a time.
