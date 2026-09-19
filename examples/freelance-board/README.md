# Freelance Board

A static Next.js mock-up of a milestone marketplace: three pages (`/`, `/subscriptions`, `/skills`) with hard-coded sample data. It makes no RPC or WebSocket calls. The page text names the `escrow_milestone*` and `reputation_verifySkill` RPC methods and the `escrow.milestone.*` event topics; those are documented, with their parameters and results as implemented in the node, in [`docs/examples/freelance-board.md`](../../docs/examples/freelance-board.md).

## Getting started

```bash
cd examples/freelance-board
npm install
npm run dev
```
