# Wallet Lite

`examples/wallet-lite` is a Next.js 14 app (workspace member `@nhb/wallet-lite`). A single page holds the panels below; each panel calls a Next.js API route under `app/api`, and the routes call the node or the identity gateway. The README in that directory has the start-up commands.

## Current status

Several panels call RPC methods the node has retired. The node answers those with HTTP `410` (`identityRPCDisabledMessage` in `rpc/identity_handlers.go`, `creatorRPCDisabledMessage` in `rpc/creator_handlers.go`). See the [examples index](README.md) for the full list.

| Panel | API route | Calls | Works today |
| --- | --- | --- | --- |
| Local session | none | Client-side key handling | Yes |
| Account snapshot | `GET /api/account` | `nhb_getBalance` | Yes |
| Alias lookup | `GET /api/identity/resolve` | `identity_resolve` | Yes |
| Creator profile | `GET /api/identity/profile` | `identity_resolve`, then `GET <NHB_API_URL>/creator/v1/content?alias=` | Alias part yes; content list falls back to a placeholder item if the request fails or returns nothing |
| Register username | `POST /api/identity/set-alias` | none (returns `410` itself) | No, retired |
| Send via claimable | `POST /api/payments/claimables` | `identity_createClaimable` | No, node returns `410` |
| Claim funds | `POST /api/identity/claim` | `identity_claim` | No, node returns `410` |
| Tip, stake, unstake | `POST /api/creator/tips`, `/stake`, `/unstake` | none (each returns `410` itself) | No, retired |
| Subscription preview | `POST /api/creator/subscriptions` | none (computed in the route) | Yes; it is a local schedule preview only |
| Payment QR | none | Client-side | Yes |
| Escrow details / dispute | `GET /api/escrow/[id]`, `POST /api/escrow/[id]/mark-scam` | see below | See below |

## What each working piece does

- **Local session.** You paste or generate a 32-byte secp256k1 private key. The address is derived in the browser (`app/lib/wallet.ts`): keccak-256 of the uncompressed public key, last 20 bytes, Bech32 with prefix `nhb`. The key lives in React state only; the page uses no `localStorage` or `sessionStorage`.
- **Account snapshot.** `nhb_getBalance` for the derived address (no bearer token). The fields are described in the [cookbook](cookbook.md#2-account-snapshot-nhb_getbalance).
- **Alias lookup.** `identity_resolve(alias)` normalises the alias (`identity.NormalizeAlias`) and returns `alias`, `aliasId`, `primary`, `addresses`, `avatarRef`, `createdAt`, `updatedAt` (`identityRecordToResult`, `rpc/identity_handlers.go`). An unknown alias is HTTP 404 `alias not found`. `identity_reverse(address)` also exists and returns `{alias, aliasId}` or 404 `address has no alias`.
- **Payment QR.** Builds `znhb://pay?to=@<alias>&token=<NHB|ZNHB>&amount=<text>` and renders it with `qrcode.react`. That URI is a convention of this example; nothing in the chain code parses it.
- **Subscription preview.** Returns up to six due dates, 7 days apart for `weekly` and 30 days apart otherwise, starting at `startDate`. It moves no funds.
- **Escrow details.** `GET /api/escrow/[id]` fetches `<NHB_RPC_URL>/wallet/escrows/<id>` with an `X-Chain-Id` header. That path is a gateway route (`gateway/routes/wallet.go`, `r.Get("/wallet/escrows/{escrowID}", ...)`, which calls `escrow_get` on the node); the node's own HTTP server only serves JSON-RPC on `/`. The gateway mounts that route only under its `consensus` route, whose prefix is `/v1/consensus` (`cmd/gateway/main.go`, `gateway/routes/router.go`), so the real gateway path is `<gateway>/v1/consensus/wallet/escrows/<id>`. The route builds its URL by appending `wallet/escrows/<id>` to `NHB_RPC_URL`, so `NHB_RPC_URL` would have to end in `/v1/consensus`; with `NHB_RPC_URL` set to the gateway root the request has no matching route. `NHB_RPC_URL` is also the endpoint for this app's JSON-RPC calls (`nhb_getBalance`, `identity_resolve`); whether one URL can serve both was not verified.
- **Mark as scam.** `POST /api/escrow/[id]/mark-scam` imports `EscrowDisputeClient` from a relative path that resolves to `examples/clients/ts/escrow/dispute`. That file does not exist (the client is at `clients/ts/escrow/dispute.ts` in the repository root), so the route does not build as written.

## Environment

The server reads these variables (`app/lib/config.ts`). All of `NHB_RPC_URL`, `NHB_RPC_TOKEN`, `NHB_CHAIN_ID`, `IDENTITY_EMAIL_SALT`, `IDENTITY_GATEWAY_URL`, `IDENTITY_GATEWAY_KEY`, `IDENTITY_GATEWAY_SECRET` are required; the config check throws `Wallet Lite configuration invalid` if any is missing or empty.

| Variable | Use |
| --- | --- |
| `NHB_RPC_URL`, `NHB_RPC_TOKEN` | Node endpoint and bearer token (attached only to calls made with `withAuth`, server side) |
| `NHB_CHAIN_ID` | Sent as the `x-nhb-chain-id` header; the node does not read it |
| `IDENTITY_EMAIL_SALT` | Key for the email hash: `0x` + hex `HMAC-SHA256(salt, lowercased NFKC-normalised email)` (`app/lib/email.ts`) |
| `IDENTITY_GATEWAY_URL`, `IDENTITY_GATEWAY_KEY`, `IDENTITY_GATEWAY_SECRET` | Identity gateway endpoint and credentials |
| `APP_PUBLIC_BASE` | Optional. Base for avatar and sample-content links in the profile route |
| `NHB_WS_URL` | Optional. Parsed, never used to open a socket |
| `NHB_API_URL` | Optional. Base for the creator content lookup; defaults to `https://gw.nhbcoin.net` in `app/api/identity/profile/route.ts` |

`readClientConfig` exposes only `appBaseUrl` and `chainId` to client code; the token, salt and gateway secret are read only by server-side code.

## Identity gateway calls

The email routes (`/api/identity/email/register`, `/api/identity/email/verify`, `/api/identity/alias/bind-email`) proxy `services/identity-gateway`. Each request is a `POST` with:

```
X-API-Key: <IDENTITY_GATEWAY_KEY>
X-API-Timestamp: <unix seconds>
X-API-Signature: hex(HMAC-SHA256(secret, METHOD "\n" path "\n" hex(SHA-256(body)) "\n" timestamp))
Idempotency-Key: <optional>
```

(`app/lib/identity-gateway.ts`; verified server-side in `authenticateRequest` and `computeSignature`, `services/identity-gateway/server.go`, default timestamp skew 5 minutes.) Request bodies: register `{email, aliasHint}`, verify `{email, code}`; verify returns `bindToken`, which bind-email requires. The gateway's bind-email handler additionally requires `alias` and `aliasSignature` in the body; the wallet-lite client does not send them, so bind-email requests from this app are rejected with `alias required` (HTTP 401).

See [`identity/email-verify.http`](identity/email-verify.http) for the request shapes.

## Sending funds from a real wallet

Wallet Lite has no transfer panel. To send NHB or ZNHB, sign a transfer and submit it as described in [wallets.md](wallets.md), from server-side code only, so the bearer token stays off the client. Key handling guidance for production wallets is in [`docs/wallet/key-management.md`](../wallet/key-management.md).
