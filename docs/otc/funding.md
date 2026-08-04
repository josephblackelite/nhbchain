# OTC Funding Verification Workflow

The OTC gateway now records fiat settlement state alongside each invoice and voucher. This
section outlines how operations and compliance teams should verify deposits before
initiating mint requests.

## Data model extensions

Invoices and vouchers carry additional fiat metadata:

- **FiatAmount / FiatCurrency** – the confirmed gross amount and currency remitted by the
  counterparty.
- **FundingStatus** – `PENDING`, `CONFIRMED`, or `REJECTED` to track settlement progress.
- **FundingReference** – the bank, custodian, or internal reference that links the fiat
  deposit to an OTC order.
- **FIAT_CONFIRMED state** – invoices transition from `APPROVED` into `FIAT_CONFIRMED` once
  funding has been validated. Only FIAT_CONFIRMED invoices may be signed or submitted on
  chain.

## Confirmation flow

1. **Bank or custodian webhook** – the new `/integrations/otc/funding/webhook` endpoint
   accepts authenticated notifications. Each payload must include the invoice identifier,
   the fiat amount and currency, the dossier key on file, and the external funding
   reference.
2. **Dossier validation** – the webhook processor loads the originating partner, enforces
   that the partner is approved, and compares the submitted dossier key against the latest
   KYB dossier stored in the system. Mismatches raise errors for manual review.
3. **State transition** – confirmed notifications update the invoice with fiat metadata,
   mark the funding status as `CONFIRMED`, and move the invoice into `FIAT_CONFIRMED`.
   An audit trail entry (`invoice.funding_confirmed`) captures the reference, custodian,
   and amount.
4. **Sign and submit** – the signer enforces that invoices remain `FIAT_CONFIRMED` and that
   fiat metadata is present before minting. Funding references are recorded alongside the
   voucher hash for downstream reconciliation.

## Compliance dashboard

The Operations Console includes a "Funding Assurance" section intended to surface:

- Counts of pending, confirmed, and rejected wires.
- Confirmed fiat volume versus total notional to highlight reconciliation progress.
- A live checklist of invoices still awaiting confirmation, including branches, amounts,
  and last update timestamps.

This dashboard currently shows only hardcoded sample data, not live funding state:
`services/otc-ops-ui`'s compliance dashboard is fed exclusively by an in-memory
`InvoiceStore` seeded with 3 hardcoded sample invoices, and it never calls the real
otc-gateway REST API or its Postgres database. Do not rely on it to confirm the funding
queue is clear before invoking Sign & Submit.

## Reconciliation expectations

- **Pending queue** – entries should be worked within the same business day. Escalate
  cases that remain pending beyond 24 hours.
- **Funding references** – every confirmed invoice must store the reference provided by the
  banking or custody platform. This identifier is logged with the voucher hash and must be
  supplied to reconciliation when matching on-chain mints to fiat deposits.
- **Rejected wires** – rejection handling is not currently implemented. `Processor.Process`
  decodes the notification's `Status` field but never branches on it, so every valid webhook
  notification is treated unconditionally as a confirmation regardless of the status it
  carries. There is currently no path that sets `FundingStatusRejected` or moves an invoice
  back to `PENDING_REVIEW`. Treat rejected wires as a manual, out-of-band process until this
  is built.
- **Monitoring** – the reconciliation ratio on the dashboard should trend toward 100% prior
  to mint windows. Investigate any delta between confirmed fiat volume and OTC notional
  before releasing vouchers.

Following this workflow ensures fiat deposits are verified, auditable, and fully traceable
through mint submission.
