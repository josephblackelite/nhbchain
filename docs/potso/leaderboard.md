# POTSO Leaderboard API

These methods expose the weight snapshot that `processPotsoRewardEpoch` stores for each processed reward epoch (see [weights.md](weights.md)). None of them require authentication. They return data only for epochs that have been processed; with rewards disabled ([config.md](config.md)) no snapshots exist.

## `potso_leaderboard`

Returns the ranked entries of an epoch snapshot. Parameters are an optional single object:

- `epoch` (optional `uint64`): epoch to query. When omitted or `0`, the last processed epoch is used.
- `offset` (optional `int`): zero-based offset. Negative values are treated as `0`.
- `limit` (optional `int`): maximum entries. `0`, omitted or negative means no limit.

Request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "potso_leaderboard",
  "params": [{"epoch": 42, "offset": 0, "limit": 2}]
}
```

Response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "epoch": 42,
    "total": 3,
    "items": [
      {
        "addr": "nhb1qqp8d9t5u4z0h8ce0f9f4d60h0jz2w6h5a3w4k",
        "weightBps": 5123,
        "stakeShareBps": 6600,
        "engShareBps": 3567
      }
    ]
  }
}
```

- `total` is the number of entries in the stored snapshot before pagination. That is the ranked candidate list after the `TopKWinners` cut. It is not the number of paid winners, which can be lower because of `MaxWinnersPerEpoch`, `MinPayoutWei` and the share cap. Use `potso_epoch_info` / `potso_epoch_payouts` for winners ([rewards-api.md](rewards-api.md)).
- If no epoch has been processed, or the requested epoch has no snapshot, the result has `total: 0` and an empty `items` (with `epoch` `0` or the requested epoch respectively).
- `weightBps`, `stakeShareBps` and `engShareBps` are `floor(share * 10000)`; they can sum to slightly under 10000.
- Order is weight descending, with the tie break from `[potso.weights].TieBreak` in force when the snapshot was computed.

Source: `handlePotsoLeaderboard` in `rpc/potso_query_handlers.go`, `Node.PotsoLeaderboard` in `core/node.go`.

## `potso_getWeight`

Returns the voting power basis-points figure that governance vote casting would use.

Params: `[{"epoch": 42, "address": "nhb1..."}]`. `address` is required; `epoch` is optional (`0`/omitted = last processed epoch).

Result: `{"epoch": 42, "address": "nhb1...", "weightBps": 5123}`. An address with no entry, or an epoch with no snapshot, returns `weightBps: 0` rather than an error. It reads the `snapshots/potso/<epoch>/weights` snapshot (`Node.PotsoWeight`), the same one `CastVote` reads.

## `potso_params`

Returns the running `[potso.weights]` values. Params: `[]`.

```json
{
  "alphaStakeBps": 7000,
  "txWeightBps": 6000,
  "escrowWeightBps": 3000,
  "uptimeWeightBps": 1000,
  "maxEngagementPerEpoch": 1000,
  "minStakeToWinWei": "0",
  "minEngagementToWin": 0,
  "decayHalfLifeEpochs": 7,
  "topKWinners": 5000,
  "tieBreak": "addrHash"
}
```

The values shown are the defaults. The result contains only these ten fields (`potsoParamsResult`). `MinStakeToEarnWei`, the dampening parameters, `MaxUserShareBps` and the `[potso.rewards]` values are not included.

## OpenAPI fragment

```yaml
paths:
  /rpc:
    post:
      summary: POTSO leaderboard and parameter methods
      requestBody:
        required: true
        content:
          application/json:
            schema:
              oneOf:
                - $ref: '#/components/schemas/PotsoLeaderboardRequest'
                - $ref: '#/components/schemas/PotsoParamsRequest'
      responses:
        '200':
          description: JSON-RPC success envelope
components:
  schemas:
    PotsoLeaderboardRequest:
      type: object
      required: [jsonrpc, id, method]
      properties:
        jsonrpc:
          type: string
          enum: ['2.0']
        id:
          type: integer
        method:
          type: string
          enum: ['potso_leaderboard']
        params:
          type: array
          maxItems: 1
          items:
            type: object
            properties:
              epoch:
                type: integer
                format: uint64
              offset:
                type: integer
              limit:
                type: integer
    PotsoParamsRequest:
      type: object
      required: [jsonrpc, id, method, params]
      properties:
        jsonrpc:
          type: string
          enum: ['2.0']
        id:
          type: integer
        method:
          type: string
          enum: ['potso_params']
        params:
          type: array
          maxItems: 0
```

## Tie break

Entries with equal weight are ordered by the tie-break key, ascending: `addrLex` is the 20-byte address, `addrHash` is the SHA-256 digest of the address. Because the order is stored in the snapshot, `offset` / `limit` windows are stable.
