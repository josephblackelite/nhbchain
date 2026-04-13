# POTSO Leaderboard API

The leaderboard surfaces the deterministic ordering produced by the POTSO
composite weighting pipeline. Two public JSON-RPC methods expose this state.

## Methods

### `potso_leaderboard`

Returns the ordered winners for a specific epoch. Parameters are supplied as an
object:

- `epoch` (optional `uint64`): Epoch to query. When omitted or `0`, the handler
  falls back to the latest processed epoch.
- `offset` (optional `int`): Zero-based offset for pagination. Defaults to `0`.
- `limit` (optional `int`): Maximum number of entries to return. `0` or omitted
  means “no limit”.

Example request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "potso_leaderboard",
  "params": [{"epoch": 42, "offset": 0, "limit": 2}]
}
```

Example response:

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
      },
      {
        "addr": "nhb1qqnw0pr5v92l8j2a2gyx9zgj9z87q5n8p5jd9s",
        "weightBps": 4787,
        "stakeShareBps": 3400,
        "engShareBps": 6433
      }
    ]
  }
}
```

The `total` field always reflects the full number of stored winners prior to
pagination. Ordering is guaranteed across nodes because the weighting pipeline
applies deterministic tie-breakers (`addrLex` or `addrHash`).

### `potso_params`

Returns the currently active `[potso.weights]` configuration. Example request:

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "potso_params",
  "params": []
}
```

Example response:

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": {
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
}
```

## OpenAPI Fragment

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

## Deterministic Tie Break

When participants share the same composite weight, the RPC reflects the
underlying ordering produced by the weighting pipeline:

- `addrLex` sorts by the 20-byte address (byte-wise ascending).
- `addrHash` computes a SHA-256 digest of the address and sorts by the digest.

Because every node uses identical data, pagination windows (`offset`, `limit`)
are stable across runs and between replicas.

