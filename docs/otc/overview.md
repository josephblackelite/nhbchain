# OTC Gateway Overview

The OTC gateway is a standalone Go microservice that orchestrates staff-facing OTC order flows. It uses PostgreSQL for persistence and integrates with S3 for receipt storage and NHB chain RPC endpoints for voucher publication. The service enforces branch-specific risk limits, maintains comprehensive audit logs, and exposes an authenticated REST API designed for teller and compliance operations.

## Capabilities

- Staff onboarding via OIDC SSO and WebAuthn multi-factor authentication.
- Lifecycle management of OTC invoices from creation through minting, rejection, or expiry.
- Configurable branch and regional caps with per-invoice limits enforced during approval.
- Structured audit trail capturing every state transition and privileged action.
- Idempotent API semantics for safe client retries.

## Architecture

The microservice runs as a single Go binary (`services/otc-gateway`) configured exclusively through environment variables:

- `OTC_PORT` – HTTP listen port.
- `OTC_DB_URL` – PostgreSQL DSN.
- `OTC_S3_BUCKET` – bucket for receipt uploads.
- `OTC_CHAIN_ID` – identifier for downstream voucher minting.
- `NHB_RPC_BASE` – base URL for on-chain RPC operations.
- `OTC_TZ_DEFAULT` – IANA timezone for timestamp normalization.

Runtime dependencies are managed via GORM for data access and the Chi router for HTTP serving.
