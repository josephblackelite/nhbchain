# Creator Module Security Notes

Facts about the creator module that matter for operators (`native/creator`, `rpc/creator_handlers.go`). Availability is described in [`overview.md`](./overview.md): the write RPCs are disabled and no creator transaction type exists.

## Access

* `creator_payouts` (read) requires RPC authentication (JWT bearer token or verified client certificate). It takes the creator address as the `caller` parameter and does not check that the authenticated identity matches it.
* The disabled methods return HTTP 410 with code `-32060` without touching state, regardless of authentication.

## Engine guards

* **Sufficient funds** – Tipping and staking debit the caller immediately. Failed balance checks surface as validation errors; clients should present clear messaging to avoid repeated retries.
* **Rate limits** – The RPC server enforces a per-source transaction quota over a sliding `RPCRateLimitWindow` (code default 5 per 60s when `RPCMaxTxPerWindow` is unset; the shipped `config.toml` sets 120). Consider lowering this in public devnets to reduce griefing and wash trading.
* **Reward configuration** – Staking yield is controlled in-code (2.5% BPS in this build). Networks can fork to adjust or gate staking entirely if needed.

## Known accounting behavior

See the balance note in [`economics.md`](./economics.md): the stake path debits the fan without crediting a vault, and unstake credits the fan without debiting one. Re-enabling the module's write path would need a review of this.
