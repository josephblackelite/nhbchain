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
* **Rate Limits**: Default 60 write requests/minute per API key, 600 public lookups/minute. Limit headers:
  * `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`.

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

### GET `/identity/resolve?username=<alias>`

Public lookup that resolves an alias to addresses and avatar.

**Example Request**

```http
GET /identity/resolve?username=frankrocks HTTP/1.1
Host: gateway.dev.nhbcoin.net
Accept: application/json
```

**Response**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c...",
  "primary": "nhb1...",
  "addresses": ["nhb1...", "nhb1alt..."],
  "avatarUrl": "https://cdn.nhb/id/frankrocks.png",
  "createdAt": "2024-05-01T12:00:00Z"
}
```

No authentication required, but requests are rate-limited per IP.

### GET `/identity/reverse?address=<bech32>`

Returns alias metadata for a linked address.

**Response**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c..."
}
```

If no alias is found, returns `404` with `IDN-404` payload.

### POST `/identity/avatars/upload`

Uploads avatar media and returns a canonical avatar reference.

**Headers**: include API auth and `Content-Type: multipart/form-data`.

**Multipart Fields**

* `file`: binary image (PNG, JPEG, WebP), max 512 KB.
* `aliasId`: hex aliasId (optional; if supplied, gateway enforces owner signature header `X-Alias-Signature`).

**Response**

```json
{
  "avatarRef": "https://cdn.nhb/avatars/0x5e2c/20240612.png",
  "contentType": "image/png",
  "size": 183421,
  "etag": "\"f0d-1c2\""
}
```

Uploaded avatars undergo content scanning (nudity, violence, malware). Non-compliant uploads return `IDN-422`.

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

### cURL – Resolve Alias

```bash
curl "$GATEWAY/identity/resolve?username=frankrocks" | jq
```

### cURL – Upload Avatar

```bash
curl -X POST "$GATEWAY/identity/avatars/upload" \
  -H "X-API-Key: $API_KEY" \
  -H "X-API-Timestamp: $(date +%s)" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "X-API-Signature: $SIG" \
  -F "file=@avatar.png" \
  -F "aliasId=0x5e2c..."
```

## OpenAPI Specification

A machine-readable schema for these endpoints is provided at [`../openapi/identity.yaml`](../openapi/identity.yaml). Use it with
`redocly lint` or `swagger-cli validate` to ensure compatibility.

## Related Docs

* [Identity Concepts](./identity.md)
* [JSON-RPC Reference](./identity-api.md)
* [Avatar Specification](./avatars.md)
* [Security & Compliance](./identity-security-compliance.md)
