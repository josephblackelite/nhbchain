# Identity JSON-RPC Reference

> Endpoint: `POST /rpc` (same port as the core node RPC) • Namespace: `identity_*`

The identity module exposes JSON-RPC endpoints for registering aliases, updating
avatar references, resolving alias metadata, and managing pay-by-email
claimables. This guide documents the request/response schemas, authentication
requirements, and sample payloads for each method.

## Authentication

* **Bearer token** – Mutating methods require the `Authorization: Bearer <token>`
  header. The token is configured in the node's RPC server (`rpc.authToken`).
  Requests without the header (or with a mismatched token) return HTTP 401.
* **Public reads** – `identity_resolve` and `identity_reverse` do not require
  authentication and may be used by wallets or public gateways to look up alias
  data.
* **Idempotency** – The node enforces idempotent semantics for
  `identity_createClaimable` and `identity_claim`. Clients may retry safely when
  receiving network errors.

## Common Error Shapes

Errors follow the JSON-RPC 2.0 structure `{code, message, data}`. Identity
handlers reuse standard node error codes:

| HTTP Status | `code` | `message` | Typical Cause |
| --- | --- | --- | --- |
| `400` | `-32602` | `invalid_params` | Invalid Bech32 address, alias format, or malformed payload. |
| `401` | `-32000` | `missing Authorization header` (or similar) | Missing/incorrect bearer token. |
| `403` | `-32060` | `forbidden` | Claimable access errors (payer/payee mismatch). |
| `404` | `-32602` | `alias not found` / `not_found` | Alias or claimable does not exist. |
| `409` | `-32061` | `conflict` | Claimable deadline exceeded, already claimed, or invalid preimage. |
| `500` | `-32001` | `internal_error` | Unexpected server error (check `data`). |

---

## Method Reference

Each method uses JSON-RPC 2.0. Examples below omit the surrounding HTTP headers
for brevity.

### `identity_setAlias`

Registers or updates the alias controlled by an address. Re-registering with a
new alias automatically emits rename events and updates timestamps.

**Parameters**

Positional array with two items:

1. `address` (`string`) – Bech32-encoded owner address (`nhb1...`).
2. `alias` (`string`) – Desired alias. The node normalises to lower-case and
   validates length/charset.

**Returns**

```json
{"ok": true}
```

**Example Request**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "identity_setAlias",
  "params": [
    "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp9p6hd",
    "frankrocks"
  ]
}
```

### `identity_setAvatar`

Updates the avatar reference for the alias owned by the address. Accepts HTTPS
URLs or `blob://` references that have been provisioned by the identity gateway.

**Parameters**

1. `address` (`string`) – Owner address.
2. `avatarRef` (`string`) – HTTPS or `blob://` reference.

**Returns**

```json
{
  "ok": true,
  "alias": "frankrocks",
  "aliasId": "0x5e2c...",
  "avatarRef": "https://cdn.nhb/avatars/frank.png",
  "updatedAt": 1718132211
}
```

### `identity_resolve`

Fetches the latest metadata for an alias. Public and cache-friendly.

**Parameters**

* `alias` (`string`) – Alias to resolve. Case-insensitive; the node applies
  canonical normalisation.

**Returns**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c...",
  "primary": "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp9p6hd",
  "addresses": [
    "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp9p6hd"
  ],
  "avatarRef": "https://cdn.nhb/avatars/frank.png",
  "createdAt": 1718131200,
  "updatedAt": 1718132211
}
```

### `identity_reverse`

Reverse lookup for an address. Returns the alias string and deterministic
`aliasId` derived from the alias.

**Parameters**

* `address` (`string`) – Bech32 address to reverse lookup.

**Returns**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c..."
}
```

### `identity_createClaimable`

Escrows funds for a recipient identified by an alias or salted email hash.
Clients **must** send a single JSON object as the first positional parameter.

**Request Object**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `payer` | string | ✓ | Bech32 address funding the claimable. |
| `recipient` | string | ✓ | 32-byte hex salted email hash or alias string. |
| `token` | string | ✓ | `NHB` or `ZNHB`. Case-insensitive. |
| `amount` | string | ✓ | Decimal string amount (in token base units). |
| `deadline` | int | ✓ | Unix timestamp (seconds) when the claim expires. Must be in the future. |

**Returns**

```json
{
  "claimId": "0x92fd...",
  "recipientHint": "0x3a4b...",
  "token": "NHB",
  "amount": "25",
  "expiresAt": 1718822400,
  "createdAt": 1718736000
}
```

### `identity_claim`

Releases a claimable to the verified recipient. Requires the preimage supplied
by the identity gateway or derived from the alias ID.

**Request Object**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `claimId` | string | ✓ | Hex-encoded claimable ID returned by `identity_createClaimable`. |
| `payee` | string | ✓ | Bech32 address receiving the funds. |
| `preimage` | string | ✓ | 32-byte hex string matching the stored `recipientHint`. |

> **Alias recipients:** When the `recipientHint` encodes an alias ID, the
> `payee` must be one of the addresses currently bound to that alias. The node
> rejects claims from unrelated accounts, while the preimage requirement
> continues to protect email-hash claimables.

**Returns**

```json
{
  "ok": true,
  "token": "NHB",
  "amount": "25"
}
```

On replay or duplicate submissions the method still returns `{ "ok": true }`
without transferring additional funds.

---

## CLI Helpers

The `nhb-cli` binary wraps the RPCs above:

| Command | Description |
| --- | --- |
| `nhb-cli id set-alias --addr <bech32> --alias <name>` | Calls `identity_setAlias`. |
| `nhb-cli id set-avatar --addr <bech32> --avatar <ref>` | Calls `identity_setAvatar`. |
| `nhb-cli id resolve --alias <name>` | Calls `identity_resolve`. |
| `nhb-cli id reverse --addr <bech32>` | Calls `identity_reverse`. |
| `nhb-cli id create-claimable ...` | Calls `identity_createClaimable`. |
| `nhb-cli id claim ...` | Calls `identity_claim`. |

Refer to the CLI help output (`nhb-cli id --help`) for full flag listings and
examples.
