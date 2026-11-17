# Escrow milestones

Milestone projects group several payment "legs" between one payer and one payee. Code: `native/escrow/types_milestone.go`, `native/escrow/engine_milestone.go`, `native/escrow/engine_milestone_signature.go`, the node methods `Node.EscrowMilestone*` in `core/node.go`, and the RPC handlers in `rpc/escrow_milestone_handlers.go`.

## Current availability

* **Only `escrow_milestoneGet` is live.** It requires RPC authentication (a JWT bearer token or a verified client certificate, `requireAuthInto` in `rpc/http.go`).
* **The five write methods are disabled.** `escrow_milestoneCreate`, `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel` and `escrow_milestoneSubscriptionUpdate` answer HTTP 410 with code `-32060` (`milestoneRPCDisabledMessage`, `rpc/escrow_milestone_handlers.go`). The reason given in the code: these methods wrote validator-local state outside the block pipeline, which makes one validator's state root differ from its peers'.
* **There is no milestone transaction type.** Nothing else calls `Node.EscrowMilestoneCreate`, `Fund`, `Release`, `Cancel` or `SubscriptionUpdate`, so no project can be created on a running chain and no milestone event is emitted. `escrow_milestoneGet` can only return a project record that is already in state.

Anything below describes the record format and the engine code for reference.

## `escrow_milestoneGet`

Request: `{"jsonrpc":"2.0","id":1,"method":"escrow_milestoneGet","params":[{"id":"0x<64 hex>"}]}`. The `id` is 64 hex characters with an optional `0x`.

Result (`milestoneProjectJSON`):

```json
{
  "id": "0x...", "payer": "0x<40 hex>", "payee": "0x<40 hex>", "realm": "", "status": "draft",
  "createdAt": 0, "updatedAt": 0, "meta": "0x...",
  "legs": [{"id": 1, "type": "deliverable", "title": "", "token": "NHB", "amount": "0", "deadline": 0, "status": "pending"}],
  "subscription": {"intervalSeconds": 0, "nextReleaseAt": 0, "active": false, "sequence": 0}
}
```

`payer` and `payee` are `0x` plus hex of the 20 address bytes (not bech32). `meta` is `0x` plus hex, empty string when there is none. `subscription` is omitted when the project has none. The read is display-only: a funded leg whose deadline has passed is shown as `expired`, but nothing is written to state (`Node.EscrowMilestoneGet`).

Errors:

| Condition | HTTP | Code |
|-----------|------|------|
| Not authenticated | 401 | `-32001` |
| Not exactly one parameter object, bad JSON or a bad ID | 400 | `-32021` |
| Unknown project | 500 | `-32025` (`internal_error`, data `escrow: milestone leg not found`) |

An unknown project is reported as an internal error rather than a 404: `writeMilestoneError` matches only the text `escrow not found`, and the error the node returns for a missing project has different text.

## Data model

Project status: `draft` (created, nothing funded), `active` (at least one leg funded), `completed` (every leg released or cancelled), `cancelled` (no leg pending or funded). Numeric values 0 to 3 in that order.

Leg status: `pending`, `funded`, `released`, `cancelled`, `expired` (0 to 4).

Leg type: `deliverable` (1) or `timebox` (2).

Amounts are token base units as decimal strings. Only `NHB` and `ZNHB` can be moved.

## Engine behavior (unreachable on a running chain)

The engine and the disabled handlers implement the following. It is listed so the record format above makes sense, not as something you can call.

* Create validation: at least one leg; each leg `id` greater than zero and unique; `title` required; `amount` a positive decimal integer; `token` required; `deadline` in the future (5 seconds of skew tolerated); `meta` at most 96 bytes; a `subscription` needs a positive `intervalSeconds` and `nextReleaseAt`.
* Each funded leg would sit in its own derived vault address (`milestoneVaultAddress`, seed `module/milestone/<projectId>/<legId>/<TOKEN>`).
* Mutations were authorized by a 65-byte secp256k1 signature over `keccak256` of a canonical JSON envelope (`MilestoneCreateEnvelope`, `MilestoneActionEnvelope`, and a subscription envelope carrying a per-project `sequence` counter), and the recovered signer had to be the payer (`native/escrow/engine_milestone_signature.go`).
* Event names defined for the engine (`native/escrow/events.go`): `escrow.milestone.created`, `escrow.milestone.funded`, `escrow.milestone.released`, `escrow.milestone.cancelled`, `escrow.milestone.leg_due`.
