# Escrow milestones

This document outlines the milestone-based escrow workflow provided by the JSON-RPC surface. The implementation is intentionally conservative: the wire format and validation logic are shipped ahead of stateful execution so that client SDKs and front-ends can start integration work.

## Project lifecycle

1. **Creation** – The payer prepares the project graph with a sequence of legs. Each leg declares:
   - A deterministic `id` (monotonic per project).
   - Its semantic `type`: `deliverable` for fixed-scope work or `timebox` for subscription style retainers.
   - Funding token, amount, descriptive metadata and a deadline.
2. **Funding** – Once created, the payer funds individual legs via `escrow_milestoneFund`. The RPC performs syntactic validation and defers persistence to the milestone engine.
3. **Release** – Successful delivery is acknowledged through `escrow_milestoneRelease`. Legs transition to the `released` state.
4. **Cancellation** – If requirements change the payer can cancel an outstanding leg with `escrow_milestoneCancel`.
5. **Subscriptions** – For retainers the optional subscription schedule automatically tracks the next release checkpoint. `escrow_milestoneSubscriptionUpdate` allows toggling an agreement on or off without mutating leg history.

All endpoints emit typed events for ledgering:

| Event | Trigger |
|-------|---------|
| `escrow.milestone.created` | Project created |
| `escrow.milestone.funded` | Leg funded |
| `escrow.milestone.released` | Leg released |
| `escrow.milestone.cancelled` | Leg or project cancelled |
| `escrow.milestone.leg_due` | Funded leg reached its deadline without release |

## Deadlines and disputes

Leg deadlines are enforced at the RPC layer to guarantee that newly funded milestones are forward-looking. When a deadline elapses without release the engine emits a `leg_due` event. The current stub implementation does not yet trigger automated refunds; operators should monitor the event stream and initiate dispute resolution under the configured realm policy.

## Safety checklist

* Capture project metadata off-chain alongside the deterministic leg IDs for auditability.
* Only enable the milestone RPCs for wallets that have passed KYC / KYB and hold the relevant realm roles.
* Configure governance policies so arbitrators understand how to adjudicate expired `timebox` retainers versus project-based `deliverable` legs.
* Persist client-side receipts for funding and release events until the stateful engine lands in a subsequent release.
