# Operator Reporting Service

`ops-reporting` provides a unified read-only operator view across the mint, merchant,
treasury, and payout rails. It is intended for finance, treasury, and operations teams
that need one place to inspect inbound funding, commerce activity, treasury movement,
and outbound redemptions.

## Purpose

The service closes the reporting gap between:

* `payments-gateway`, which tracks quotes, invoices, NowPayments references, and mint
  settlement hashes
* `escrow-gateway`, which tracks P2P and merchant trade lifecycle state
* `payoutd`, which tracks treasury instructions for refills and sweeps
* `payoutd`, which now also tracks payout execution outcomes for redemptions

This gives NHBChain operators a lightweight reconciliation surface before a full BI or
data-warehouse pipeline is in place.

## Configuration

Environment variables:

* `OPS_REPORT_LISTEN` - HTTP bind address. Default: `:8091`
* `OPS_REPORT_PAYMENTS_DB` - path to the payments SQLite database. Default:
  `payments-gateway.db`
* `OPS_REPORT_ESCROW_DB` - path to the escrow-gateway SQLite database. Default:
  `escrow-gateway.db`
* `OPS_REPORT_TREASURY_DB` - path to the payout treasury instruction Bolt database.
  Default: `nhb-data-local/payoutd/treasury.db`
* `OPS_REPORT_PAYOUT_DB` - path to the payout execution Bolt database. Default:
  `nhb-data-local/payoutd/executions.db`
* `OPS_REPORT_BEARER_TOKEN` - required bearer token protecting all reporting endpoints
  except health checks

Example:

```powershell
$env:OPS_REPORT_LISTEN=":8091"
$env:OPS_REPORT_PAYMENTS_DB="C:\nhb\payments-gateway.db"
$env:OPS_REPORT_ESCROW_DB="C:\nhb\escrow-gateway.db"
$env:OPS_REPORT_TREASURY_DB="C:\nhb\nhb-data-local\payoutd\treasury.db"
$env:OPS_REPORT_PAYOUT_DB="C:\nhb\nhb-data-local\payoutd\executions.db"
$env:OPS_REPORT_BEARER_TOKEN="replace-me"
go run ./services/ops-reporting
```

## Endpoints

Health:

* `GET /healthz`

Authenticated endpoints:

* `GET /summary`
* `GET /mint/invoices`
* `GET /mint/export?format=json|csv`
* `GET /merchant/trades`
* `GET /merchant/export?format=json|csv`
* `GET /treasury/instructions`
* `GET /treasury/export?format=json|csv`
* `GET /payout/executions`
* `GET /payout/export?format=json|csv`

Authentication:

```text
Authorization: Bearer <OPS_REPORT_BEARER_TOKEN>
```

## Summary response

`GET /summary` returns a combined payload with:

* mint invoice totals by status
* mint fiat/token aggregates by status
* merchant trade totals by status
* treasury instruction totals by status and action
* payout execution totals by status and asset
* generation timestamp

Example:

```json
{
  "generatedAt": "2026-04-12T10:30:00Z",
  "mint": {
    "countByStatus": {
      "minted": 12,
      "pending": 2
    },
    "amountFiatByStatus": {
      "minted": "600",
      "pending": "40"
    },
    "amountTokenByStatus": {
      "minted": "600",
      "pending": "40"
    },
    "totalInvoices": 14,
    "mintedInvoices": 12,
    "pendingInvoices": 2,
    "errorInvoices": 0
  },
  "merchant": {
    "countByStatus": {
      "settled": 10,
      "funded": 2
    },
    "totalTrades": 12,
    "settledTrades": 10,
    "openTrades": 2,
    "disputedTrades": 0
  },
  "treasury": {
    "countByStatus": {
      "pending": 1,
      "approved": 3
    },
    "countByAction": {
      "refill": 2,
      "sweep": 2
    },
    "total": 4,
    "pending": 1,
    "approved": 3,
    "rejected": 0
  },
  "payout": {
    "countByStatus": {
      "settled": 8,
      "failed": 1
    },
    "countByAsset": {
      "USDC": 5,
      "USDT": 4
    },
    "totalExecutions": 9,
    "settled": 8,
    "failed": 1,
    "aborted": 0,
    "processing": 0
  }
}
```

## Filtering

Mint filters:

* `status`
* `recipient`
* `created_from`
* `created_to`
* `updated_from`
* `updated_to`
* `limit`

Treasury filters:

* `status`
* `action`
* `asset`
* `limit`

Merchant filters:

* `status`
* `seller`
* `buyer`
* `limit`

Payout filters:

* `status`
* `asset`
* `limit`

Timestamps must be RFC 3339.

Examples:

```bash
curl https://ops-reporting.example.com/summary \
  -H "Authorization: Bearer ${OPS_REPORT_BEARER_TOKEN}"
```

```bash
curl "https://ops-reporting.example.com/mint/invoices?status=minted&recipient=nhb1merchant&limit=50" \
  -H "Authorization: Bearer ${OPS_REPORT_BEARER_TOKEN}"
```

```bash
curl "https://ops-reporting.example.com/treasury/export?status=pending&format=csv" \
  -H "Authorization: Bearer ${OPS_REPORT_BEARER_TOKEN}"
```

```bash
curl "https://ops-reporting.example.com/merchant/trades?status=settled&seller=nhb1merchant" \
  -H "Authorization: Bearer ${OPS_REPORT_BEARER_TOKEN}"
```

```bash
curl "https://ops-reporting.example.com/payout/executions?status=failed&asset=USDT" \
  -H "Authorization: Bearer ${OPS_REPORT_BEARER_TOKEN}"
```

## Operational notes

* This service is read-only. It does not create invoices, process payouts, settle
  trades, or approve treasury instructions.
* The service expects the `payments-gateway` SQLite schema, `escrow-gateway` SQLite
  schema, and `payoutd` treasury and payout stores to already exist.
* The service is designed for direct operator consumption, finance exports, and simple
  dashboard integrations while broader cross-rail reporting continues to expand.
