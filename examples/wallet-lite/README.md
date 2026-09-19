# Wallet Lite

A Next.js app (identity, claimables, QR intents, creator panels). Most write flows now hit RPC methods the node has retired; see [`docs/examples/wallet-lite.md`](../../docs/examples/wallet-lite.md) for a panel-by-panel status table with source references.

**Retired RPC methods.** `identity_setAlias`, `identity_setAvatar`, `identity_addAddress`, `identity_removeAddress`, `identity_setPrimary`, `identity_rename`, `identity_createClaimable`, `identity_claim`, `creator_tip`, `creator_stake` and `creator_unstake` return HTTP `410 Gone` from the node (`identityRPCDisabledMessage` in `rpc/identity_handlers.go`, `creatorRPCDisabledMessage` in `rpc/creator_handlers.go`). The register-username, tip, stake and unstake API routes in this app return `410` themselves; the claimable and claim routes still call the node and receive the `410`.

What works: the account snapshot (`nhb_getBalance`), alias lookup and creator profile (`identity_resolve`), the subscription schedule preview (computed locally), and the `znhb://pay` QR generator.

## Getting started

```bash
cd examples
yarn install
cd wallet-lite
yarn dev
```

All of these must be set (the server throws `Wallet Lite configuration invalid` otherwise): `NHB_RPC_URL`, `NHB_RPC_TOKEN`, `NHB_CHAIN_ID`, `IDENTITY_EMAIL_SALT`, `IDENTITY_GATEWAY_URL`, `IDENTITY_GATEWAY_KEY`, `IDENTITY_GATEWAY_SECRET`. Optional: `APP_PUBLIC_BASE` (base for avatar and sample-content links), `NHB_WS_URL` (parsed, not used), `NHB_API_URL` (base for the creator content lookup; defaults to `https://gw.nhbcoin.net`).

## Security notes

- The token, email salt and gateway secret are read only by server-side code.
- The private key is held in React state; the page uses no browser storage. Do not enter real keys into the demo.
- Email addresses are hashed on the server (`0x` + hex `HMAC-SHA256(salt, normalised email)`) before they are used in a claimable request.
