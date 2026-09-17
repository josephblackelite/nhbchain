# P2P Mini-Market

P2P Mini-Market is a dual-lock escrow demo that shows how NHB peer-to-peer trades
progress from an off-chain offer to an atomic settlement. Operators can stage
buy/sell quotes for NHB ⇄ ZNHB, accept an offer as the counterparty, fund both
legs, and settle or dispute the trade.

The UI is intentionally transparent: both wallets live side-by-side so it is
easy to drive end-to-end QA scenarios.

**Retired RPC methods: this demo's core trade flow is currently non-functional.** `p2p_createTrade`,
`p2p_settle`, `p2p_dispute`, `p2p_resolve`, and `escrow_fund` all used to mutate validator-local state
directly outside the block pipeline, which guarantees a consensus fork/halt on this chain's
2-validator zero-quorum-slack topology -- the node's RPC layer now returns `410 Gone` for every one of
them unconditionally (see `rpc/p2p_handlers.go`'s `p2pRPCDisabledMessage` and `rpc/escrow_handlers.go`'s
`escrowRPCDisabledMessage`). There is no signed-transaction replacement for this specific dual-lock
escrow flow yet. The chain's live P2P market feature (order-book style listing/fill/cancel via
`TX_TYPE_MARKET_CREATE_LISTING`/`FILL`/`CANCEL` over `nhb_sendTransaction`) is unaffected and still
works, but it is a different trade model from the one this demo's UI walks through below.

## Getting started

```bash
# Install workspace dependencies from the examples root
cd examples
yarn install

# Launch the mini-market dev server
cd p2p-mini-market
yarn dev
```

The server reads RPC configuration from the shared `.env` file. Ensure the
following variables are set:

* `NHB_RPC_URL`
* `NHB_RPC_TOKEN`
* `NHB_CHAIN_ID`
* `NHB_WS_URL` (optional for live updates)

## Flows

1. Load seller and buyer private keys. The demo ships with a “Generate” button to
   create throwaway keys locally.
2. Publish an offer describing which asset you are selling (`SELL_NHB` or
   `SELL_ZNHB`), the amounts for each leg, and a funding deadline.
3. Accept the offer as the counterparty. The server calls
   `p2p_createTrade` and returns dual pay intents that encode the escrow vaults
   and memos.
4. Fund each escrow using the QR codes or by sending from another wallet, then
   click “Mark funded” once the deposit lands. The UI calls `escrow_fund` for the
   appropriate leg.
5. When both legs are funded, call “Settle trade” to execute `p2p_settle` and
   release both escrows atomically.
6. Use the “Dispute” and “Resolve dispute” controls to exercise each outcome:
   `release_both`, `refund_both`, and the mixed release/refund variants.

## Notes

* Credentials (`NHB_RPC_TOKEN`) stay on the Next.js server runtime. They are not
  exposed to the browser.
* The local state (offers and trades) is persisted to `localStorage` so a page
  refresh keeps context.
* WebSocket updates are optional; the app polls every eight seconds to refresh
  trade and escrow status.
