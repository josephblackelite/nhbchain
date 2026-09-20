# Creator Module Security Notes

Facts about the creator module that matter for operators (`native/creator`, `rpc/creator_handlers.go`). Availability is described in [`overview.md`](./overview.md): the write RPCs are disabled and no creator transaction type exists.

## Access

* `creator_payouts` (read) requires RPC authentication (JWT bearer token or verified client certificate). It takes the creator address as the `caller` parameter and does not check that the authenticated identity matches it.
* The disabled methods return HTTP 410 with code `-32060` without touching state, regardless of authentication.

## Engine guards

Enforced by the engine when it runs (`native/creator/engine.go`), which today happens only in tests and Go callers because the write RPCs are disabled:

* Tips and stakes debit the caller's NHB balance and fail with `creator engine: insufficient balance` when it is too low.
* Tips are limited to 5 per creator per rolling 1-second window; a fan may stake at most 1,000,000,000,000 base units per 3600-second window ([`economics.md`](./economics.md)).
* The staking yield rate is a constant in the code (`stakingAccrualBps` = 250, 2.5%); no parameter changes it.

## Known accounting behavior

See the balance note in [`economics.md`](./economics.md): the stake path debits the fan without crediting a vault, and unstake credits the fan without debiting one. Re-enabling the module's write path would need a review of this.
