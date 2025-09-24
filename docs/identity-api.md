# Identity JSON-RPC Reference

> Endpoint: `POST /rpc` (same port as core node RPC) • Namespace: `identity_*`

The identity module exposes deterministic JSON-RPC methods for registering aliases, managing linked addresses, configuring
avatars, and handling claimables. All write operations require an owner signature following the NHBChain EIP-191-style scheme
described below.

## Authentication & Signature Scheme

* **Scheme:** EIP-191 (`\x19NHB Signed Message:\n${len}|payload`), hashed with keccak256 prior to secp256k1 signing.
* **Payload format:** `${method}|${paramsHash}|${chainId}|${nonce}|${expiry}`.
  * `paramsHash` = keccak256 of canonical JSON-serialized params (sorted keys, no whitespace).
  * `nonce` sourced from `identity_get(aliasOrId).version + 1` or monotonic wallet counter.
  * `expiry` = unix timestamp (seconds) after which the signature is invalid.
* **Verification:** Nodes recompute the payload and recover the signer. The recovered address must match the alias owner.

Example signing payload for `identity_registerAlias`:

```
method = "identity_registerAlias"
params = {"alias":"frankrocks","ownerBech32":"nhb1...","primaryAddr":"nhb1..."}
paramsHash = keccak256("{\"alias\":\"frankrocks\",\"ownerBech32\":\"nhb1...\"}")
chainId = 187001
nonce = 7
expiry = 1718131261
payload = "identity_registerAlias|0x4fb6...|187001|7|1718131261"
```

Wallets should display the decoded payload before signing. Include `expiry` that matches UX expectations (typically 5 minutes).

### Common Error Codes

| Code | Message | Description | Suggested Remediation |
| --- | --- | --- | --- |
| `IDN-001` | `name_taken` | Alias reserved or already registered. | Prompt user to pick another alias or appeal via governance. |
| `IDN-002` | `invalid_alias` | Alias fails normalization or policy checks. | Sanitize input, enforce length/charset before submission. |
| `IDN-003` | `not_owner` | Caller signature does not match alias owner. | Re-authenticate with owner key or transfer ownership. |
| `IDN-004` | `bad_signature` | Signature fails verification. | Recompute payload, ensure nonce/expiry correct. |
| `IDN-005` | `address_not_linked` | Address not currently linked to alias. | Fetch alias details, link address first. |
| `IDN-006` | `claim_expired` | Claimable expired before claim. | Instruct payer to recreate claimable. |
| `IDN-007` | `alias_not_found` | Alias or ID cannot be resolved. | Prompt user to register alias or check spelling. |
| `IDN-008` | `replay_detected` | Nonce already used or signature expired. | Generate new nonce/expiry and re-sign. |
| `IDN-009` | `gateway_required` | Operation requires off-chain verification (e.g., email binding). | Complete verification via gateway. |

All errors include `{code, message, data}` fields; `data` may contain contextual hints (`{"aliasId":"0x.."}`).

---

## Method Reference

Each method below includes parameters, return schema, and request/response examples. Replace `NODE_RPC_URL` with your node
endpoint. All examples use JSON-RPC 2.0.

### `identity_registerAlias`

Registers a new alias and sets the initial primary address.

**Parameters**

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `alias` | string | ✓ | Desired alias (normalized client-side). |
| `ownerBech32` | string | ✓ | Owner account that signs subsequent mutations. |
| `sig` | string | ✓ | Hex-encoded signature per scheme above. |

**Returns**

```json
{
  "aliasId": "0x5e2c...",
  "alias": "frankrocks",
  "owner": "nhb1...",
  "primaryAddr": "nhb1...",
  "createdAt": 1718131200
}
```

**Example Request**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "identity_registerAlias",
  "params": ["frankrocks", "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpxxx", "0xSIG"]
}
```

**Example Error Response**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32001,
    "message": "identity error",
    "data": {"code": "IDN-001", "message": "name_taken"}
  }
}
```

### `identity_addAddress`

Links a new Bech32 address to an alias.

**Parameters**

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `aliasOrId` | string | ✓ | Alias string or hex aliasId. |
| `addressBech32` | string | ✓ | Address to link. |
| `sig` | string | ✓ | Owner signature. |

**Returns**

```json
{"ok": true, "addresses": ["nhb1...", "nhb1alt..."]}
```

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "identity_addAddress",
  "params": ["frankrocks", "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y", "0xSIG"]
}
```

### `identity_removeAddress`

Removes a linked address. Cannot remove the current primary address; set a new primary first.

**Parameters**: same as `identity_addAddress`.

**Returns**

```json
{"ok": true, "addresses": ["nhb1..."]}
```

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "identity_removeAddress",
  "params": ["frankrocks", "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y", "0xSIG"]
}
```

### `identity_setPrimary`

Sets the primary payout address.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `aliasOrId` | string | ✓ | Alias or aliasId. |
| `addressBech32` | string | ✓ | Address that must already be linked. |
| `sig` | string | ✓ | Owner signature. |

**Returns**

```json
{"ok": true, "primaryAddr": "nhb1..."}
```

If the address is not linked, the method fails with `IDN-005`.

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 13,
  "method": "identity_setPrimary",
  "params": ["frankrocks", "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y", "0xSIG"]
}
```

### `identity_setAvatar`

Updates the avatar reference. Accepts HTTPS URL or on-chain blob reference (`blob://{cid}`).

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `aliasOrId` | string | ✓ | Alias or aliasId. |
| `avatarUrlOrBlobRef` | string | ✓ | Avatar reference per [Avatar spec](./avatars.md). |
| `sig` | string | ✓ | Owner signature. |

**Returns**: `{ "ok": true, "avatarRef": "https://..." }`

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 14,
  "method": "identity_setAvatar",
  "params": ["frankrocks", "https://cdn.nhb/avatars/frankrocks.png", "0xSIG"]
}
```

### `identity_rename`

Renames an alias without changing `aliasId`.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `aliasId` | string | ✓ | Hex aliasId (canonical). |
| `newAlias` | string | ✓ | New alias candidate. |
| `sig` | string | ✓ | Owner signature. |

**Returns**: `{ "ok": true, "alias": "frankr0cks" }`

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 15,
  "method": "identity_rename",
  "params": ["0x5e2c...", "frankr0cks", "0xSIG"]
}
```

### `identity_get`

Fetches the full alias record.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `aliasOrId` | string | ✓ | Alias or aliasId. |

**Returns**

```json
{
  "aliasId": "0x5e2c...",
  "alias": "frankrocks",
  "owner": "nhb1...",
  "primaryAddr": "nhb1...",
  "addresses": ["nhb1...", "nhb1alt..."],
  "avatarRef": "https://cdn.nhb/id/frankrocks.png",
  "createdAt": 1718131200,
  "updatedAt": 1718132200,
  "version": 4
}
```

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 16,
  "method": "identity_get",
  "params": ["frankrocks"]
}
```

### `identity_resolve`

Resolves an alias to linked addresses.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `nameOrAlias` | string | ✓ | Alias string (with or without leading `@`). |

**Returns**

```json
{
  "aliasId": "0x5e2c...",
  "alias": "frankrocks",
  "primary": "nhb1...",
  "addresses": ["nhb1...", "nhb1alt..."],
  "avatarRef": "https://cdn.nhb/id/frankrocks.png",
  "createdAt": 1718131200
}
```

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 21,
  "method": "identity_resolve",
  "params": ["frankrocks"]
}
```

### `identity_reverseResolve`

Reverse-lookup of alias by address.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `addressBech32` | string | ✓ | Address to resolve. |

**Returns**: `{ "alias": "frankrocks", "aliasId": "0x5e2c..." }`

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 22,
  "method": "identity_reverseResolve",
  "params": ["nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y"]
}
```

### `identity_createClaimable`

Creates a claimable escrow for an unresolved alias or email hash.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `recipientAliasOrEmailHash` | string | ✓ | Alias string/aliasId or salted email hash (hex). |
| `token` | string | ✓ | Token denom (e.g., `NHB`, `ZNHB`). |
| `amount` | string | ✓ | Decimal string. |
| `expiry` | integer | ✓ | Unix timestamp expiry. |
| `payerSig` | string | ✓ | Signature from payer address authorizing hold. |

**Returns**

```json
{
  "claimId": "0x92fd...",
  "expiresAt": 1718137200
}
```

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 42,
  "method": "identity_createClaimable",
  "params": ["0x5e2c...", "NHB", "10.00", 1718736000, "0xPAYER_SIG"]
}
```

### `identity_claim`

Claims an existing claimable once alias/email resolves.

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `claimId` | string | ✓ | Claimable identifier. |
| `recipientSig` | string | ✓ | Signature from alias owner proving control. |

**Returns**

```json
{
  "ok": true,
  "settledTx": "0xabc123...",
  "amount": "10.0",
  "token": "NHB",
  "to": "nhb1primary..."
}
```

On success, the claimable is removed and the escrow vault transfers funds to the alias primary address.

**Request Example**

```http
POST NODE_RPC_URL
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 43,
  "method": "identity_claim",
  "params": ["0x92fd...", "0xRECIP_SIG"]
}
```

---

## Batch & Pagination Guidance

* JSON-RPC batch requests are supported; include nonces/expiries per call.
* `identity_get` and `identity_resolve` support future pagination of addresses via optional `offset`, `limit` params (reserved).

## Monitoring & Events

Consumers can subscribe to `identity.*` events via `eth_subscribe` (`logs`) and filter by topic (`identity.alias.registered`,
`identity.claimable.created`). Each event log encodes `aliasId`, addresses, and metadata hashes. Use this for audit trails.

## Related Documents

* [Identity Concepts](./identity.md)
* [Identity Gateway REST API](./identity-gateway.md)
* [CLI Usage](./identity-cli.md)
