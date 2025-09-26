# Merchant Loyalty Console

A Next.js dashboard for NHBChain loyalty operations teams. It lets you:

- Create loyalty businesses and register paymasters.
- Manage merchant assignments and rotate pools without leaving the browser.
- Configure loyalty programs that accrue ZNHB at settlement.
- Track daily reward stats to verify that accruals fire after real escrow payments.

## Getting started

```bash
yarn install
yarn workspace @nhb/merchant-loyalty-console dev
```

Set the RPC endpoint and authentication token before running in production:

```bash
export NHB_RPC_URL=https://api.nhbcoin.net/rpc
export NHB_RPC_TOKEN=... # bearer token for privileged RPCs
```

By default the console proxies JSON-RPC calls through `app/api/rpc/route.ts`. The proxy automatically attaches the bearer token for privileged methods like `loyalty_createBusiness` and `loyalty_setPaymaster`.

## Features

- **Business bootstrap:** Create a business, add merchants, and rotate paymasters via JSON-RPC (`loyalty_*`).
- **Program orchestration:** Generate deterministic program IDs, configure accrual rates, and pause/resume programs.
- **Stats monitor:** Pull `loyalty_programStats` and `loyalty_paymasterBalance` to verify ZNHB accrual after settlements.
- **Auto-refresh:** Optional polling keeps paymaster balances and program stats current when payments settle in real time.

Refer to [`docs/loyalty/loyalty.md`](../../docs/loyalty/loyalty.md) for RPC semantics and role requirements.
