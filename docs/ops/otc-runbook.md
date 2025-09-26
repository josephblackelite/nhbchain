# OTC Operations Runbook

This runbook covers the end-to-end workflow for receipt, review, approval, and SuperAdmin execution flows within the OTC Operations Console located at `services/otc-ops-ui`.

## 1. Environment Preparation

1. Install dependencies and start the console:
   ```bash
   cd services/otc-ops-ui
   npm install
   npm run dev
   ```
2. Export optional variables:
   - `WEBHOOK_ENDPOINT` – HTTPS endpoint for lifecycle notifications.

3. Ensure Prometheus scrapes `http://<host>:<port>/api/metrics`.

## 2. Invoice Intake

1. Select the **Receipt** stage tab.
2. Apply filters for branch, amount, or due date as needed.
3. Validate uploaded evidence and due date.
4. Trigger **Escalate** for anomalies (moves invoice into Review queue) or log **Reject** with note.
5. Record upload problems via **Reject** using reason note; metrics counter `otc_receipt_upload_failures_total` increments automatically when failures are tracked upstream.

## 3. Review Stage

1. Choose the **Review** tab.
2. Confirm counterparty data, rate/TWAP, and cap bucket.
3. Use **Approve** to move forward, or **Escalate** to re-route back to Review leads.
4. Add investigation notes before action submission; notes appear in timeline.
5. Ensure branch and treasury reviewers coordinate via timeline entries.

## 4. Approval Stage

1. Select **Approval** tab for invoices waiting on sign-off.
2. Treasury approvers execute **Approve** once compliance checks pass.
3. SuperAdmin role performs **Sign** when voucher is ready and enters the signer transaction hash.
4. SuperAdmin completes **Sign & Submit** with on-chain `txHash`. This action updates Prometheus `otc_mint_success_total` and emits webhooks containing voucherId and txHash.

## 5. Completed & Rejected Monitoring

- **Completed** tab tracks invoices that are approved or submitted.
- **Rejected** tab aggregates all rejections for audit.
- Export any queue to CSV via **Export CSV** (respects applied filters).

## 6. Webhook Validation

1. Configure a listener endpoint that logs request bodies.
2. Perform each workflow action (Approve, Reject, Escalate, Sign & Submit).
3. Confirm payload fields: `invoiceId`, `voucherId`, `txHash`, `stage`, `status`, `evidence`, and full `timeline`.
4. Investigate failures via server logs under `WEBHOOK_ENDPOINT not configured` or HTTP status error messages.

## 7. Metrics Validation

1. Access `GET /api/metrics` to confirm the exporter responds.
2. Ensure gauges and counters are present:
   - `otc_invoice_volume_total`
   - `otc_approval_latency_seconds`
   - `otc_receipt_upload_failures_total`
   - `otc_cap_usage_ratio`
   - `otc_mint_success_total`
   - `otc_signer_health`
3. After workflow actions, verify the corresponding metrics increase or update.

## 8. Dry-Run Acceptance Test

1. Start the UI (`npm run dev`) and Prometheus scrape (optional via curl).
2. Run through a sample branch workflow:
   - Escalate a receipt to Review.
   - Approve in Review.
   - Sign and Submit via SuperAdmin.
3. Export CSV while filters applied.
4. Confirm webhook receiver captured each lifecycle event.
5. Validate Prometheus metrics changed for approval latency and mint success.

## 9. Incident Response

- If signer health drops, set `otc_signer_health` gauge to `0` via manual override script or environment check within the service.
- For webhook failures, inspect logs for dispatch errors and retry actions when endpoint is restored.
- Use timeline entries for audit evidence during RCA.

## 10. Rollback / Recovery

- Restart service with `npm run dev` (local) or redeploy container.
- In-memory store reloads seed data from `data/invoices.ts`; import actual data through API integration when available.
- For corrupted metrics, restart process to reset counters.

