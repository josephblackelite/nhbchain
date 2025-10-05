# API Replay Protection

The escrow and OTC gateways accept requests that are authenticated via an HMAC-SHA256 signature. Each request **must** include the following headers:

| Header | Description |
| --- | --- |
| `X-Api-Key` | Identifies the client credential used to sign the request. |
| `X-Timestamp` | Unix timestamp (seconds) at the time of signing. The gateway rejects requests older than ±2 minutes by default (configurable via `ESCROW_GATEWAY_TIMESTAMP_SKEW`). |
| `X-Nonce` | Unique, client-chosen string used once per timestamp. Nonces are tracked per API key for twice the skew window (configurable via `ESCROW_GATEWAY_NONCE_TTL`). Reusing a nonce causes the request to be rejected. |
| `X-Signature` | Hex-encoded HMAC-SHA256 signature computed with the shared secret. |

The canonical payload used for signing is the newline-delimited concatenation of:

1. The string value of `X-Timestamp`.
2. The exact `X-Nonce` header value.
3. The uppercase HTTP method.
4. The canonical request path (path plus query parameters sorted lexicographically).
5. The UTF-8 request body (empty string when there is no body).

```text
payload = join([timestamp, nonce, strings.ToUpper(method), canonicalPath, body], "\n")
signature = hex.EncodeToString(HMAC_SHA256(secret, payload))
```

Signatures are compared using constant-time `hmac.Equal` after decoding from hex, eliminating early-exit timing leaks. A per-key nonce cache prevents captured signatures from being replayed within the TTL window, while still permitting legitimate retries signed with a fresh nonce.

Wallet co-signatures (`X-Sig` / `X-Sig-Addr`) are bound to the same timestamp and nonce. The signed payload is:

```text
payload = strings.Join([]string{
    strings.ToUpper(method),
    canonicalPath,
    body,
    timestamp,
    nonce,
    strings.ToLower(resourceID),
}, "|")
```

Requests missing any header, using stale timestamps, reusing a nonce, or providing malformed signatures now fail with `401 Unauthorized` before hitting business logic.
