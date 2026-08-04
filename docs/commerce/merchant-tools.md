# Merchant Tooling Guide

Task 5 introduces a dedicated toolset for merchants operating NHBChain-based escrow and P2P commerce flows.

---

## 1. Reconciliation Reporting

`payments-gateway` exposes `GET /invoices`, `GET /reconciliation/summary`, and `GET /reconciliation/export` for
invoice-to-mint reporting. See [payments-reconciliation.md](../api/payments-reconciliation.md).

---

## 2. Support & Resources

* **Documentation:** `/docs/escrow/escrow.md` (state machine), `/docs/escrow/gateway-api.md` (REST operations).
* **SDKs:** TypeScript & Go SDKs include helpers for signing requests and consuming paginated endpoints.
* **Contact:** Reach the merchant integrations team at `merchants@nehborly.net` for production onboarding or
  escalation support.
