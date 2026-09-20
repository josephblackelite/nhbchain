# Freelance board

`examples/freelance-board` is a static Next.js 13 mock-up of a milestone marketplace UI. It is not a workspace member (it has its own `package.json`) and it makes no network requests: the three pages contain hard-coded sample data and text.

```bash
cd examples/freelance-board
npm install
npm run dev
```

## What the pages show

- `/` lists three hard-coded milestone legs and names the RPC methods and event topics they correspond to.
- `/subscriptions` describes a retainer flow around `escrow_milestoneSubscriptionUpdate`.
- `/skills` lists two hard-coded skill attestations and names `reputation_verifySkill`.

Nothing on these pages subscribes to a WebSocket or calls the node, whatever the page text says.

## The RPC methods the pages refer to

Caution: these methods are not retired, but the node methods behind the write calls (`EscrowMilestoneCreate`, `Fund`, `Release`, `Cancel`, `SubscriptionUpdate`, and `ReputationVerifySkill` in `core/node.go`) lock `n.stateMu` and write `n.state.Trie` directly, outside the transaction and block pipeline. The comment on `escrowRPCDisabledMessage` in `rpc/escrow_handlers.go` explains why that pattern was disabled for `escrow_create` and the other retired methods: the change exists only on the validator that served the call, so the next block is rejected by the others on a state-root mismatch. Treat the write methods below as unsafe on a multi-validator network. `escrow_milestoneGet` is read-only.

These are dispatched by the node (`rpc/http.go`). All six milestone methods and `reputation_verifySkill` require `Authorization: Bearer <JWT>` (`requireAuthInto`), and are not among the retired methods listed in the [examples index](README.md).

Milestone methods (`rpc/escrow_milestone_handlers.go`). Each takes a single parameter object. Every mutating call carries a `signature`: a 65-byte secp256k1 signature, hex encoded, over a canonical envelope (`escrow.RecoverMilestone*Signer`). There is no caller field; the signer is recovered from the signature, and the node requires it to be the project's payer (`milestoneUnauthorized("payer")` in `core/node.go`).

| Method | Parameters | Result |
| --- | --- | --- |
| `escrow_milestoneCreate` | `payer`, `payee` (bech32), optional `realm`, optional `meta` (`0x` hex, at most 96 bytes), `legs`, optional `subscription`, `signature` | `{"id": "0x..."}` |
| `escrow_milestoneGet` | `id` | Project object (below) |
| `escrow_milestoneFund` | `id`, `legId` (> 0), `signature` | `{"status": "funded"}` |
| `escrow_milestoneRelease` | `id`, `legId`, `signature` | `{"status": "released"}` |
| `escrow_milestoneCancel` | `id`, `legId`, `signature` | `{"status": "cancelled"}` |
| `escrow_milestoneSubscriptionUpdate` | `id`, `active`, `signature` | Project object |

Each leg in `legs`: `id` (> 0, unique), `type` (`deliverable`, `timebox`, or `subscription` as an alias for `timebox`), `title`, `description`, `token`, `amount` (positive base-10 integer string), `deadline` (Unix seconds, must be in the future). At least one leg is required. `subscription` is `{intervalSeconds, nextReleaseAt, active}`.

Project object: `id`, `payer`, `payee` (as `0x` hex), `realm`, `status` (`draft`, `active`, `completed`, `cancelled`), `createdAt`, `updatedAt`, `meta`, `legs` (`id`, `type`, `title`, `token`, `amount`, `deadline`, `status`), and `subscription` (including `sequence`, the counter the next subscription-update signature must cover). Leg `status` is one of `pending`, `funded`, `released`, `cancelled`, `expired`.

Errors use the escrow error codes with messages such as `invalid_params`, `not_found` (HTTP 404) and `forbidden` (HTTP 403).

`reputation_verifySkill` (`rpc/reputation_handlers.go`): parameter object `{verifier, subject, skill, expiresAt?}` (bech32 addresses); returns `{verifier, subject, skill, issuedAt, expiresAt?}` with `issuedAt` set by the node. It does not echo the request payload. An empty `skill` is `invalid_params` / `skill required`.

Authorization: `verifier` is read from the request body, and the request carries no signature over it. `core/node.go` (`ReputationVerifySkill`) requires that address to hold the `ROLE_REPUTATION_VERIFIER` role and otherwise returns `ErrReputationVerifierUnauthorized` (`reputation: caller lacks verifier role`), which the handler maps to HTTP 403 (`rpc/reputation_handlers.go`). The only other gate is the bearer token required for the call, so any token holder can name any verifier address that holds the role.

## Event topics

Defined in `native/escrow/events.go`: `escrow.milestone.created`, `escrow.milestone.funded`, `escrow.milestone.released`, `escrow.milestone.cancelled`, `escrow.milestone.leg_due`. See [`docs/escrow/milestones.md`](../escrow/milestones.md) for the engine.
