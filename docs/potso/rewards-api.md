# POTSO Rewards API Reference

JSON-RPC 2.0 methods for reading reward results and settling claim-mode rewards. Handlers are in `rpc/potso_reward_handlers.go`; routing is in `rpc/http.go`. Epoch-level methods (`potso_epoch_info`, `potso_epoch_payouts`, `potso_rewards_outflow`, including the `potso_rewards_outflow` result fields) are described in [potso_rewards.md](../potso_rewards.md#potso_rewards_outflow). Payout modes are in [rewards-modes.md](rewards-modes.md).

Every parameter list is a single JSON object: `"params": [ { ... } ]`. Amounts are decimal wei strings. Addresses are Bech32 (`nhb1...`).

## `potso_reward_claim`

Settles a claim-mode reward. **Requires authentication** (JWT bearer token or verified client certificate; otherwise HTTP 401) **and** a signature by the winning address.

```json
{ "epoch": 123, "address": "nhb1examplewinner...", "signature": "0x..." }
```

`address` and `signature` are required. The signature is a 65-byte secp256k1 signature (hex, `0x` optional) over `SHA-256("potso_reward_claim|<epoch>|<lowercase address>")`. The address recovered from it must equal `address`.

Response:

```json
{ "paid": true, "amount": "899000000000000000000" }
```

`paid` is `false`, with the amount still returned, if the reward was already settled. Errors:

| Condition | HTTP status | JSON-RPC code | Message |
| --- | --- | --- | --- |
| Missing address/signature, bad address, bad hex, signature not 65 bytes, signature does not match address | 400 | `-32602` | `address and signature are required`, `invalid address`, `invalid signature`, `signature must be 65 bytes`, `signature does not match address` |
| No claim record for `(epoch, address)` | 404 | `-32000` | `reward not found` |
| Node is not in claim mode | 400 | `-32602` | `claiming disabled` |
| Treasury balance too low | 409 | `-32000` | `INSUFFICIENT_TREASURY` (claim stays pending) |
| Node module paused or other failure | 500 | `-32000` | `failed to claim reward` |

## `potso_rewards_history`

No authentication. Settled payouts for an address, newest first.

```json
{ "address": "nhb1examplewinner...", "cursor": "2", "limit": 2 }
```

`address` is required. `cursor` is an optional zero-based offset as a decimal string (a non-numeric or negative value returns an error). `limit` is optional; values `<= 0` mean 50.

```json
{
  "address": "nhb1examplewinner...",
  "entries": [
    { "epoch": 200, "amount": "850000000000000000000", "mode": "auto" },
    { "epoch": 199, "amount": "920000000000000000000", "mode": "claim" }
  ],
  "nextCursor": "4"
}
```

`nextCursor` is omitted when there are no further entries. Only settled payouts appear: auto payouts with a positive amount, and claim-mode payouts after the claim succeeds.

## `potso_export_epoch`

No authentication. Builds the payout ledger of one epoch as CSV.

```json
{ "epoch": 199 }
```

```json
{
  "epoch": 199,
  "csvBase64": "<base64 of the CSV>",
  "totalPaid": "1770000000000000000000",
  "winners": 2
}
```

`totalPaid` is the sum of the payout amounts of the winners, and `winners` is their count. The decoded CSV has the header and columns

```
address,amount,claimed,claimedAt,mode
```

- `address`: Bech32 address.
- `amount`: decimal wei.
- `claimed`: `true` or `false`.
- `claimedAt`: Unix seconds; `0` for an unclaimed entry.
- `mode`: `auto` or `claim`.

Rows are in winner order (highest weight first). An epoch with no stored winners returns only the header. There is no checksum field; `address` plus `epoch` identifies a row.

## Accounting notes

- Per-address reconciliation: page through `potso_rewards_history` until `nextCursor` is absent.
- Per-epoch reconciliation: `potso_export_epoch`, or `potso_epoch_payouts` for JSON.
- Repeated `potso_reward_claim` calls for a settled reward return `paid: false` with the amount, not an error.
- The node emits the reward events in [notifications.md](notifications.md). No webhook or push delivery exists in the node.

## CLI

```bash
nhb-cli potso reward claim   --epoch 199 --addr nhb1... [--key wallet.key]
nhb-cli potso reward history --addr nhb1... [--cursor N] [--limit M]
nhb-cli potso reward export  --epoch 199 > rewards-199.csv
```

- `claim` signs the digest with the key file and calls `potso_reward_claim` with `NHB_RPC_TOKEN` as the bearer token; it fails if `NHB_RPC_TOKEN` is unset or if the key's address differs from `--addr`.
- `--epoch` must be non-zero for `claim` and `export`, so epoch 0 cannot be claimed or exported with the CLI.
- `history` prints the raw JSON result. `export` writes the decoded CSV bytes to stdout.

## OpenAPI fragment

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
