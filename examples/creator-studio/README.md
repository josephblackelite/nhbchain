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

**Retired RPC methods: `creator_publish`, `creator_tip`, `creator_stake`, and `creator_unstake` are currently disabled.**
They used to mutate validator-local state directly outside the block pipeline, which guarantees a consensus fork/halt on
this chain's 2-validator zero-quorum-slack topology, so the node's RPC layer now returns `410 Gone` for all four
unconditionally (see `rpc/creator_handlers.go`'s `creatorRPCDisabledMessage`). Every call this demo's publish/tip/stake/
unstake UI makes will fail until a signed-transaction replacement ships -- there is no working alternative flow yet, so
this UI's write actions are non-functional today, not just insecure.

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
