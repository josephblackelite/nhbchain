# Creator Studio

The Creator Studio workspace showcases the full creator lifecycle (publish → tip → stake → payout) against live NHB endpoints.
It re-exports the flows documented in [`docs/examples/creator-studio.md`](../../docs/examples/creator-studio.md) and proxies
JSON-RPC calls through `pages/api/rpc.ts` so you can experiment without exposing bearer tokens in the browser.

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
