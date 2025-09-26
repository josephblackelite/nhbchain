# OTC Data Model

The OTC gateway persists all operational data in PostgreSQL. Schema migrations are managed by GORM's auto-migrate feature during process startup. The key tables are:

## `branches`
Stores metadata about OTC branches and their risk controls.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `name` | Text | Unique branch name |
| `region` | Text | Geographic region identifier |
| `region_cap` | Numeric | Maximum aggregate exposure for the region |
| `invoice_limit` | Numeric | Maximum amount per invoice |
| `created_at` / `updated_at` | Timestamps | Audit timestamps |

## `users`
Holds staff members synchronized from the identity provider.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `email` | Text | Unique user email |
| `role` | Text | Staff role (`teller`, `supervisor`, `compliance`, `superadmin`, `auditor`) |
| `branch_id` | UUID | Assigned branch |
| `created_at` / `updated_at` | Timestamps | Audit timestamps |

## `invoices`
Represents OTC orders and their workflow state.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `branch_id` | UUID | Associated branch |
| `created_by_id` | UUID | Staff member who created the invoice |
| `amount` | Numeric | Order amount |
| `currency` | Text | Currency code |
| `state` | Text | Workflow state (`CREATED` → ... → `MINTED` / `REJECTED` / `EXPIRED`) |
| `region` | Text | Denormalized region for cap checks |
| `reference` | Text | Optional external reference |
| `created_at` / `updated_at` | Timestamps | Audit timestamps |

## `receipts`
Links invoices to uploaded receipt artifacts stored in S3.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `invoice_id` | UUID | Related invoice |
| `object_key` | Text | S3 key of the receipt |
| `uploaded_by` | UUID | Staff member who uploaded the receipt |
| `created_at` | Timestamp | Upload time |

## `decisions`
Records compliance and supervisory actions.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `invoice_id` | UUID | Related invoice |
| `actor_id` | UUID | Approver/reviewer |
| `outcome` | Text | `approved`, `rejected`, etc. |
| `notes` | Text | Optional comments |
| `created_at` | Timestamp | Decision time |

## `vouchers`
Contains voucher payloads for downstream on-chain minting.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `invoice_id` | UUID | One-to-one with invoices |
| `chain_id` | Text | Chain identifier |
| `payload` | Text | Serialized voucher payload |
| `created_at` | Timestamp | Creation time |

## `events`
Immutable audit trail capturing all staff interactions.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Primary key |
| `invoice_id` | UUID nullable | Related invoice when applicable |
| `user_id` | UUID | Actor identifier |
| `action` | Text | Action name (`invoice.created`, etc.) |
| `details` | Text | Structured metadata |
| `created_at` | Timestamp | Event time |

## Idempotency keys
The service also persists an `idempotency_keys` table (managed automatically) to replay responses for duplicate requests.
