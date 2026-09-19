# Merchant Loyalty Console

A Next.js dashboard for loyalty operations. Reads work; the write actions call RPC methods that the node has retired.

**Retired RPC methods.** `loyalty_createBusiness`, `loyalty_setPaymaster`, `loyalty_addMerchant`, `loyalty_removeMerchant`, `loyalty_createProgram`, `loyalty_updateProgram`, `loyalty_pauseProgram` and `loyalty_resumeProgram` return HTTP `410 Gone` (`loyaltyRPCDisabledMessage` in `rpc/loyalty_handlers.go`). The equivalent operations are signed transactions (`TxTypeCreateLoyaltyBusiness` `0x42` through `TxTypeResumeLoyaltyProgram` `0x49`) submitted with `nhb_sendTransaction`; see [`docs/loyalty/loyalty.md`](../../docs/loyalty/loyalty.md).

**Missing RPC methods.** The "Fan rewards" tab calls `loyalty_getCreatorRewardsPool`, `loyalty_creatorRewardsStats` and `loyalty_setCreatorRewardsPool`. No such methods exist in the node (`rpc/http.go`), so those calls return `unknown method`.

What works today: `loyalty_getBusiness`, `loyalty_listPrograms`, `loyalty_paymasterBalance` and `loyalty_programStats` (the four reads the console uses that the node implements; none of them requires a token).

## Getting started

```bash
cd examples
yarn install
yarn workspace @nhb/merchant-loyalty-console dev
```

```bash
export NHB_RPC_URL=<node or gateway JSON-RPC URL>   # default https://api.nhbcoin.net/rpc
export NHB_RPC_TOKEN=<bearer token>                  # attached only to calls in the console's mutating list
```

## Security warning

`app/api/rpc/route.ts` allows only the `loyalty_*` methods the UI uses and attaches `NHB_RPC_TOKEN` only to the ones it classes as mutating. It does not authenticate the visitor: the "Admin / caller wallet" field is free text with no signature or session check. Do not expose this app on a public network.

## What the page does

- Load a business by ID (`loyalty_getBusiness`), then its programs, paymaster balance and fan-rewards config.
- Program stats for a chosen day (`loyalty_programStats`).
- Optional auto-refresh every 15 seconds of the program list, per-program stats and paymaster balance.

RPC semantics and roles are in [`docs/loyalty/loyalty.md`](../../docs/loyalty/loyalty.md).
