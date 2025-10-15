# OTC Gateway authentication baseline

This note captures the current state of authentication and JWT configuration plumbing before the planned migration to real JWT + WebAuthn validation.

## OTC gateway service (`services/otc-gateway/auth/auth.go`)

* Middleware `Authenticate` expects an `Authorization: Bearer <subject>|<role>` header, where the pseudo-token is split on the first `|` to extract the user subject and role.
* Accepted roles are enumerated in `allowedRoles` and include teller, supervisor, compliance, superadmin, auditor, partner, partneradmin, and rootadmin.
* Root admin access is gated by an in-memory allowlist populated via `SetRootAdmins`; the allowlist entries come from the `OTC_ROOT_ADMIN_SUBJECTS` environment variable handled in `services/otc-gateway/config/config.go`.
* WebAuthn is currently simulated by requiring an `X-WebAuthn-Verified: true` header. No cryptographic verification exists yet.
* Successful authentication populates request context keys `user_id` and `user_role`, consumed later by `RequireRole`.

## OTC gateway configuration (`services/otc-gateway/config/config.go`)

* Environment loader collects swap credentials, identity service settings, and HSM material; there is no JWT/WebAuthn configuration yet.
* Root admin subjects are sourced from `OTC_ROOT_ADMIN_SUBJECTS` (comma-separated) and passed to the auth package via `SetRootAdmins` elsewhere in the startup wiring.

## RPC server JWT plumbing (`rpc/http.go` and `config/types.go`)

* `rpc.JWTConfig` / `config.RPCJWT` already support enabling JWT enforcement with HS256 or RS256, specifying issuer, audiences, skew, and secrets (via env) or RSA public keys (via file path).
* `rpc.NewServer` currently tolerates `JWT.Enable == false` and instead falls back to a legacy static token read from `NHB_RPC_TOKEN` unless mutual TLS is used.
* When JWT is enabled, `newJWTVerifier` builds a verifier with issuer/audience enforcement, default 30s leeway, and supports HS256 (env secret) or RS256 (PEM public key) via `parseRSAPublicKey`.
* `TestRequireAuthWithJWT` in `tests/rpc/security_test.go` validates issuer/audience/expiry enforcement for HS256 tokens. MTLS bypass is exercised by `TestRequireAuthWithMTLS`.

## Gaps for upcoming work

* OTC gateway lacks real JWT parsing, signature verification, claim validation, and WebAuthn attestation integration.
* Configuration has no concept of JWT/WebAuthn secrets or key sources for the gateway.
* RPC server still exposes the static token fallback when JWTs are disabled, contrary to the desired behaviour of failing fast unless MTLS is configured.
* Test coverage focuses on happy-path JWT verification at the RPC layer but does not cover expired/invalid JWTs for OTC gateway interactions.
