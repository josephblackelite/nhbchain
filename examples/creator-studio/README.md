# Creator Studio

The Creator Studio workspace showcases the full creator lifecycle (publish → tip → stake → payout) against live NHB endpoints.
It re-exports the flows documented in [`docs/examples/creator-studio.md`](../../docs/examples/creator-studio.md) and proxies
JSON-RPC calls through `pages/api/rpc.ts`, which keeps the bearer token out of the browser.

**Security warning: this is a demo, not a production-ready app.** The proxy now enforces a server-side allowlist restricting
every call to the exact `creator_*` methods this UI uses, but it does **not** authenticate the *visitor* -- every one of
those methods is mutating (it moves funds or publishes content on behalf of a `caller`/`fan`/`creator` address supplied in
the request body) and none of them are verified against who is actually making the request. Do not expose this app on a
public network without first adding real per-visitor authentication (e.g. a wallet-signature challenge) in front of every
call.

## Getting started

```bash
cd examples
cp .env.example creator-studio/.env.local
cd creator-studio
yarn install
yarn dev
```

Read the dedicated guide for the full walkthrough, expected event emissions, and devnet debugging tips:
[`docs/examples/creator-studio.md`](../../docs/examples/creator-studio.md).
