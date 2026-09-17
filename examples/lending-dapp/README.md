# NHBChain Lending dApp Example

This example showcases how to build a lending interface on top of the NHBChain developer APIs. It is intentionally lightweight so it can act as a copy-and-paste starting point for experimentation, hackathons, or production prototypes.

## Features

- **Earn**: Supply and withdraw NHB liquidity using the NHBChain Lending Playbook pattern LEND-02.
- **Borrow**: Deposit ZNHB as collateral, borrow NHB, repay loans, and experiment with developer fees using the NHBChain Lending Playbook pattern LEND-01.
- **Fee Recipient Demo**: Demonstrates how a developer can monetize their local deployment via the `lend_borrowNHBWithFee` RPC endpoint.
- **Empowerment Narrative**: Includes messaging that connects the protocol to NHBChain's mission of expanding access to credit for unbanked communities.

## Getting Started

```bash
npm install
npm run dev
```

Visit http://localhost:3000 to explore the landing page, and use the navigation to jump between the Earn and Borrow flows.

> **Tip:** The component structure mirrors the walkthrough in the [NHBChain Lending developer guide](../../docs/), making it easy to align the UI with the accompanying documentation and RPC examples.

## Project Structure

- `pages/index.tsx` — Landing page with a summary, call to action, and links to docs.
- `pages/earn.tsx` — Earn flow with mock state management for supply and withdraw actions.
- `pages/borrow.tsx` — Borrow flow demonstrating collateral, borrowing, repayment, and fee recipient entry.
- `components/` — Reusable UI primitives (cards, layout, and forms).
- `lib/mockData.ts` — Mock data to make the example interactive without deploying contracts.

The mock state flows can be swapped for real NHBChain RPC calls when you are ready to integrate with the chain.

## Production Considerations

- Integrate real wallet authentication (e.g., WalletConnect or custom NHBChain wallets).
- Replace the mock state hooks with real calls to the NHBChain RPC endpoints found in `/docs`.
- Validate addresses before accepting them as developer fee recipients.
- Add analytics to capture supply/borrow events and monitor liquidity health.

## License

This example inherits the root repository license.
