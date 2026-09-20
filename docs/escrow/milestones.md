# Escrow milestones

Milestone projects group several payment "legs" between one payer and one payee. Code: `native/escrow/types_milestone.go`, `native/escrow/engine_milestone.go`, `native/escrow/engine_milestone_signature.go`, the node methods `Node.EscrowMilestone*` in `core/node.go`, and the RPC handlers in `rpc/escrow_milestone_handlers.go`.

> **Important: these RPCs are not transactions.** Unlike classic escrow (whose write RPCs are disabled in favour of signed transactions, see [`escrow.md`](./escrow.md)), the milestone write methods are live JSON-RPC methods that change the state of the node that receives the call (`Node.EscrowMilestoneCreate/Fund/Release/Cancel/SubscriptionUpdate` write directly to the node's state trie under its state lock). There is no milestone transaction type. The milestone code path does not go through block execution, so a milestone change is applied only on the node you called. Treat the milestone methods as single-node functionality until a transaction-based path exists.

## Authentication and authorization

Every milestone method, including `escrow_milestoneGet`, requires RPC authentication (a JWT bearer token, or a verified client certificate; `requireAuthInto` in `rpc/http.go`).

Mutating methods additionally require a wallet signature. There is no `caller` field; the authorized party is the signer recovered from the signature, and it must equal the project's payer (`milestoneUnauthorized("payer")` otherwise). Each `signature` parameter is a 65-byte secp256k1 signature, hex encoded (optional `0x`), over `keccak256` of a canonical JSON envelope (no EIP-191 prefix), built from that call's own fields:

* `escrow_milestoneCreate`: `MilestoneCreateEnvelope`
  `{"action":"milestoneCreate","payer":"<40 hex>","payee":"<40 hex>","realm":"<omitted if empty>","meta":"<0x hex, omitted if empty>","legs":[{"id":1,"type":"deliverable","token":"NHB","amount":"<decimal>","deadline":1730000000}],"subscription":{"intervalSeconds":..,"nextReleaseAt":..,"active":..}}`
  (`subscription` omitted if none). Leg `title` and `description` are not part of the signed bytes. The signer must equal `payer`.
* `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`: `MilestoneActionEnvelope`
  `{"projectId":"<64 hex, no 0x>","legId":<n>,"action":"milestoneFund|milestoneRelease|milestoneCancel"}`.
* `escrow_milestoneSubscriptionUpdate`: `{"projectId":"<64 hex>","action":"milestoneSubscriptionUpdate","active":<bool>,"sequence":<n>}` where `sequence` is the subscription's current `sequence` counter as returned by `escrow_milestoneGet`. The node increments it every time a toggle commits, so a captured signature cannot be replayed.

Address forms differ between the request, the signed envelope and the result. In the `escrow_milestoneCreate` request the `payer` and `payee` params are bech32 account addresses with prefix `nhb` or `znhb` (`parseBech32Address`, `rpc/escrow_handlers.go`, which calls `genesis.ParseBech32Account`; a hex address is rejected with `invalid_params`). The node converts them to 20-byte addresses, and the signed envelope then carries the same 20 bytes as 40 hex characters. Results and events use `0x` plus 40 hex (see "RPC results"). The `escrow_milestoneCreate` request object has the fields `payer`, `payee`, `realm`, `meta`, `legs`, `subscription` and `signature`; `legs[].title` and `legs[].description` are sent in the request but are not in the signed bytes.

Go helpers that build these signatures: `SignMilestoneCreateEnvelope`, `SignMilestoneActionEnvelope`, `SignMilestoneSubscriptionEnvelope`.

## Data model

Project status: `draft` (created, nothing funded), `active` (at least one leg funded), `completed` (every leg released or cancelled, checked when a leg is released), `cancelled` (no leg is pending or funded any more, checked after a cancel or expiry).

Leg status: `pending`, `funded`, `released`, `cancelled`, `expired`.

Leg type: `deliverable` or `timebox`. The RPC also accepts `subscription` as an alias for `timebox`.

Create validation:

* At least one leg; each leg `id` greater than zero and unique within the project.
* Leg `title` required, `amount` a positive decimal integer string, `token` required (upper-cased; only `NHB` and `ZNHB` can be moved), `deadline` in the future (5 seconds of clock skew tolerated).
* `meta` optional, `0x`-prefixed even-length hex of at most 96 bytes.
* `subscription` optional; `intervalSeconds` and `nextReleaseAt` must both be positive.
* The project ID is `sha256` over a fingerprint of the project contents plus the creation time in nanoseconds; it is returned as `{"id": "0x..."}` and is not derivable in advance.

## Project lifecycle

1. **Create** (`escrow_milestoneCreate`): stores the project as `draft`; emits `escrow.milestone.created`.
2. **Fund** (`escrow_milestoneFund`, params `id`, `legId`, `signature`): the payer's balance is moved into a per-leg vault account, the leg becomes `funded` and the project `active`. Fails if the leg is not `pending` or its deadline has passed. Result `{"status":"funded"}`.
3. **Release** (`escrow_milestoneRelease`): moves the leg amount from its vault to the payee and marks the leg `released`. Only the payer can release. Result `{"status":"released"}`.
4. **Cancel** (`escrow_milestoneCancel`): a `funded` leg is refunded to the payer from its vault; the leg becomes `cancelled`. A released leg cannot be cancelled. Result `{"status":"cancelled"}`.
5. **Subscription toggle** (`escrow_milestoneSubscriptionUpdate`, params `id`, `active`, `signature`): sets `subscription.active` and returns the project. Fails if the project has no subscription. The node stores and toggles the schedule fields but does not itself create legs from the schedule.
6. **Get** (`escrow_milestoneGet`, param `id`): returns the project. This is a read; overdue funded legs are shown as `expired` in the returned view but that view is not persisted.

Amounts move in token base units, as decimal strings.

## Vaults and deadlines

Each funded leg uses a derived vault address (`milestoneVaultAddress`, seed `module/milestone/<projectId>/<legId>/<TOKEN>`), so locked balances are separate per leg.

`escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel` and `escrow_milestoneSubscriptionUpdate` first sweep overdue legs (`sweepMilestoneDueLegsLocked`): any `funded` leg whose deadline has passed is refunded from its vault to the payer, marked `expired`, and `escrow.milestone.leg_due` is emitted. If no open legs remain the project becomes `cancelled`. A leg cannot be funded after its deadline.

## RPC results

`escrow_milestoneGet` and `escrow_milestoneSubscriptionUpdate` return:

```json
{
  "id": "0x...", "payer": "0x<40 hex>", "payee": "0x<40 hex>", "realm": "", "status": "draft",
  "createdAt": 0, "updatedAt": 0, "meta": "0x...",
  "legs": [{"id": 1, "type": "deliverable", "title": "", "token": "NHB", "amount": "0", "deadline": 0, "status": "pending"}],
  "subscription": {"intervalSeconds": 0, "nextReleaseAt": 0, "active": false, "sequence": 0}
}
```

`payer` and `payee` are `0x` plus hex of the 20 address bytes (not bech32). `meta` and `subscription` are omitted when empty. Errors: HTTP 404 with code `-32022` for an unknown project or leg; HTTP 403 with `-32023` when the signer is not the payer; HTTP 400 with `-32021` for invalid parameters; HTTP 401 with `-32001` without valid RPC authentication.

## Events

| Event | Trigger |
|-------|---------|
| `escrow.milestone.created` | Project created |
| `escrow.milestone.funded` | Leg funded |
| `escrow.milestone.released` | Leg released |
| `escrow.milestone.cancelled` | Leg cancelled |
| `escrow.milestone.leg_due` | Overdue funded leg expired and refunded |

Attributes (`newMilestoneEvent`): `projectId`, `payer`, `payee` (hex), `realmId`, `status` (numeric project status); `subscriptionInterval`, `subscriptionNext`, `subscriptionActive` when a subscription exists; and for leg events `legId`, `legType` (1 = deliverable, 2 = timebox), `legAmount`, `legDeadline`, `legStatus` (0 pending, 1 funded, 2 released, 3 cancelled, 4 expired).

Numeric project status values: 0 draft, 1 active, 2 completed, 3 cancelled.
