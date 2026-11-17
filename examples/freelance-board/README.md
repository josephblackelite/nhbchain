# Freelance Board

A static Next.js mock-up of a milestone marketplace: three pages (`/`, `/subscriptions`, `/skills`) with hard-coded sample data. It makes no RPC or WebSocket calls. The page text names the `escrow_milestone*` and `reputation_verifySkill` RPC methods and the `escrow.milestone.*` event topics. The node retires all of those methods except `escrow_milestoneGet` (HTTP `410`); see [`docs/examples/freelance-board.md`](../../docs/examples/freelance-board.md).

## Getting started

```bash
cd examples/freelance-board
npm install
npm run dev
```
