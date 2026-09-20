# Creator Studio

A Next.js app that was written for the creator flow publish, tip, stake, payout. Only the payout-ledger read (`creator_payouts` with `claim` unset) works against the current node; see [`docs/examples/creator-studio.md`](../../docs/examples/creator-studio.md) for the details and the source references.

**Retired RPC methods.** `creator_publish`, `creator_tip`, `creator_stake`, `creator_unstake` and `creator_payouts` with `claim: true` return HTTP `410 Gone` from the node (`creatorRPCDisabledMessage` in `rpc/creator_handlers.go`). The Publish, Tip, Stake and Claim controls in the UI are disabled and only log a note.

**Security warning: this is a demo.** `pages/api/rpc.ts` forwards only `creator_publish`, `creator_tip`, `creator_stake` and `creator_payouts` (a fixed allowlist), attaching `Authorization: Bearer <token>` from `NHB_RPC_TOKEN` (or `NEXT_PUBLIC_NHB_RPC_TOKEN`). It does not authenticate the visitor. Do not expose it on a public network without adding per-visitor authentication.

## Getting started

```bash
cd examples
cp .env.example creator-studio/.env.local
yarn install
cd creator-studio
yarn dev
```

`NHB_RPC_URL` defaults to `http://localhost:8545` when unset.
