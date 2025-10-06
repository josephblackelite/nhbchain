# OTC Partner Onboarding Runbook

This runbook codifies the standard operating procedure for onboarding a new OTC partner from application through production activation.

## 1. Intake and Account Provisioning

1. **Initiate partner record:**
   - Partner administrators authenticate with `RolePartnerAdmin` credentials and submit the application via `POST /api/v1/partners`.
   - Required payload fields:
     - `name` and `legal_name`
     - `kyb_object_key` and `licensing_object_key` (S3 keys for uploaded KYB/licensing artifacts)
     - `contacts` array containing name, email, phone, role, and unique OIDC/WebAuthn `subject` identifiers for each partner staff member.
   - The submitting subject **must** be present in the contact roster. Duplicate subjects are rejected.

2. **Roster validation:**
   - Partner contacts are stored in the `partner_contacts` table with a uniqueness constraint on `subject` to prevent cross-partner reuse.
   - Subsequent dossier refreshes must be filed using the same endpoints to keep metadata aligned.

## 2. Document Management

1. **Dossier uploads:**
   - Use `POST /api/v1/partners/{id}/dossier` to update KYB/licensing object keys as additional documentation is received.
   - Only partner contacts associated with the record or allowlisted root admins can upload new dossiers.
   - All updates refresh the `updated_at` timestamp on the partner record for reconciliation.

2. **Storage conventions:**
   - Store KYB packages within the configured `OTC_S3_BUCKET`, under a partition such as `partners/{partner_id}/kyb/{timestamp}`.
   - Licensing artifacts should mirror the same structure for ease of lifecycle management.

## 3. Approval Workflow

1. **Root admin review:**
   - Members of the `RoleRootAdmin` allowlist (`OTC_ROOT_ADMIN_SUBJECTS`) perform final review.
   - Approval decisions are recorded via:
     - `POST /api/v1/partners/{id}/approve`
     - `POST /api/v1/partners/{id}/reject`
   - Each decision appends an immutable record to `partner_approvals` and emits an audit event tagged `partner.approved` or `partner.rejected`.

2. **Decision logging:**
   - Include contextual notes in the request body (`{"notes":"reason"}`) for auditability.
   - Rejections clear `approved_at`/`approved_by` timestamps; approvals stamp both fields with the reviewing subject and time.

## 4. Enabling Production Access

1. **Invoice access gating:**
   - Invoice creation (`POST /api/v1/invoices`) and receipt uploads are blocked until the associated partner record is marked `approved`.
   - Attempting to access these routes while pending results in a `403` with guidance to complete KYB review.

2. **Mint guardrails:**
   - Voucher signing and submission (`/ops/otc/invoices/{id}/sign-and-submit`) verify that the invoice creator is tied to an approved partner before interacting with the HSM or swap RPC.
   - Operations personnel should confirm partner state in the console prior to minting actions.

## 5. Operations Console Checklist

1. **Status board:**
   - The OTC Ops UI (`services/otc-ops-ui`) now includes a partner readiness panel reflecting onboarding stage, dossier currency, and approval status.
   - Use this board during daily stand-ups to track blockers and outstanding tasks per partner.

2. **Alerting:**
   - Configure alerts to flag partners lingering in `pending_review` beyond agreed SLAs.
   - Root admins should be paged when dossiers arrive to avoid approval bottlenecks.

## 6. Access Revocation

1. **Offboarding:**
   - To suspend a partner, issue `POST /api/v1/partners/{id}/reject` with context in the `notes` field.
   - Rejection immediately blocks invoice creation and mint access while retaining the dossier history.

2. **Identity hygiene:**
   - Remove stale contact subjects from the dossier payload to prevent future logins.
   - Coordinate with identity providers to deactivate credentials for removed contacts.

## 7. Audit Expectations

- Ensure all approval/rejection decisions include notes referencing supporting evidence.
- Periodically export partner tables for compliance review using the existing recon tooling.
- Partner-related audit events (action prefix `partner.`) are stored in the shared `events` table for single-pane investigations.

Following this runbook keeps KYB evidence, operational readiness, and production permissions tightly coupled, reducing the risk of minting for unverified partners.
