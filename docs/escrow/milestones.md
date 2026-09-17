# Escrow milestones

This document describes the live milestone escrow workflow exposed through JSON-RPC. Milestone projects are persisted in node state, funded legs lock value into deterministic vault addresses, releases settle to the payee, and funded overdue legs are automatically refunded to the payer when the project is read or mutated.

## Authorization

Every mutating milestone RPC (`escrow_milestoneCreate`, `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`, `escrow_milestoneSubscriptionUpdate`) requires a wallet signature from the project's payer instead of a plain `caller`/`payer` address field -- the authorized party is derived solely by recovering the signer of a canonical envelope, never trusted from client-supplied text. Each call takes a `signature` parameter (65-byte secp256k1, hex-encoded) over a JSON envelope built from that exact call's own fields (see `native/escrow/engine_milestone_signature.go`'s `MilestoneActionEnvelope`/`MilestoneCreateEnvelope`):

- `escrow_milestoneCreate` signs `{action:"milestoneCreate", payer, payee, realm, meta, legs, subscription}` (addresses as raw hex, matching classic escrow's `CreateWithSignature` convention) -- the recovered signer must equal the claimed `payer`.
- `escrow_milestoneFund` / `escrow_milestoneRelease` / `escrow_milestoneCancel` sign `{action:"milestoneFund"|"milestoneRelease"|"milestoneCancel", projectId, legId}`.
- `escrow_milestoneSubscriptionUpdate` signs `{action:"milestoneSubscriptionUpdate", projectId, active}` -- the toggle's target value travels inside the signed envelope so a signature for one direction can never be replayed for the other.

Every action string is part of what gets signed, so a signature produced for one action (or one leg, or one toggle direction) can never be replayed to authorize a different one.

## Project lifecycle

1. **Creation** - The payer prepares the project graph with a sequence of legs and signs the create envelope. Each leg declares:
   - A deterministic `id` (monotonic per project).
   - Its semantic `type`: `deliverable` for fixed-scope work or `timebox` for subscription-style retainers.
   - Funding token, amount, descriptive metadata, and a deadline.
2. **Funding** - Once created, the payer funds individual legs via `escrow_milestoneFund`. The node debits the payer, credits the deterministic per-leg vault, and persists the leg as `funded`.
3. **Release** - Successful delivery is acknowledged through `escrow_milestoneRelease`. The node pays the locked amount from the vault to the payee and marks the leg as `released`.
4. **Cancellation** - If requirements change, the payer can cancel an outstanding leg with `escrow_milestoneCancel`. If the leg was already funded, the locked amount is refunded from the vault to the payer before the state transition is persisted.
5. **Subscriptions** - For retainers, the optional subscription schedule tracks the next release checkpoint. `escrow_milestoneSubscriptionUpdate` toggles an agreement on or off without mutating completed leg history.

All endpoints emit typed events for ledgering:

| Event | Trigger |
|-------|---------|
| `escrow.milestone.created` | Project created |
| `escrow.milestone.funded` | Leg funded and value locked |
| `escrow.milestone.released` | Leg released and value paid out |
| `escrow.milestone.cancelled` | Leg or project cancelled |
| `escrow.milestone.leg_due` | Funded leg expired and was refunded |

## Vaults and deadlines

Each funded leg uses a deterministic vault address derived from the project ID, leg ID, and token. That keeps locked balances auditable and makes payout and refund flows deterministic.

Deadlines are enforced both when funding and during later project access. A leg cannot be newly funded after its deadline. If a funded leg reaches its deadline without release, the next read or mutating operation sweeps it into the `expired` state, refunds the payer from the vault, and emits `escrow.milestone.leg_due`.

## Safety checklist

* Capture project metadata off-chain alongside the deterministic leg IDs for auditability.
* Only enable the milestone RPCs for wallets that have passed KYC / KYB and hold the relevant realm roles.
* Configure governance policies so arbitrators understand how to adjudicate expired `timebox` retainers versus project-based `deliverable` legs.
* Persist client-side receipts for creation, funding, release, cancellation, and due-expiry events.
