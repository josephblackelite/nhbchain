# POTSO Rewards Accounting Integration

This guide explains how to read POTSO reward payouts from the NHB node's JSON-RPC
API for external accounting and reconciliation. It covers the reward query and
export methods that actually exist on the node today.

## Reward Payout Modes

Each reward entry is paid out in one of two modes:

* `auto` — the reward is credited automatically; no further action is required.
* `claim` — the reward is reserved for the winner and must be settled explicitly
  via `potso_reward_claim` before it is paid.

## JSON-RPC Interfaces

### `potso_rewards_history`

Fetch reward history for a single participant, paginated across epochs.

Parameters:

```json
{
  "address": "nhb1...",
  "cursor": "",
  "limit": 50
}
```

`address` (Bech32, required). `cursor` and `limit` are optional pagination
controls; `limit` defaults to 50 on the node if omitted.

Response:

```json
{
  "address": "nhb1...",
  "entries": [
    { "epoch": 123, "amount": "1000000000000000000", "mode": "auto" }
  ],
  "nextCursor": "173"
}
```

`amount` is a decimal string in wei. `nextCursor` is present when more pages
are available and empty otherwise.

### `potso_reward_claim`

Settle a `claim`-mode reward for a specific epoch and address. Requires a
signature from the winning address.

Parameters:

```json
{
  "epoch": 123,
  "address": "nhb1...",
  "signature": "0x..."
}
```

Response:

```json
{ "paid": true, "amount": "1000000000000000000" }
```

Errors: `reward not found` (404) when no claimable reward exists for that
epoch/address pair, `claiming disabled` (400) when the deployment's reward
configuration doesn't allow manual claims, and `INSUFFICIENT_TREASURY` (409)
if the reward treasury can't currently cover the payout.

### `potso_export_epoch`

Generate a CSV export of the full payout ledger for one epoch.

Parameters:

```json
{ "epoch": 123 }
```

Response:

```json
{
  "epoch": 123,
  "csvBase64": "YWRkcmVzcyxhbW91bnQsY2xhaW1lZCxjbGFpbWVkQXQsbW9kZQ0K...",
  "totalPaid": "50000000000000000000",
  "winners": 42
}
```

`csvBase64` is the export encoded as base64; decode it to get the raw CSV.
`totalPaid` is a decimal wei string summed across all winners in the epoch.

#### CSV Schema

```
address,amount,claimed,claimedAt,mode
```

* `address` — Bech32 NHB address.
* `amount` — decimal wei string.
* `claimed` — `true`/`false`; always `true` for `auto` entries.
* `claimedAt` — timestamp the reward was settled, empty if unclaimed.
* `mode` — `auto` or `claim`.

## Accounting Checklist

1. **Pull per-participant history** via `potso_rewards_history` when
   reconciling an individual account, paginating with `cursor` until
   `nextCursor` is empty.
2. **Pull a full epoch ledger** via `potso_export_epoch` when reconciling an
   entire epoch's payouts against the treasury; decode `csvBase64` and import
   using `address` + `amount` as your dedupe key (there is no separate
   idempotency checksum field on this export).
3. **Settle claim-mode rewards** via `potso_reward_claim`, using the returned
   `paid`/`amount` to confirm settlement — repeated claims against an
   already-paid reward return `reward not found`, not a silent success, so
   treat that error as "already handled" rather than a hard failure when
   retrying.

## Not currently available

There is no webhook/push notification system for reward events (no
`potso.rewards.ready`/`potso.rewards.paid` equivalent), no JSONL export
alongside the CSV export above, and no separate "mark rewards paid" method —
settlement for `claim`-mode rewards happens through `potso_reward_claim`
itself. Accounting systems need to poll `potso_rewards_history` /
`potso_export_epoch` rather than subscribe to push events.
