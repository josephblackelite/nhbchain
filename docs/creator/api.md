# Creator Module API

Handlers: `rpc/creator_handlers.go`. Requests use the node's JSON-RPC envelope with `params` as an array holding one object.

## Method status

| Method | Status | Params |
| --- | --- | --- |
| `creator_publish` | Disabled | `caller`, `contentId`, `uri`, `metadata` |
| `creator_tip` | Disabled | `caller`, `contentId`, `amount` |
| `creator_stake` | Disabled | `caller`, `creator`, `amount` |
| `creator_unstake` | Disabled | `caller`, `creator`, `amount` (shares) |
| `creator_payouts` with `claim: true` | Disabled | `caller`, `claim` |
| `creator_payouts` (read) | Live, requires authentication | `caller` |

Disabled methods answer HTTP 410 with error code `-32060` and the message defined by `creatorRPCDisabledMessage`. They used to change the node's own state outside block execution, and no signed-transaction replacement exists for the creator module: there is no creator transaction type in `core/types/transaction.go`.

## `creator_payouts` (read)

Authentication: JWT bearer token (or verified client certificate), like other authenticated RPC methods; otherwise HTTP 401 with code `-32001`. Any authenticated caller can read any creator's ledger by passing that address as `caller`.

```json
{"jsonrpc":"2.0","id":1,"method":"creator_payouts","params":[{"caller":"nhb1..."}]}
```

Result (`creatorPayoutsResult`), amounts as decimal strings in base units:

```json
{
  "creator": "nhb1...",
  "pending": "0",
  "totalTips": "0",
  "totalYield": "0",
  "lastPayout": 0,
  "claimed": "0"
}
```

`claimed` is always `"0"` on this path. A creator with no ledger returns zeros.

Errors: HTTP 400 with `-32602` for a wrong parameter count (`exactly one parameter object expected`), an invalid parameter object, an invalid `caller` address (`invalid caller address`) or a failure loading the ledger (`failed to load payouts`).

## Engine errors

The engine returns these strings (see [`overview.md`](./overview.md)): `creator engine: content already exists`, `content not found`, `amount must be positive`, `deposit below minimum`, `deposit too small for share precision`, `insufficient balance`, `stake not found`, `payout vault not configured`, `rewards treasury not configured`, `payout vault underfunded`, `share supply depleted`, `insufficient shares`, `redeem value below precision`, `per-epoch stake cap exceeded`, `tip rate limit exceeded`, `invalid content uri`, `invalid content metadata`.

## Events

See [`overview.md`](./overview.md).
