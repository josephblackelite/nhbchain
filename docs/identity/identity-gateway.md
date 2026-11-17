# Identity Gateway REST API

`services/identity-gateway` is a small Go HTTP service (`services/identity-gateway/server.go`, `store.go`, `cmd/identity-gateway/main.go`) that verifies email addresses off-chain and records opt-in bindings between a verified email hash and an alias ID. It has three endpoints, all `POST`. It exposes no alias lookup or avatar upload endpoints; alias data comes from the node's `identity_resolve` / `identity_reverse` methods ([`identity-api.md`](./identity-api.md)).

The machine-readable schema is [`../openapi/identity.yaml`](../openapi/identity.yaml).

## Authentication and headers

Every endpoint requires:

* `X-API-Key`: a configured key.
* `X-API-Timestamp`: Unix seconds; must be within the allowed skew (default 5 minutes, either direction) of the server clock.
* `X-API-Signature`: hex `HMAC-SHA256(secret, method + "\n" + path + "\n" + hex(sha256(body)) + "\n" + timestamp)`, where `path` is the URL path without a query string.
* `Idempotency-Key` (optional): if present, the first response for `(api key, method, path, key)` is stored and replayed (with header `X-Idempotency-Cache: hit`) for the idempotency TTL (default 24 hours). The stored response is returned even when the body differs.

Request bodies are limited to 64 KiB.

Errors are `{"error":{"code":"IDN-xxx","message":"...","details":{}}}` with these codes: `IDN-400` (bad request), `IDN-401` (authentication or proof failure), `IDN-404` (verification session not found), `IDN-409` (alias already linked to another email; also used for an expired verification code), `IDN-429` (too many verification requests, `details.retryAfter` in seconds), `IDN-500` (internal), `IDN-502` (verification message could not be dispatched).

## Configuration (environment variables)

| Variable | Default | Description |
| --- | --- | --- |
| `IDENTITY_GATEWAY_API_KEYS` | required | Comma-separated `key:secret` pairs. |
| `IDENTITY_EMAIL_SALT` | required | Salt used for the email hash and code digest. |
| `IDENTITY_GATEWAY_NODE_URL` | required | Node JSON-RPC URL, used to call `identity_resolve` when binding an alias. |
| `IDENTITY_GATEWAY_LISTEN` | `:8095` | Listen address. |
| `IDENTITY_GATEWAY_PORT` | empty | If set, replaces the port of the listen address. |
| `IDENTITY_GATEWAY_DB` | `identity-gateway.db` | BoltDB file. |
| `IDENTITY_GATEWAY_CODE_TTL` | `10m` | Verification code lifetime. |
| `IDENTITY_GATEWAY_REGISTER_WINDOW` | `1h` | Window for the register attempt limit. |
| `IDENTITY_GATEWAY_REGISTER_ATTEMPTS` | `5` | Maximum register calls per email hash per window. |
| `IDENTITY_GATEWAY_TIMESTAMP_SKEW` | `5m` | Allowed timestamp skew. |
| `IDENTITY_GATEWAY_IDEMPOTENCY_TTL` | `24h` | Idempotency record lifetime. |
| `NHB_ENV`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_INSECURE` | | Logging label and telemetry export. |

The bind-token lifetime is 15 minutes (`defaultBindTokenTTL`). The binary uses a `LogEmailer`: verification codes are **written to the service log, not emailed** (`emailer.go`); a real mail sender has to replace it.

Local run: `IDENTITY_GATEWAY_API_KEYS=demo:demo-secret IDENTITY_EMAIL_SALT=demo-salt IDENTITY_GATEWAY_NODE_URL=http://localhost:8080 go run ./services/identity-gateway/cmd/identity-gateway`. A service definition for it exists in `deploy/compose/docker-compose.yml`.

## Email normalization and hashing

`emailHash = "0x" + hex(HMAC-SHA256(salt, NFKC(lowercase(trimmed email))))`. The address must parse with `net/mail`. The verification code is a 6-digit random number; only a salted digest of it is stored.

## `POST /identity/email/register`

Body: `{"email": "...", "aliasHint": "..."}` (`aliasHint` optional; it is passed to the emailer). Starts verification: stores the code digest and expiry and dispatches the code. At most `IDENTITY_GATEWAY_REGISTER_ATTEMPTS` calls per email hash per window, otherwise `IDN-429`. Response `200`: `{"status":"pending","expiresIn":<seconds>}`.

## `POST /identity/email/verify`

Body: `{"email": "...", "code": "..."}`. Checks the code. Response `200`:

```json
{"status":"verified","verifiedAt":"2026-01-01T00:00:00Z","emailHash":"0x...","bindToken":"<64 hex>"}
```

`bindToken` is a random 256-bit single-use token valid for 15 minutes; only its digest is stored. Errors: `IDN-404` if no verification was started, `IDN-409` code expired, `IDN-400` wrong code.

## `POST /identity/alias/bind-email`

Binds a verified email hash to an alias for opt-in lookup. Body:

```json
{"aliasId":"0x...","alias":"frankrocks","email":"frank@example.com","consent":true,"bindToken":"<from verify>","aliasSignature":"0x<65-byte signature>"}
```

Both proofs are required:

1. `bindToken` from `/identity/email/verify` (proves control of the inbox); missing or invalid tokens return `IDN-401`.
2. `aliasSignature`: a secp256k1 signature (hex, 65 bytes) over `keccak256` of the JSON `{"aliasId":"<aliasId>","emailHash":"<emailHash>","bindToken":"<bindToken>"}` (fields in that order, no EIP-191 prefix). The recovered address must equal the alias's `primary` address returned by the node's `identity_resolve` (`IDN-401` otherwise, including when the alias is unknown).

`aliasId` must equal `0x` plus hex of `keccak256(normalized alias)` (`IDN-400` otherwise). The token is consumed by a successful bind. Response `200`:

```json
{"status":"linked","aliasId":"0x...","emailHash":"0x...","publicLookup":true}
```

`publicLookup` echoes `consent`. An alias already bound to a different email returns `IDN-409`.

## Storage

BoltDB buckets `emails` (hash, code digest and expiry, register attempts, verification time, bind token digest and expiry, alias bindings), `aliases` (alias ID to email hash) and `idempotency`. The service does not expose bound data through any endpoint and has no `/privacy/export` or other privacy endpoints.
