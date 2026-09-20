# Identity JSON-RPC Reference

Handlers: `rpc/identity_handlers.go`, `rpc/claimable_handlers.go`. Requests use the node's JSON-RPC envelope: `{"jsonrpc":"2.0","id":1,"method":"...","params":[...]}`.

## Method status

| Method | Status | Authentication |
| --- | --- | --- |
| `identity_resolve` | Live | none |
| `identity_reverse` | Live | none |
| `claimable_get` | Live | none |
| `identity_setAlias`, `identity_setAvatar`, `identity_addAddress`, `identity_removeAddress`, `identity_setPrimary`, `identity_rename`, `identity_createClaimable`, `identity_claim` | Disabled | n/a |
| `claimable_create`, `claimable_claim`, `claimable_cancel` | Disabled | n/a |

Disabled methods answer HTTP 410 with error code `-32060` and the message `identityRPCDisabledMessage` / `claimableRPCDisabledMessage`; they used to change the receiving node's state outside block execution. To claim a username use the `TxTypeRegisterIdentity` transaction (`nhb-cli claim-username`); see [`identity.md`](./identity.md). No `identity_claimableExpire` method exists.

## `identity_resolve`

Params: one string, the alias (any case; trimmed and lower-cased by the node).

```json
{"jsonrpc":"2.0","id":1,"method":"identity_resolve","params":["frankrocks"]}
```

Result (`identityResolveResult`):

```json
{
  "alias": "frankrocks",
  "aliasId": "0x<keccak256 of the alias>",
  "primary": "nhb1...",
  "addresses": ["nhb1..."],
  "avatarRef": "https://...",
  "createdAt": 1718131200,
  "updatedAt": 1718132211
}
```

`avatarRef` is omitted when unset. Errors: HTTP 400 code `-32602` for a missing or malformed parameter or an invalid alias (`invalid alias`, with the rule violated in `data`); HTTP 404 code `-32602` with message `alias not found`.

## `identity_reverse`

Params: one string, a bech32 address.

```json
{"jsonrpc":"2.0","id":1,"method":"identity_reverse","params":["nhb1..."]}
```

Result: `{"alias": "frankrocks", "aliasId": "0x..."}`. Errors: HTTP 400 `-32602` (`invalid address`), HTTP 404 `-32602` with message `address has no alias`.

## `claimable_get`

Params: `{"id": "0x<64 hex>"}`.

Result (`claimableJSON`): `id`, `payer` (bech32), `token`, `amount` (decimal string), `hashLock` (`0x` plus 64 hex), `deadline`, `createdAt`, `expiresAt`, `nonce`, `chainId` (string), `status` (`init`, `claimed`, `cancelled`, `expired`).

Errors use their own code family: `-32041` invalid params (HTTP 400), `-32042` not found (404), `-32043` forbidden (403), `-32044` conflict (409), `-32045` internal (500). An unknown ID returns HTTP 404 with `-32042` and message `not_found`.

## Errors shared by all node methods

| HTTP | Code | Meaning |
| --- | --- | --- |
| 400 | `-32602` | Invalid parameters. |
| 404 | `-32602` | Alias not found (identity methods reuse the invalid-params code). |
| 410 | `-32060` | Method disabled. |
| 500 | `-32000` | Internal error. |

## CLI

`nhb-cli id resolve --alias <name>` and `nhb-cli id reverse --addr <bech32>` call the live methods. The other `nhb-cli id` subcommands are retired: they exit non-zero without contacting the node; see [`identity-cli.md`](./identity-cli.md).
