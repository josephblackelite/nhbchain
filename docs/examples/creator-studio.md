# Creator Studio Example

The creator studio is a Next.js workspace that demonstrates the new creator module lifecycle: publish → tip → stake → payout. It proxies JSON-RPC requests through an API route and renders real-time ledger feedback driven by the events emitted from the chain.

## Getting Started

```bash
cd examples
cp .env.example creator-studio/.env.local # populate NHB_RPC_URL + NHB_RPC_TOKEN
cd creator-studio
yarn install
yarn dev
```

Visit `http://localhost:3000` and provide the following inputs:

1. **Creator / Fan** – Devnet Bech32 addresses with funded balances.
2. **Content** – Choose a unique `contentId`, URI, and metadata JSON.
3. **Tip** – Enter the NHB amount (in wei) a fan will tip the creator.
4. **Stake** – Lock additional NHB behind the creator to mint staking yield.
5. **Payouts** – Inspect the payout ledger or claim pending accruals directly.

The right-hand log shows every RPC call so you can correlate UI actions with emitted events. When running against devnet, tail the node logs or connect an indexer to the following event keys to confirm indexing coverage:

- `creator.content.published`
- `creator.content.tipped`
- `creator.fan.staked`
- `creator.fan.unstaked`
- `creator.payout.accrued`

## Devnet Workflow

1. Publish content from the creator address.
2. Send a tip from the fan address and observe balances shift.
3. Stake additional NHB to trigger payout accruals.
4. Refresh the ledger to view pending totals.
5. Claim payouts and verify the pending balance drops to zero.

This walkthrough mirrors the behaviour described in [`docs/creator/overview.md`](../creator/overview.md) and is intended for demos, QA, and partner onboarding.
