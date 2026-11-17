# Lending dApp (mock)

A small Next.js app with two flows, Earn and Borrow, driven entirely by in-browser mock state. It makes no network requests and does not talk to a node.

## What is in it

- `pages/index.tsx`: landing page.
- `pages/earn.tsx`: supply and withdraw against mock balances (`lib/mockData.ts`, `useEarnState`).
- `pages/borrow.tsx`: deposit collateral, borrow, repay against mock balances (`useBorrowState`), plus a "Developer fee recipient" input that the mock state ignores.
- `components/`: cards, layout and forms.

`lib/mockData.ts` computes a health factor from constants in the file (`MAX_UTILIZATION = 0.75`, `LIQUIDATION_THRESHOLD = 0.85`). These are illustrative values, not the chain's lending parameters.

## Method names on the pages are outdated

The pages label buttons with `lend_supplyNHB`, `lend_withdrawNHB`, `lend_getPosition`, `lend_depositZNHB`, `lend_borrowNHBWithFee` and `lend_getHealthFactor`. None of those names is registered in the node's RPC dispatcher (`rpc/http.go`). The node's lending methods are named `lending_*` (for example `lending_getMarket`, `lending_getUserAccount`, `lending_borrowNHBWithFee`), and the write methods among them (`lending_supplyNHB`, `lending_withdrawNHB`, `lending_depositZNHB`, `lending_withdrawZNHB`, `lending_borrowNHB`, `lending_borrowNHBWithFee`, `lending_repayNHB`, `lending_liquidate`) are disabled and return HTTP `410`. Lending writes are signed transactions submitted with `nhb_sendTransaction` (`TxTypeLendingSupplyNHB` `0x13` through `TxTypeLendingRepayNHB` `0x18`, `TxTypeLendingLiquidate` `0x1D`; `core/types/transaction.go`). See [`docs/finance/lending`](../../docs/finance/lending) for the module documentation.

## Getting started

```bash
npm install
npm run dev
```

Then open the URL that `next dev` prints.

This example inherits the root repository license.
