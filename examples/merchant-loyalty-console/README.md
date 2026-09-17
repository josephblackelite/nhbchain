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

Set the RPC endpoint and authentication token:

```bash
export NHB_RPC_URL=https://api.nhbcoin.net/rpc
export NHB_RPC_TOKEN=... # bearer token for privileged RPCs
```

**Security warning: this is a demo, not a production-ready admin console.** The console proxies JSON-RPC calls through `app/api/rpc/route.ts`, which keeps `NHB_RPC_TOKEN` out of the browser and now enforces a server-side allowlist restricting every call to the exact `loyalty_*` methods this UI uses (nothing else can be invoked through the proxy). It does **not** authenticate the *visitor* -- the "Admin / caller wallet" field is a free-text address with no signature or session check behind it, so anyone who can reach this app's `/api/rpc` endpoint can invoke any mutating method (`loyalty_createBusiness`, `loyalty_setPaymaster`, `loyalty_addMerchant`, program create/update/pause/resume, etc.) as any caller address they type in. Do not expose this app on a public network, and do not treat it as sufficient access control for real merchant operations, without first adding real per-visitor authentication (e.g. a wallet-signature challenge) in front of every mutating call.

## Features

- **Business bootstrap:** Create a business, add merchants, and rotate paymasters via JSON-RPC (`loyalty_*`).
- **Program orchestration:** Generate deterministic program IDs, configure accrual rates, and pause/resume programs.
- **Fan rewards:** Dedicated tab to inspect the creator rewards pool, tweak share splits, and monitor fan payout stats alongside loyalty programs.
- **Stats monitor:** Pull `loyalty_programStats` and `loyalty_paymasterBalance` to verify ZNHB accrual after settlements.
- **Auto-refresh:** Optional polling keeps paymaster balances and program stats current when payments settle in real time.

Refer to [`docs/loyalty/loyalty.md`](../../docs/loyalty/loyalty.md) for RPC semantics and role requirements.
