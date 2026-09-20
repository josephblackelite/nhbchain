# Trade Engine (dual-leg escrow)

The trade engine (`native/escrow/trade_engine.go`, `native/escrow/trade_types.go`) coordinates a buyer and a seller by creating two escrows, one per leg, and settling them together.

## Current availability

* **Read:** `p2p_getTrade` is live.
* **Write RPCs are disabled.** `p2p_createTrade`, `p2p_settle`, `p2p_dispute` and `p2p_resolve` answer HTTP 410 with code `-32060` (`rpc/p2p_handlers.go`, `p2pRPCDisabledMessage`).
* **No transaction type drives the trade engine.** The escrow transaction handlers in `core/state_transition.go` call only the escrow engine (`applyLockEscrow` calls `Engine.Fund` and does not notify the trade engine), and nothing in the block pipeline calls `CreateTrade`, `SettleAtomic`, `TradeDispute`, `TradeResolve`, `TradeTryExpire` or `OnFundingProgress`. The `Node.P2P*` methods in `core/node.go` that call them are reachable only from the disabled RPC handlers.
* The `nhb-cli p2p` subcommands `create-trade`, `settle`, `dispute` and `resolve` do not contact the node: each prints that the command is retired and exits with status 1 (`p2pRetiredMethods`, `cmd/nhb-cli/p2p_cmd.go`; `reportRetired`, `cmd/nhb-cli/retired_cmd.go`). `nhb-cli p2p get --id <trade id>` calls `p2p_getTrade` and works.
* The chain's listing-based peer-to-peer market uses separate transaction types (`TxTypeMarketCreateListing` `0x35`, `TxTypeMarketFillListing` `0x36`, `TxTypeMarketCancelListing` `0x37`; `native/market`), not this engine.

The remainder of this document describes what the engine code does when it is invoked, for reference.

## Statuses

`TradeStatus` (`native/escrow/trade_types.go`), returned by `p2p_getTrade` as the string shown:

| Value | Constant | RPC string |
|-------|----------|------------|
| 0 | `TradeInit` | `init` |
| 1 | `TradePartialFunded` | `partial_funded` |
| 2 | `TradeFunded` | `funded` |
| 3 | `TradeDisputed` | `disputed` |
| 4 | `TradeSettled` | `settled` |
| 5 | `TradeCancelled` | `cancelled` |
| 6 | `TradeExpired` | `expired` |

## Trade definition

`CreateTrade(offerID, buyer, seller, quoteToken, quoteAmount, baseToken, baseAmount, deadline, slippageBps, nonce)`:

* Tokens must be `NHB` or `ZNHB`; both amounts must be positive; `deadline` must not be before the current time; `slippageBps` is at most 10000 and defaults to 0.
* Trade ID: `keccak256(offerId || buyer || seller || nonce)` where `nonce` is a 32-byte value. Re-creating an identical definition returns the existing trade; a different definition under the same ID fails.
* Two escrows are created, both with `feeBps` 0, no mediator, no realm, and the trade's deadline:
  * quote escrow: payer = buyer, payee = seller, token/amount = quote;
  * base escrow: payer = seller, payee = buyer, token/amount = base.
* Trade status starts at `init`. Both escrow IDs are indexed back to the trade.

Because both escrows are created with `feeBps` 0, no escrow fee is charged on trade legs.

## Funding

`OnFundingProgress` (via `HandleEscrowFunded`) recomputes status from the two escrows: both `EscrowFunded` gives `TradeFunded` and records `fundedAt`; one funded gives `TradePartialFunded`; none gives `TradeInit`. It emits `escrow.trade.partial_funded` or `escrow.trade.funded` on a change. Trades already `settled`, `expired` or `cancelled` are left alone.

## Atomic settlement and partial fills

`SettleAtomic(tradeID)`:

* No-op if already `settled`; fails with `trade: disputed trade requires resolution` if disputed; fails with `trade: settlement deadline elapsed` if `deadline > 0` and `now > deadline`; fails with `trade: both escrows must be funded` unless both legs are `EscrowFunded`.
* Release amounts are computed from the two vault balances (`computeSettlementAmounts`): the full expected amounts when both balances cover them; otherwise the largest pair that fits both balances at the offer's price ratio and passes the slippage check. The check (`ensureSlippage`) requires the executed ratio to equal the offered ratio exactly when `slippageBps` is 0, and to be within `slippageBps` of it otherwise. If no pair qualifies, settlement fails with `trade: settlement outside slippage tolerance`.
* For each leg, any balance above the release amount is returned to the leg's payer, then the leg is released: the base leg to the buyer, the quote leg to the seller.
* On success the trade becomes `settled` and `escrow.trade.settled` is emitted.

## Disputes and resolution

* `TradeDispute(tradeID, caller)`: the caller must be the buyer or seller and the trade must be `funded` or `partial_funded`; the trade becomes `disputed` and `fundedAt` is cleared. It changes only the trade status; the two escrows are not moved to `EscrowDisputed`.
* `TradeResolve(tradeID, outcome)`: the trade must be `disputed`. `outcome` is one of `release_both`, `refund_both`, `release_base_refund_quote`, `release_quote_refund_base` (case-insensitive). Releases go to the escrow's payee; refunds go to its payer through `Engine.Refund`, which requires the escrow to be `funded`, the payer as caller and the deadline not yet reached. The trade ends as `settled` for every outcome, including `refund_both`, and `escrow.trade.resolved` is emitted with an `outcome` attribute.
* At the node layer (`Node.P2PResolve`) the caller must hold the `ROLE_ARBITRATOR` role.

## Expiry and idle auto-refund

`TradeTryExpire(tradeID, now)`:

* If the trade is `funded` and `now >= fundedAt + 900` (constant `autoRefundSecs`), it calls the base-leg refund and then the quote-leg refund, and on success the trade becomes `expired`.
* Otherwise, once `now >= deadline`: if only one leg is `funded` it attempts to refund that leg and, on success, the trade becomes `expired`; if neither leg is funded the trade becomes `cancelled` (and `escrow.trade.expired` is emitted); if both are funded the call fails with `trade: cannot auto-expire fully funded trade`.
* Trades already `settled`, `expired` or `cancelled` are left alone.

Caveat on the refunds. Both leg refunds go through `Engine.Refund`, which requires the caller to be the leg's payer (the seller for the base leg, the buyer for the quote leg) and the escrow engine's own clock to be before the escrow deadline; otherwise it fails with `escrow: refund deadline passed`. Both leg escrows are created with the trade's `deadline`. As a result, at or after the deadline the single-funded-leg branch returns that error instead of refunding, and the 900-second idle branch also fails once the deadline has passed (it can only refund while the deadline has not been reached). Only the "neither leg funded, so `cancelled`" outcome works after the deadline. `Engine.Expire`, which does refund after a deadline, is not called by the trade engine.

As noted above, nothing in production calls `TradeTryExpire`.

## Events

`escrow.trade.created`, `escrow.trade.partial_funded`, `escrow.trade.funded`, `escrow.trade.disputed`, `escrow.trade.resolved`, `escrow.trade.settled`, `escrow.trade.expired`. Attributes (`newTradeEvent`): `tradeId`, `offerId`, `buyer`, `seller` (hex), `baseToken`, `baseAmount`, `quoteToken`, `quoteAmount`, `escrowBaseId`, `escrowQuoteId` (hex), `deadline`, `createdAt`, `fundedAt` (only when set), `slippageBps`, `status` (numeric value from the table above), and `outcome` on `resolved`. `escrow.trade.cancelled` does not exist; the cancelled path emits `escrow.trade.expired`.

## `p2p_getTrade`

Request: `{"jsonrpc":"2.0","id":1,"method":"p2p_getTrade","params":[{"tradeId":"0x<64 hex>"}]}`.

Result (`tradeJSON`): `id`, `offerId`, `buyer`, `seller` (bech32), `quoteToken`, `quoteAmount`, `escrowQuoteId`, `baseToken`, `baseAmount`, `escrowBaseId`, `deadline`, `createdAt`, `fundedAt`, `slippageBps`, `status`. An unknown trade returns HTTP 404 with code `-32022`. Error codes for the `p2p_*` trade methods reuse `-32021` to `-32025` (`rpc/p2p_handlers.go`).

`p2p_info` and `p2p_peers` are unrelated to trades: they return the node's peer-to-peer network view (`rpc/p2p_query_handlers.go`). `p2p_peers` (like `net_peers`) requires RPC authentication; `p2p_info` does not.

## Pause switch

The engine's clock is the block timestamp (`SetNowFunc` in `configureTradeEngine`, `core/state_transition.go`). The trade engine checks the `trade` module pause flag on every operation (`nativecommon.Guard(e.pauses, "trade")`), and the escrow engine it drives checks the `escrow` flag. The helper programs in `examples/docs/ops` (`read_pauses`, `pause_toggle`) read and stage pause flags.
