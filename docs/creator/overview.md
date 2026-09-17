# Creator Module Overview

The creator module powers a native fan economy on NHB Chain. It lets artists and communities publish content, receive direct tips, and bootstrap sustainable revenue via fan staking. Every action emits structured events so downstream indexers, discovery portals, and devnet demos can track lifecycle changes in real time.

## Core Concepts

| Concept | Description |
| --- | --- |
| **Content** | Immutable publication envelope containing the creator address, canonical ID, off-chain URI, metadata stub, publish timestamp, and cumulative engagement counters. |
| **Tip** | Transfer of liquid NHB from a fan to a content creator. Tips immediately credit the creator’s balance and are tracked against the originating content. |
| **Stake** | Fans can lock NHB behind a creator to signal long-term support. Staking mints yield into the creator’s payout ledger at a configurable reward rate. |
| **Payout Ledger** | Rolling ledger that records total tips, staking yield, pending distribution, and last payout time for each creator. Creators can inspect or claim the ledger at any time via JSON-RPC. |

## State Layout

The module persists data under the following storage prefixes:

- `creator/content/<contentId>` – RLP-encoded `Content` records.
- `creator/stake/<creator>/<fan>` – RLP-encoded `Stake` positions keyed by creator/fan pair.
- `creator/ledger/<creator>` – RLP-encoded `PayoutLedger` for each creator.

Accounts are debited/credited atomically through the existing account manager, ensuring balance consistency with other modules.

## Event Stream

Every mutation emits an indexed event ready for discovery tooling:

| Event | Attributes |
| --- | --- |
| `creator.content.published` | `contentId`, `creator`, `uri` |
| `creator.content.tipped` | `contentId`, `creator`, `fan`, `amount` |
| `creator.fan.staked` | `creator`, `fan`, `amount`, `shares` |
| `creator.fan.unstaked` | `creator`, `fan`, `amount` |
| `creator.payout.accrued` | `creator`, `pending`, `totalTips`, `totalYield` |

Events are wrapped with the standard `events.Event` interface so `StateProcessor.AppendEvent` receives structured payloads for RPC and archive indexing. Devnet scripts can subscribe to this stream to power the creator studio demo and surface live accrual progress.

## Lifecycle Flow

1. **Publish** – A creator calls `creator_publish` to register a new content ID. The engine normalises the payload, persists the record, and emits `creator.content.published`.
2. **Tip** – A fan calls `creator_tip` with the published ID and amount. Balances are transferred, content totals incremented, the payout ledger is updated, and twin `creator.content.tipped` / `creator.payout.accrued` events fire.
3. **Stake** – Supporters lock NHB with `creator_stake`, producing shares and a staking yield bump that is added to `PendingDistribution`. A discovery event highlights the backing and yield change.
4. **Unstake** – Fans can exit via `creator_unstake`, returning funds and adjusting share counts while emitting `creator.fan.unstaked`.
5. **Claim** – Creators view or withdraw pending balances through `creator_payouts`, optionally zeroing the pending amount and logging the new ledger snapshot.

This flow is mirrored in the `/examples/creator-studio` Next.js app and the accompanying documentation so operators can experiment end-to-end on devnet.
