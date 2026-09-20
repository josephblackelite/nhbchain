# P2P Mini-Market

`examples/p2p-mini-market` is a Next.js 14 app (workspace member `@nhb/p2p-mini-market`, started by `yarn dev`) built around the two-escrow ("dual-lock") trade engine in `native/escrow/trade_engine.go`. This page describes that engine's states and outcomes, and which parts of the demo work against the current node.

## Current status

The RPC methods that create and move a dual-lock trade are retired. The node answers `p2p_createTrade`, `p2p_settle`, `p2p_dispute`, `p2p_resolve` (`rpc/p2p_handlers.go`, `p2pRPCDisabledMessage`) and `escrow_fund` (`rpc/escrow_handlers.go`) with HTTP `410`. The demo's own API routes for those actions (`app/api/p2p/create-trade`, `settle`, `dispute`, `resolve`, and `app/api/escrow/fund`) do not call the chain; they return `410` with `retired: true` after validating the request body. What still works:

| Route | Calls |
| --- | --- |
| `GET /api/account?address=` | `nhb_getBalance` |
| `GET /api/p2p/trade?tradeId=` | `p2p_getTrade({tradeId})` |
| `GET /api/escrow/get?escrowId=` | `escrow_get({id})` |
| `GET /api/config` | Client config (`chainId`, `wsUrl`) |

So the UI can load balances and display trades or escrows that already exist, but it cannot create or fund a trade. The chain's live peer-to-peer market is a different model: signed `TxTypeMarketCreateListing` (`0x35`), `TxTypeMarketFillListing` (`0x36`) and `TxTypeMarketCancelListing` (`0x37`) transactions submitted with `nhb_sendTransaction`, read through `market_listOpenListings`, `market_getListing`, `market_getMyListings`, `market_getMyFills` and `market_getFlatFee`.

## Trade model

`CreateTrade` creates two escrows and one trade record:

- The **quote** escrow: payer is the buyer, payee is the seller, holds the quote token.
- The **base** escrow: payer is the seller, payee is the buyer, holds the base token.

The demo's "SELL_NHB" direction sets base = NHB and quote = ZNHB; "SELL_ZNHB" is the reverse (`mini-market-app.tsx`). The funding deadline in the UI defaults to 6 hours. The trade ID is a Keccak-256 hash of the offer ID, buyer, seller and a nonce, so re-creating the same definition is idempotent, and reusing an ID with a different definition is an error (`trade: identifier already exists with different definition`). Amounts must be positive (`trade: quote amount must be positive`, `trade: base amount must be positive`); a deadline earlier than the current time is rejected (`trade: deadline before creation time`).

### Trade states

When the engine runs inside the block pipeline, every time it reads or stores (`createdAt`, `fundedAt`, deadline and 900-second checks, and the escrow legs' own timestamps) is the block timestamp, not the wall clock (`configureTradeEngine` in `core/state_transition.go`).

`p2p_getTrade` reports `status` as one of these strings (`tradeStatusString` in `rpc/p2p_handlers.go`):

| State | How the engine reaches it |
| --- | --- |
| `init` | Created; neither escrow funded. |
| `partial_funded` | Exactly one escrow is funded (`OnFundingProgress`). |
| `funded` | Both escrows funded; `fundedAt` is set. |
| `disputed` | The buyer or the seller opened a dispute while the trade was `funded` or `partial_funded` (`TradeDispute`; any other caller: `trade: unauthorized dispute caller`). |
| `settled` | Both legs released atomically (`SettleAtomic`, which requires both escrows funded and rejects a disputed trade or one past its deadline), or a dispute was resolved successfully (`TradeResolve`; see the unfunded-leg caveat under Dispute outcomes). |
| `cancelled` | The deadline passed with neither escrow funded (`TradeTryExpire`). |
| `expired` | The deadline passed with one funded leg, which is refunded; or a fully funded trade sat unsettled for 900 seconds (`autoRefundSecs`) and both legs were refunded. |

If the deadline has passed while both legs are funded and fewer than 900 seconds have passed since `fundedAt`, `TradeTryExpire` returns `trade: cannot auto-expire fully funded trade` instead of expiring the trade.

### Escrow leg states

`init`, `funded`, `released`, `refunded`, `expired`, `disputed` (`escrowStatusString` in `rpc/escrow_handlers.go`).

### Dispute outcomes

`TradeResolve` accepts exactly these outcomes (case-insensitive) on a `disputed` trade. When it succeeds the trade ends `settled` (resolving an already `settled` trade is a no-op that returns success). Any other value fails with `trade: invalid resolution outcome`.

| Outcome | Base escrow (seller's asset) | Quote escrow (buyer's asset) | Net effect |
| --- | --- | --- | --- |
| `release_both` | Released to the buyer | Released to the seller | The swap completes. |
| `refund_both` | Refunded to the seller | Refunded to the buyer | Everyone gets their own asset back. |
| `release_base_refund_quote` | Released to the buyer | Refunded to the buyer | The buyer ends with both assets. |
| `release_quote_refund_base` | Refunded to the seller | Released to the seller | The seller ends with both assets. |

(`releaseBaseLeg` releases to `trade.Buyer`, `releaseQuoteLeg` to `trade.Seller`; refunds go to each escrow's payer.)

Edge case: a trade can be disputed from `partial_funded`, where one escrow is still unfunded. Each of `releaseBaseLeg`, `releaseQuoteLeg`, `refundBaseLeg` and `refundQuoteLeg` (`native/escrow/trade_engine.go`) returns an error (`trade: base leg not releasable`, `trade: quote leg not releasable`, `trade: base leg not refundable`, `trade: quote leg not refundable`) when its escrow is neither funded nor disputed, so any outcome that touches the unfunded leg fails and `TradeResolve` returns that error before it sets the trade to `settled`. A leg that is already released (for the release legs) or already refunded or expired (for the refund legs) is skipped without error.

Who is allowed to call resolve is decided by the node method behind `p2p_resolve`, which is currently disabled, so this page does not state it.

## Running the demo

```bash
cd examples
yarn install
cd p2p-mini-market
yarn dev
```

Required environment (`app/lib/config.ts`): `NHB_RPC_URL`, `NHB_RPC_TOKEN`, `NHB_CHAIN_ID`. Optional: `NHB_WS_URL` (parsed and exposed at `/api/config`, but the app opens no WebSocket) and `APP_PUBLIC_BASE`. The token is used only in server routes and is sent only on the `escrow_get` and `p2p_getTrade` calls.

In the browser the app keeps its offers and trades in `localStorage` and re-polls every trade every 8 seconds. Seller and buyer private keys are entered or generated in the page.

## Pay intents

The UI renders each pay intent as a QR code of `znhb://pay?to=<to>&token=<token>&amount=<amount>&memo=<memo>` (`app/components/pay-intent-card.tsx`). That URI is a convention of this example; the chain code does not parse it. The intents came from the `p2p_createTrade` result (`payIntents.seller` and `payIntents.buyer`, each `{to, token, amount, memo}` in `p2pCreateResult`), which is retired.
