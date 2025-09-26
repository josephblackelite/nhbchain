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
- `OTC_SWAP_RPC_BASE` – base URL for on-chain swap voucher submission (falls back to `NHB_RPC_BASE`).
- `OTC_TZ_DEFAULT` – IANA timezone for timestamp normalization.
- `OTC_VOUCHER_TTL_SECONDS` – lifetime applied to mint vouchers before expiry.
- `OTC_MINT_POLL_INTERVAL_SECONDS` – cadence for polling mint confirmations.
- `OTC_SWAP_PROVIDER` – identifier reported to `swap_submitVoucher`.
- `OTC_HSM_BASE_URL`, `OTC_HSM_CA_CERT`, `OTC_HSM_CLIENT_CERT`, `OTC_HSM_CLIENT_KEY`, `OTC_HSM_KEY_LABEL`, `OTC_HSM_SIGNER_DN` – mTLS parameters for the signing service.

Runtime dependencies are managed via GORM for data access and the Chi router for HTTP serving.
