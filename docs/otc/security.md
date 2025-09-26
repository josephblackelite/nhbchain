# OTC Security Model

## Identity and Access Management

- **OIDC SSO** – All API requests must include an access token mapped to internal staff identities. The reference implementation expects an `Authorization` header where the bearer token encodes the subject UUID and role.
- **WebAuthn MFA** – Requests must confirm possession of a registered WebAuthn credential via the `X-WebAuthn-Verified` header. Production deployments should integrate with a WebAuthn server and reject requests without recent assertions.
- **Role-Based Access Control** – Supported roles: teller, supervisor, compliance, superadmin, auditor. Each API endpoint enforces role guards matching operational responsibilities.

## Data Protection

- **Transport Security** – Deploy behind TLS termination. Internal services should communicate via mTLS when crossing trust boundaries.
- **Secrets Management** – Environment variables deliver database credentials and S3 access keys. Rotate regularly and prefer dedicated secret stores in production.
- **Receipt Storage** – Receipts are stored in S3; only the object key is persisted. Buckets should enforce encryption at rest and access policies restricting uploaders to signed URLs.

## Audit and Monitoring

- Every privileged action writes an `events` row including actor ID, timestamp, and contextual details.
- Decision outcomes are stored separately in the `decisions` table for clear compliance review.
- HTTP request logs include request IDs for traceability; integrate with SIEM tooling for alerting.

## Operational Hardening

- Apply database migrations automatically on startup and monitor for failures.
- Configure rate limits and WAF rules in front of the service to mitigate abuse.
- Ensure the `OTC_TZ_DEFAULT` timezone aligns with regulatory reporting requirements.
