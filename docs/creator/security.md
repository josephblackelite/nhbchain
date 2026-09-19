# Creator Module Security Notes

Facts about the creator module that matter for operators (`native/creator`, `rpc/creator_handlers.go`). Availability is described in [`overview.md`](./overview.md): the write RPCs are disabled and no creator transaction type exists.

## Access

* `creator_payouts` (read) requires RPC authentication (JWT bearer token or verified client certificate). It takes the creator address as the `caller` parameter and does not check that the authenticated identity matches it.
* The disabled methods return HTTP 410 with code `-32060` without touching state, regardless of authentication.

## Engine guards

* Content IDs are unique; publishing an existing ID fails (`creator engine: content already exists`).
* Amounts must be positive; balances are checked before debit (`creator engine: insufficient balance`).
* Rate limits kept in the engine and persisted with state: 5 tips per creator per rolling second, and 1,000,000,000,000 base units staked per fan per 3600-second window.
* Content URIs are restricted to the schemes `https`, `ipfs`, `ar`, `nhb` and 512 bytes; metadata to UTF-8 and 4096 bytes. The chain stores only these references.
* The staking yield rate (250 bps of the deposit) is a constant in the engine (`stakingAccrualBps`); it is not a configuration value.

## Known accounting behavior

See the balance note in [`economics.md`](./economics.md): the stake path debits the fan without crediting a vault, and unstake credits the fan without debiting one. Re-enabling the module's write path would need a review of this.
