# P2P Mini-Market

A Next.js app built around the two-escrow ("dual-lock") trade engine in `native/escrow/trade_engine.go`. The trade states, dispute outcomes and what the demo can and cannot do today are documented in [`docs/examples/p2p-mini-market.md`](../../docs/examples/p2p-mini-market.md).

**Retired RPC methods: the core trade flow does not work.** `p2p_createTrade`, `p2p_settle`, `p2p_dispute`, `p2p_resolve` and `escrow_fund` return HTTP `410 Gone` from the node (`p2pRPCDisabledMessage` in `rpc/p2p_handlers.go`, `escrowRPCDisabledMessage` in `rpc/escrow_handlers.go`), and the demo's matching API routes return `410` without calling the chain. Only the read routes work: `nhb_getBalance`, `p2p_getTrade` and `escrow_get`. The chain's live peer-to-peer market is a different model (`TxTypeMarketCreateListing` `0x35`, `TxTypeMarketFillListing` `0x36`, `TxTypeMarketCancelListing` `0x37` via `nhb_sendTransaction`).

## Getting started

```bash
cd examples
yarn install
cd p2p-mini-market
yarn dev
```

Required environment (`app/lib/config.ts`): `NHB_RPC_URL`, `NHB_RPC_TOKEN`, `NHB_CHAIN_ID`. Optional: `NHB_WS_URL` (parsed, but the app opens no WebSocket) and `APP_PUBLIC_BASE`. `yarn dev` in `examples/` also starts this app.

## Behaviour of the page

- Seller and buyer private keys are entered or generated ("Generate") in the page.
- The offer form has a direction (`SELL_NHB` or `SELL_ZNHB`), the two amounts and a funding deadline in hours (default 6).
- Offers and trades are stored in `localStorage`; every trade is re-polled every 8 seconds.
- Each pay intent is rendered as a QR code of a `znhb://pay?...` URI, a convention of this example.
- The token in `NHB_RPC_TOKEN` is used only by server routes.
