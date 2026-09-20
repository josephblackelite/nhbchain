# POTSO Rewards API Reference

This guide documents the settlement endpoints introduced with POTSO settlement claim mode and the supporting history/export
features. All methods follow JSON-RPC 2.0 semantics and are exposed by the node RPC server.

## RPC Methods

### `potso_reward_claim` (retired)

Retired. The method answers every call with HTTP 410 and JSON-RPC error `-32060`
(`codeMethodDisabled`) and does nothing. It used to pay a claim-mode reward by
debiting the treasury and crediting the claimant on the state of the one
validator that handled the call, outside block execution, so that validator's
next block differed from every other validator's. Rewards are paid at epoch end
in `auto` mode, which needs no claim, and the queries below are unchanged. A
claim-mode payout will need a signed transaction that every validator executes
in a block; none exists yet, so do not run a deployment in `claim` mode.

### `potso_rewards_history`

Returns the chronological settlement history for an address (newest first). No authentication is required.

**Parameters**

```json
{
  "address": "nhb1examplewinner...",
  "cursor": "2",    // optional zero-based offset encoded as a string
  "limit": 2          // optional page size, defaults to 50
}
```

**Response**

```json
{
  "address": "nhb1examplewinner...",
  "entries": [
    { "epoch": 200, "amount": "850000000000000000000", "mode": "auto" },
    { "epoch": 199, "amount": "920000000000000000000", "mode": "claim" }
  ],
  "nextCursor": "2"
}
```

`nextCursor` is omitted when no further pages remain.

### `potso_export_epoch`

Builds a CSV export for reconciliation.

**Parameters**

```json
{ "epoch": 199 }
```

**Response**

```json
{
  "epoch": 199,
  "csvBase64": "YWRkcmVzcyxhbW91bnQsY2xhaW1lZCxjbGFpbWVkQXQsbW9kZQpu...",
  "totalPaid": "1770000000000000000000",
  "winners": 2
}
```

The decoded CSV contains the columns `address,amount,claimed,claimedAt,mode` (claimedAt is a Unix timestamp). The file is
sorted in winner order.

## CLI Commands

The CLI surfaces helper commands under `nhb-cli potso reward`:

* `nhb-cli potso reward claim` (retired: it reports that `potso_reward_claim` is retired and exits non-zero)
* `nhb-cli potso reward history --addr nhb1... [--cursor N] [--limit M]`
* `nhb-cli potso reward export --epoch 199 > rewards-199.csv`

`history` returns the raw JSON payload. `export` streams the decoded CSV bytes to STDOUT, making shell redirects
straightforward.

## OpenAPI Fragment

```yaml
paths:
  /rpc:
    post:
      summary: POTSO reward RPC
      description: JSON-RPC entry point for POTSO reward settlement methods.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                jsonrpc:
                  type: string
                  example: "2.0"
                method:
                  type: string
                  enum: [potso_rewards_history, potso_export_epoch]
                params:
                  type: array
                  items:
                    type: object
                id:
                  type: integer
      responses:
        '200':
          description: JSON-RPC success envelope
        '4XX':
          description: JSON-RPC error envelope
```

For a complete schema include the method-specific parameter/response shapes described above and the shared error envelopes
defined in `docs/openapi`.
