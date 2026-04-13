# Trade Engine Safety & Liveness

The trade engine coordinates a matched buy/sell pair by creating two hardened
escrows (base and quote legs) and ensuring they settle atomically within the
parameters agreed to in the originating offer. The HARDEN-2 upgrade introduces
three key guarantees:

* **Deterministic pricing** – trades now specify a `slippageBps` tolerance so
  the engine can reject settlements that drift outside the negotiated price.
* **Explicit expiry** – every trade carries an absolute `deadline` that bounds
  when funds can be released.
* **Idle auto-refunds** – fully funded trades automatically unwind after a
  configurable idle window (15 minutes by default) if neither party settles.

## Trade definition

`CreateTrade` persists the negotiated metadata and instantiates the base and
quote escrows. In addition to the existing identifiers and token amounts, the
following fields are now required:

* `deadline` – a UNIX timestamp that must be in the future when the trade is
  created. Both escrows inherit the same expiry and settlement attempts after
  the deadline are rejected.
* `slippageBps` – the maximum tolerated price drift expressed in basis points
  (0–10,000). When omitted the engine defaults to 0 bps (no deviation). Any
  settlement whose realised price would exceed this tolerance reverts.

Only native NHB and wrapped ZNHB tokens are accepted by the trade/escrow layer.
The engine normalises symbols through an internal registry that currently
whitelists `NHB` and `ZNHB` and rejects any other asset codes.

## Funding and `FundedAt`

Both legs must reach the `EscrowFunded` state before an atomic settlement can
occur. Whenever the second leg funds, the engine records the current time in the
trade’s `FundedAt` field and emits `escrow.trade.funded` with the timestamp for
client telemetry. If only one side funds, the trade remains in
`TradePartialFunded` and exposes the partially funded event instead.

## Atomic settlement & partial fills

`SettleAtomic` enforces the deadline, verifies both escrows are funded and then
computes the release amounts using the negotiated price ratio. The engine will
release the maximum amount that:

1. keeps both legs within their escrow balances, and
2. honours the slippage tolerance.

If one side over-funds, any excess is immediately refunded to the original
payer before the remaining balance is released to the counterparty. Partial
settlements therefore succeed whenever the available balances stay within the
accepted slippage window, ensuring liveness without allowing price drift.

## Auto-refunds & expiry handling

The `TradeTryExpire` maintenance hook manages both natural expiry and the new
idle auto-refund flow:

* If a trade is fully funded and remains idle for 900 seconds (`AUTO_REFUND_SECS`)
  after `FundedAt`, the engine refunds both escrows, marks the trade as
  `TradeExpired`, and emits `escrow.trade.expired` so both parties are notified.
* Once the explicit `deadline` elapses, the engine refunds any funded legs (or
  cancels the trade outright if neither funded) using the existing expiry rules.

These behaviours guarantee that funds never remain trapped indefinitely—even
when both sides funded but failed to settle.

## Operational toggles

Trade settlement respects the global `system/pauses` kill switch. Operations can
verify the live flag and stage a governance toggle with the helper scripts:

```bash
go run ./examples/docs/ops/read_pauses
go run ./examples/docs/ops/pause_toggle --module trade --state pause
```

Per-offer controls such as `deadline` and `slippageBps` act as on-chain caps and
surface descriptive `codeInvalidParams` errors when breached (for example
`"escrow engine: price slippage exceeded"`). Clients should present the error
messages directly so counterparties understand which guard triggered.
