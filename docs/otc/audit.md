# OTC Audit Trail

The OTC gateway captures audit data in the `events` table to provide an immutable record of staff activity.

## Event Generation

The service records events for:

- Invoice creation, receipt uploads, state transitions, and approvals.
- Future enhancements such as rejections, expirations, voucher minting, and login audits.

Each event stores:

| Field | Description |
| --- | --- |
| `id` | Event UUID |
| `invoice_id` | Related invoice when applicable |
| `user_id` | Staff member performing the action |
| `action` | Machine-readable action name (e.g., `invoice.created`, `invoice.APPROVED`) |
| `details` | Optional contextual metadata |
| `created_at` | Timestamp in the configured timezone |

## Consumption

Auditors can query `/api/v1/invoices/{id}` using the `auditor` role to retrieve invoice decisions and receipts. Direct SQL access to the `events` table enables chronological review for entire branches or regions.

## Idempotency and Auditing

The idempotency middleware prevents duplicate inserts when clients retry POST requests, ensuring that each successful operation creates exactly one audit event.
