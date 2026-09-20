# Freelance board

`examples/freelance-board` is a static Next.js 13 mock-up of a milestone marketplace UI. It is not a workspace member (it has its own `package.json`) and it makes no network requests: the three pages contain hard-coded sample data and text.

```bash
cd examples/freelance-board
npm install
npm run dev
```

## What the pages show

- `/` lists three hard-coded milestone legs and names `escrow_milestoneFund`, `escrow_milestoneRelease` and `escrow_milestoneCancel` and the `escrow.milestone.*` event topics.
- `/subscriptions` describes a retainer flow around `escrow_milestoneSubscriptionUpdate`.
- `/skills` lists two hard-coded skill attestations and mentions `reputation_verifySkill`.

Nothing on these pages subscribes to a WebSocket or calls the node, whatever the page text says (`pages/index.tsx` says the UI subscribes to websocket relays for the `escrow.milestone.*` topics; it does not).

## Status of the RPC methods the pages refer to

Every method the pages name that writes state is retired in the node and answers HTTP `410` with error code `-32060` (`codeMethodDisabled`):

| Method | Handler |
| --- | --- |
| `escrow_milestoneCreate`, `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`, `escrow_milestoneSubscriptionUpdate` | `rpc/escrow_milestone_handlers.go` (`milestoneRPCDisabledMessage`) |
| `reputation_verifySkill` | `rpc/reputation_handlers.go` (`reputationRPCDisabledMessage`) |

The messages say the methods mutated validator-local state outside the block pipeline and that a signed-transaction replacement is pending. There is no milestone or reputation transaction type in `core/types/transaction.go`, and nothing in the node calls the `Node.EscrowMilestoneCreate`, `Fund`, `Release`, `Cancel` or `SubscriptionUpdate` methods or `Node.ReputationVerifySkill` (`core/node.go`), so at present no live path creates or changes a milestone project or records a skill verification, and no live path emits the `escrow.milestone.*` events (topic names are defined in `native/escrow/events.go`: `escrow.milestone.created`, `escrow.milestone.funded`, `escrow.milestone.released`, `escrow.milestone.cancelled`, `escrow.milestone.leg_due`).

### `escrow_milestoneGet`

The one milestone method that is still served (`handleEscrowMilestoneGet`). It requires `Authorization: Bearer <JWT>` (`requireAuthInto`; without it the node answers HTTP 401 with code `-32001`). It takes a single parameter object `{"id": "<32-byte id, hex, optional 0x>"}`.

Result (`milestoneProjectJSON`, `formatMilestoneJSON`): `id`, `payer` and `payee` (as `0x` hex, empty string for a zero address), `realm`, `status` (`draft`, `active`, `completed`, `cancelled`), `createdAt`, `updatedAt`, `meta` (`0x` hex or empty), `legs` (`id`, `type` `deliverable` or `timebox`, `title`, `token`, `amount` decimal string, `deadline`, `status` one of `pending`, `funded`, `released`, `cancelled`, `expired`), and, when the project has one, `subscription` (`intervalSeconds`, `nextReleaseAt`, `active`, `sequence`).

Errors: a wrong parameter count or an id that does not parse is HTTP 400 with `invalid_params` (code `-32021`); a project that does not exist is HTTP 404 with `not_found` (code `-32022`) (`writeMilestoneError`). Because no live path creates projects, this is the answer for an id that was never stored.

For the escrow module itself see [`docs/escrow/milestones.md`](../escrow/milestones.md).
