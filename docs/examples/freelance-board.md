# Freelance board example

The freelance board provides a design reference for milestone projects, subscription retainers, and skill verification. The Next.js project lives under `examples/freelance-board` and focuses on deterministic data structures rather than backend integrations.

## Getting started

```bash
cd examples/freelance-board
npm install
npm run dev
```

The app includes:

* **Dashboard** – displays milestone legs with statuses that map to the RPC transitions (`escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`).
* **Subscription view** – walks through recurring retainers using `escrow_milestoneSubscriptionUpdate`.
* **Skill ledger** – showcases the payload returned by `reputation_verifySkill`.

## Integrating with devnet

The UI currently mocks responses because the milestone engine is shipping incrementally. When devnet support arrives you can:

1. Point API calls at the JSON-RPC endpoint.
2. Use deterministic IDs and metadata when calling `escrow_milestoneCreate`.
3. Listen for the event topics listed in [docs/escrow/milestones.md](../escrow/milestones.md).

Keep production secrets out of the demo app. Treat it as a blueprint for your own frontend implementation.
