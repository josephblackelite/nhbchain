# Merchant Tooling Guide

Task 5 introduces a dedicated toolset for merchants operating NHBChain-based escrow and P2P commerce flows. This guide covers
settlement exports, reconciliation dashboards, and sandbox simulators to validate integrations before production launch.

---

## 1. Settlement Exports

The gateway now delivers both synchronous and asynchronous settlement exports covering dual-lock trade activity.

### 1.1 Export formats

* **CSV:** Comma-separated format optimised for spreadsheets and accounting systems.
* **JSON:** Structured format for ingestion into data pipelines.
* **Column set:**
  | Column | Description |
  |--------|-------------|
  | `trade_id` | Trade identifier (32-byte hex). |
  | `escrow_base_id` / `escrow_quote_id` | Escrow leg identifiers. |
  | `buyer` / `seller` | Bech32 addresses of trade parties. |
  | `base_token` / `quote_token` | Token symbols for each leg. |
  | `base_gross_amount` / `quote_gross_amount` | Amount deposited before fees. |
  | `base_fee_amount` / `quote_fee_amount` | Fees deducted during release. |
  | `base_net_amount` / `quote_net_amount` | Amount paid out after fees. |
  | `settled_at` | RFC 3339 timestamp of on-chain settlement. |
  | `settlement_tx_hash` | Transaction hash executing atomic settlement. |
  | `resolution_type` | `settled`, `refunded`, or `arbitrated`. |
  | `idempotency_key` | Source idempotency key supplied by merchant, if any. |

### 1.2 Accessing exports

* **Synchronous:** `GET /exports/settlements` (see `/docs/escrow/gateway-api.md`) supports paging and filtering by date, token, or
  merchant ID. Use for up to 31 days of data.
* **Asynchronous:** `POST /exports/settlements` initiates a job for larger ranges (up to 365 days). Supported delivery targets:
  * `s3` – Provide bucket ARN and role assumption details.
  * `gcs` – Provide service account and bucket path.
  * `webhook` – Gateway delivers archive to a signed URL you control.
* Poll job status via `GET /exports/settlements/{job_id}`. Completed jobs include checksum (SHA256) and row count for validation.

### 1.3 Automating ingestion

1. Schedule nightly asynchronous exports covering the prior day (`settled_from`/`settled_to`).
2. Validate checksum and row count against expectations.
3. Persist records to your finance system and mark them reconciled once matched against on-chain events (§2).
4. Store idempotency keys and transaction hashes for audit trails.

---

## 2. Reconciliation Dashboard

The merchant console ships with a reconciliation dashboard designed for operations and finance teams.

### 2.1 Data sources

* **Gateway exports.** Feeds settlement totals, disputes, and refunds.
* **On-chain indexer.** Provides real-time escrow/trade status by subscribing to events listed in `/docs/escrow/escrow.md`.
* **Webhook activity.** Dashboard displays delivery success metrics and outstanding retries.

### 2.2 Key widgets

* **Settlement summary.** Aggregates gross and net volume per token, fee totals, and dispute rate for the selected period.
* **Breakage monitor.** Highlights trades stuck in `TradePartialFunded` or `TradeDisputed` for longer than configurable thresholds.
* **Event ledger.** Chronological log combining gateway events and on-chain confirmations; supports CSV export for auditors.
* **SLA compliance.** Visualizes REST latency metrics retrieved from `GET /metrics/sla` alongside custom alert thresholds.

### 2.3 Workflow

1. **Import exports.** Upload nightly CSV/JSON or connect your data lake via API keys.
2. **Match to chain.** Dashboard runs automatic matching by comparing `trade_id`, `escrow_id`, and `tx_hash` between exports and
   on-chain events. Mismatches are flagged for review.
3. **Resolve exceptions.** Operators can drill into disputes, trigger reminders, or escalate to arbitrators directly from the
   dashboard.
4. **Sign-off.** Generate a reconciliation report (PDF/CSV) summarizing matched volume, outstanding disputes, and SLA compliance.

### 2.4 Access control

* **Roles:** `Finance`, `Operations`, `Support`, and `Admin`. Each role grants differing access to export downloads, dispute
  actions, and alert configuration.
* **Audit logging:** All dashboard actions log to `GET /audit/logs` (see gateway API). Include user, timestamp, and idempotency key
  when available.

---

## 3. Sandbox Simulator

Use the sandbox to rehearse complex trade flows and validate your automation prior to production.

### 3.1 Components

* **Faucet service (`POST /sandbox/faucet`).** Seeds buyer/seller wallets with NHB and ZNHB test balances.
* **Trade simulator UI.** Visualizes both escrow legs, funding status, and event timeline. Supports manual triggers for dispute and
  settlement paths.
* **Automated scripts.** Sample scripts (TypeScript & Python) demonstrate creating trades, funding legs, and invoking settlement
  via RPC and REST endpoints.

### 3.2 Supported scenarios

| Scenario | Purpose |
|----------|---------|
| Happy path settlement | Fund both legs, execute `p2p_settle`, observe atomic release and matching gateway webhook. |
| Buyer default | Fund seller leg only, trigger expiry, confirm refunds occur for both legs. |
| Dispute & arbitration | Open dispute, simulate arbitrator outcome (release or refund), verify dashboard updates. |
| Retry safety | Repeat `settle` or `resolve` with same `Idempotency-Key` to confirm idempotent responses. |

### 3.3 Running simulations

1. Obtain sandbox API key and wallet test keys from the merchant console.
2. Run sample script: `npm run simulate:settlement -- --buyer <addr> --seller <addr>`.
3. Monitor emitted events via WebSocket (`wss://sandbox.api.nhbcoin.net/escrow/events`).
4. Review sandbox dashboard metrics (`https://sandbox.console.nhbcoin.net/commerce`) to ensure exports and SLA graphs render as
   expected.

### 3.4 Graduation checklist

* All integration tests pass in sandbox (settlement, dispute, refund).
* Nightly export ingestion job processes at least three consecutive days without errors.
* Reconciliation dashboard shows zero unmatched records for the test period.
* Alerting webhooks successfully receive SLA breach test notifications.

---

## 4. Support & Resources

* **Documentation:** `/docs/escrow/escrow.md` (state machine), `/docs/escrow/gateway-api.md` (REST operations).
* **SDKs:** TypeScript & Go SDKs include helpers for signing requests and consuming paginated endpoints.
* **Contact:** Reach the merchant integrations team at `merchants@nehborly.net` for sandbox access, production onboarding, or
  escalation support.
