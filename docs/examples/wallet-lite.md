# Wallet Lite Demo

Wallet Lite is a static-friendly Next.js application that exercises the identity module on
`nhbcoin.net` JSON-RPC endpoints. It focuses on three workflows: username registration, pay-by-email
claimables, and QR payment intents.

## Pages & Features

The single-page interface is split into panels:

* **Local session** – Paste or generate a throwaway secp256k1 private key. The app derives the NHB
  bech32 address locally and never persists the key.
* **Account snapshot** – Fetches balances and alias metadata via `nhb_getBalance` for the derived
  address.
* **Register username** – Calls `identity_setAlias` through a server-side API route that attaches the
  configured RPC bearer token.
* **Send via claimable** – Creates escrowed payments with `identity_createClaimable`. Users can select
  alias, email, or raw preimage recipients. For email targets, the server hashes the address with the
  configured salt before invoking the RPC.
* **Claim funds** – Redeems claimables via `identity_claim`. The form auto-derives the alias preimage
  when the claimant enters their username.
* **QR payment intent** – Generates a `znhb://pay` URI and renders a QR code for scanning wallets.

## RPC & Gateway Usage

Wallet Lite interacts with the following endpoints:

| Flow | RPC Method | Notes |
| --- | --- | --- |
| Account snapshot | `nhb_getBalance` | Read-only; no authentication required. |
| Username registration | `identity_setAlias` | Requires `Authorization: Bearer <NHB_RPC_TOKEN>`. |
| Claimable creation | `identity_createClaimable` | Accepts alias strings or salted email hashes. |
| Claim redemption | `identity_claim` | Invoked when the recipient has the alias/email preimage. |
| Alias discovery | `identity_resolve` | Used to surface avatars, created-at timestamps, and primary addresses. |

The application does not call the identity gateway directly; email hashing happens server-side via
`IDENTITY_EMAIL_SALT`. `NHB_WS_URL` is read from the root environment for future live updates but is
not yet consumed by the UI.

## Security Posture

* Private keys live only in client state. Refreshing the page clears them.
* Sensitive credentials (`NHB_RPC_TOKEN`, `IDENTITY_EMAIL_SALT`) stay on the Next.js server runtime
  and are never injected into the browser bundle.
* Claimable payloads are validated and normalised before hitting the RPC to avoid malformed requests.
* The default production base URL is `https://nhbcoin.com`. Set `APP_PUBLIC_BASE` accordingly when
  hosting behind CloudFront or S3.

## Claimables Walkthrough

1. Register a username for the payer address.
2. Choose **Email** as the recipient type and enter the recipient's email. Wallet Lite hashes the
   email and sends `identity_createClaimable` with the resulting 32-byte hex string.
3. Share the returned `claimId` with the recipient. They verify the email through the identity
   gateway, register an alias, and claim using the same app by entering the claim ID and alias (the
   preimage is derived automatically).
4. When the claim succeeds, the alert surface confirms the token and amount released.

Because the UI also supports alias recipients, the same claimable flow can be used to send funds to
registered usernames—helpful for showcasing escrow holds and expiry countdowns.
