# SWAP-4 Regulatory Brief

The SWAP-4 release hardens the fiat on-ramp against operational and financial crime risks. This brief summarises the controls for regulators, auditors, and investors.

## Custody Model

* Mints originate from PSP-cleared fiat deposits. Tokens are minted into custodial accounts controlled by the on-ramp until customer withdrawals occur.
* Reversal operations debit the custodial account and credit a treasury-managed refund sink. Funds remain on-chain—no silent burns occur.
* Ledger records are immutable and store provider IDs, mint amounts, oracle source, and status transitions (`minted`, `reconciled`, `reversed`).

## Risk Controls

* **Provider allow list** – Only PSP identifiers approved in `[swap.providers]` can submit vouchers. Requests from unknown providers are rejected and logged via `swap.alert.limit_hit` events.
* **Limit buckets** – Per-transaction, daily, monthly, and velocity thresholds are enforced before minting. Breaches emit auditable events with contextual attributes.
* **Sanctions hook** – When enabled, every voucher recipient is evaluated. Failures block the mint and emit `swap.alert.sanction` events for compliance review.
* **Alerting** – All guardrails emit deterministic events appended to the block event log so external monitors and auditors can replay history.

## KYC and Sanctions Responsibilities

* **PSP** – Responsible for full KYC/AML on the fiat leg, transaction monitoring, and providing provider transaction IDs.
* **On-ramp** – Enforces blockchain-level guardrails, maintains the sanctions hook, and honours regulator-mandated reversals.
* **Third-party sanctions service** – Optional integration; SWAP-4 exposes a hook so operators can plug in proprietary or vendor services without protocol changes.

## Audit Trail

* Use `swap_limits` to retrieve real-time counters and confirm that customers remain under policy thresholds.
* Export voucher history via `swap_voucher_export` for reconciliation. Filter for `status=reversed` to review clawbacks.
* The optional `cmd/swap-audit` utility prints the currently loaded risk configuration for config-change reviews.

## Governance & Change Management

* Limit changes require config updates and should be recorded in change-control logs with effective timestamps.
* Provider onboarding involves both PSP legal review and a config update. Ensure the allow list is version-controlled.
* Sanctions hook upgrades must be regression-tested in a non-production environment before activation (`SanctionsCheckEnabled = true`).

## Investor Assurance

* Limits and alerts demonstrate prudent control over token issuance, reducing regulatory risk.
* Refund sink balances are transparent on-chain, enabling proof-of-liabilities exercises.
* The design minimises manual interventions—most failures are prevented pre-mint and surfaced via events, reducing clawback costs.
