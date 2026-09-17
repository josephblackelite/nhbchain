# Identity Gateway REST API

> Base URL: `https://gateway.dev.nhbcoin.net` (replace with environment) • Version: v0

The identity gateway manages off-chain verification (email, avatar uploads) and provides public lookup endpoints for wallets. All
mutating endpoints require HMAC-authenticated API keys issued to partner applications.

## Authentication & Headers

* **API Key**: `X-API-Key: <key>` issued per tenant. Use distinct keys for server-side and client-side integrations.
* **HMAC Signature**: `X-API-Signature` header computed as `hex(HMAC_SHA256(secret, method + "\n" + path + "\n" + bodySha256 +
  "\n" + timestamp))`.
* **Timestamp**: `X-API-Timestamp` (unix seconds). Requests older than 300s are rejected (`IDN-401`).
* **Idempotency**: `Idempotency-Key` header (UUID v4). Repeating the same key returns the initial response.
* **Rate Limits**: Default 60 write requests/minute per API key, 600 public lookups/minute.

### Error Format

```json
{
  "error": {
    "code": "IDN-4xx",
    "message": "description",
    "details": {}
  }
}
```

`IDN-400` (bad request), `IDN-401` (auth), `IDN-404` (not found), `IDN-409` (conflict/idempotent replay), `IDN-429` (rate limit).

## Deployment & Configuration

The production service lives under [`services/identity-gateway`](../../services/identity-gateway). It is a
small Go HTTP binary backed by BoltDB for verification state, idempotency caches, and alias bindings. The
process reads configuration exclusively from environment variables:

| Variable | Default | Description |
| --- | --- | --- |
| `IDENTITY_GATEWAY_LISTEN` | `:8095` | Address to bind the HTTP listener. |
| `IDENTITY_GATEWAY_PORT` | _empty_ | Optional override for the listener port when running behind Compose/Helm. |
| `IDENTITY_GATEWAY_DB` | `identity-gateway.db` | Path to the BoltDB file storing verification sessions and bindings. |
| `IDENTITY_GATEWAY_API_KEYS` | _required_ | Comma-delimited list of `key:secret` pairs used for HMAC auth. |
| `IDENTITY_EMAIL_SALT` | _required_ | Salt used for HMAC(email) derivation. Rotate per environment. |
| `IDENTITY_GATEWAY_CODE_TTL` | `10m` | Validity window for verification codes. |
| `IDENTITY_GATEWAY_REGISTER_WINDOW` | `1h` | Sliding window used for the 5-attempts-per-email rate limit. |
| `IDENTITY_GATEWAY_REGISTER_ATTEMPTS` | `5` | Max register calls permitted per window for an email hash. |
| `IDENTITY_GATEWAY_TIMESTAMP_SKEW` | `5m` | Allowed difference between request timestamp and server clock. |
| `IDENTITY_GATEWAY_IDEMPOTENCY_TTL` | `24h` | Retention for cached responses keyed by `Idempotency-Key`. |

Telemetry (`OTEL_EXPORTER_*`) and logging (`NHB_ENV`) follow the same conventions as the other services. A
local instance can be launched via:

```bash
IDENTITY_GATEWAY_API_KEYS=demo:demo-secret \
IDENTITY_EMAIL_SALT=demo-salt \
go run ./services/identity-gateway/cmd/identity-gateway
```

The Docker Compose bundle now includes an `identity-gateway` service that exposes port `8095` and stores
state under the `identity-gateway-data` volume. Update the API key secret before exposing the gateway outside
trusted environments.

---

## Endpoints

### POST `/identity/email/register`

Initiates email verification by sending a one-time code.

**Headers**: `X-API-Key`, `X-API-Signature`, `X-API-Timestamp`, `Content-Type: application/json`, `Idempotency-Key` (optional).

**Request Body**

```json
{
  "email": "frank@example.com",
  "aliasHint": "frankrocks"
}
```

**Response**

```json
{
  "status": "pending",
  "expiresIn": 600
}
```

**Notes**

* `aliasHint` is optional; when provided it is included in verification emails.
* Rate limited to 5 attempts/hour per email hash.

### POST `/identity/email/verify`

Marks an email as verified using the code delivered out-of-band.

**Request Body**

```json
{
  "email": "frank@example.com",
  "code": "483921"
}
```

**Response**

```json
{
  "status": "verified",
  "verifiedAt": "2024-06-12T18:20:00Z",
  "emailHash": "0xabcd..."
}
```

On success, the gateway stores the salted hash and marks the email as eligible for alias binding.

### POST `/identity/alias/bind-email`

Binds a verified email to an alias ID for opt-in lookup.

**Request Body**

```json
{
  "aliasId": "0x5e2c...",
  "email": "frank@example.com",
  "consent": true
}
```

**Response**

```json
{
  "status": "linked",
  "aliasId": "0x5e2c...",
  "emailHash": "0xabcd...",
  "publicLookup": true
}
```

If the email was not previously verified, the endpoint returns `IDN-401`.

### Alias resolution and avatars

The gateway does not expose lookup or upload endpoints. Alias resolution (and
reverse lookup by address) is done via the node's `identity_resolve` /
`identity_reverse` JSON-RPC methods (see [JSON-RPC Reference](./identity-api.md)).
Avatars are set via the `identity_setAvatar` JSON-RPC method, where the caller
supplies an HTTPS URL or `blob://` reference string directly — there is no
gateway upload endpoint.

---

## Usage Examples

### HMAC Signature Example (pseudo-code)

```python
import hashlib, hmac, time, json

body = json.dumps({"email": "frank@example.com"})
body_hash = hashlib.sha256(body.encode()).hexdigest()
ts = str(int(time.time()))
message = "POST\n/identity/email/register\n" + body_hash + "\n" + ts
signature = hmac.new(secret.encode(), message.encode(), hashlib.sha256).hexdigest()
```

### cURL – Start Email Verification

```bash
curl -X POST "$GATEWAY/identity/email/register" \
  -H "X-API-Key: $API_KEY" \
  -H "X-API-Timestamp: $(date +%s)" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"email":"frank@example.com","aliasHint":"frankrocks"}'
```

## OpenAPI Specification

A machine-readable schema for these endpoints is provided at [`../openapi/identity.yaml`](../openapi/identity.yaml). Use it with
`redocly lint` or `swagger-cli validate` to ensure compatibility.

## Related Docs

* [Identity Concepts](./identity.md)
* [JSON-RPC Reference](./identity-api.md)
* [Avatar Specification](./avatars.md)
* [Security & Compliance](./identity-security-compliance.md)
