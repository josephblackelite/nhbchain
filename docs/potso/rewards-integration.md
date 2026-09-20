# POTSO Rewards Accounting Integration

This guide explains how to read POTSO reward payouts from the NHB node's JSON-RPC
API for external accounting and reconciliation. It covers the reward query and
export methods that actually exist on the node today.

## Reward Payout Modes

Each reward entry is paid out in one of two modes:

* `auto` — the reward is credited automatically; no further action is required.
* `claim` — the reward is reserved for the winner until it is settled. The
  `potso_reward_claim` method that settled it is retired (see below), so a
  `claim`-mode reward has no way to be paid today; deployments use `auto`.

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

### `potso_reward_claim` (retired)

Retired: every call gets HTTP 410 with JSON-RPC error `-32060` and nothing is
executed. The method paid a `claim`-mode reward on the live state of the one
validator that handled the call, outside block execution, so that validator's
next block differed from the others'. A claim-mode payout will need a signed
transaction that every validator executes in a block; until one exists, use
`auto` mode.

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
3. **Claim-mode rewards** are not settled through the RPC any more
   (`potso_reward_claim` is retired). Only `auto`-mode rewards are paid; an
   entry with `claimed=false` and `mode=claim` stays unpaid.

## Not currently available

There is no webhook/push notification system for reward events (no
`potso.rewards.ready`/`potso.rewards.paid` equivalent), no JSONL export
alongside the CSV export above, and no separate "mark rewards paid" method.
Accounting systems need to poll `potso_rewards_history` /
`potso_export_epoch` rather than subscribe to push events.
