# Wallet Lite

Wallet Lite is a client-side demo that exercises the NHB identity and creator flows:

* Register a username against a bech32 address via `identity_setAlias`.
* Create claimable escrows for usernames or emails with `identity_createClaimable`.
* Claim escrowed funds using the alias preimage or a verified email hash.
* Compose QR codes that encode `znhb://pay` intents.
* Tip creators against published content via `creator_tip`.
* Stake behind creators to simulate subscription-style memberships.
* Browse creator profiles (avatar, addresses, recent drops) fetched from the public gateway.

The demo targets static hosting and only stores private keys in memory. It is suitable for
walkthroughs and automated test accounts; do not connect production keys.

## Getting started

```bash
# Install dependencies from the examples workspace root
cd examples
yarn install

# Launch the Wallet Lite dev server
cd wallet-lite
yarn dev
```

The server reads RPC settings from the repository root `.env` file:

* `NHB_RPC_URL`
* `NHB_RPC_TOKEN`
* `NHB_CHAIN_ID`
* `IDENTITY_EMAIL_SALT`
* `APP_PUBLIC_BASE` (used for metadata URLs)
* `NHB_WS_URL` (optional, reserved for future realtime updates)
* `NHB_API_URL` (defaults to `https://gw.nhbcoin.net` for creator profile lookups)

For static deployments set `APP_PUBLIC_BASE=https://nhbcoin.com` so absolute links resolve correctly.

## Flows

1. Paste or generate a throwaway private key. Wallet Lite derives the NHB bech32 address locally.
2. Register a username. The API route adds the bearer token and calls `identity_setAlias` on
   `NHB_RPC_URL`.
3. Create a claimable payment. Choose between alias, email, or raw preimage recipient types. The
   API computes the salted email hash before invoking `identity_createClaimable`.
4. Claim the funds. Provide the claim ID and either an alias (auto-derived preimage) or an explicit
   preimage returned by the identity gateway.
5. Generate a `znhb://pay` QR code for sharing.
6. Tip a creator and view the pending payout ledger returned by `creator_tip`.
7. Stake (subscribe) or unstake behind a creator with `creator_stake` / `creator_unstake`.
8. Inspect the profile panel to verify avatars, public addresses, and recent content sourced from [`https://gw.nhbcoin.net`](https://gw.nhbcoin.net).

See [`docs/examples/wallet-lite.md`](../../docs/examples/wallet-lite.md) for a deeper dive into the RPC calls and gateway integration points surfaced by the new tipping and subscription panels.

## Security considerations

* Bearer tokens and salts are read only on the Next.js server runtime. They are never exposed to the
  browser.
* Private keys remain in React state—refreshing the page clears them. Do not store real credentials
  in the demo UI.
* Email addresses are hashed with the configured salt before they leave the server.
