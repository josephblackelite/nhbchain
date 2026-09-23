# POTSO Rewards API Reference

JSON-RPC 2.0 methods for reading reward results. Handlers are in `rpc/potso_reward_handlers.go`; routing is in `rpc/http.go`. Epoch-level methods (`potso_epoch_info`, `potso_epoch_payouts`, `potso_rewards_outflow`, including the `potso_rewards_outflow` result fields) are described in [potso_rewards.md](../potso_rewards.md#potso_rewards_outflow). Payout modes are in [rewards-modes.md](rewards-modes.md).

Every parameter list is a single JSON object: `"params": [ { ... } ]`. Amounts are decimal wei strings. Addresses are Bech32 (`nhb1...`).

## `potso_reward_claim` (retired)

The method is still routed, but the handler always answers HTTP 410 with JSON-RPC code `-32060` and a message saying the method is disabled (`handlePotsoRewardClaim`, `potsoRewardClaimRPCDisabledMessage` in `rpc/potso_reward_handlers.go`; `codeMethodDisabled` in `rpc/http.go`). It performs no authentication and reads no parameters. The reason given in the code comment is that the old implementation debited the treasury and credited the claimant on the live state of the one validator that handled the call, outside block execution, so validators could disagree about the next block.

Consequence: a reward recorded in claim mode cannot be collected on a running node (see [rewards-modes.md](rewards-modes.md)). Auto mode, the mode in the repository's configuration files, needs no claim.

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
    { "epoch": 199, "amount": "920000000000000000000", "mode": "auto" }
  ],
  "nextCursor": "4"
}
```

`nextCursor` is omitted when there are no further entries. Only settled payouts appear: auto payouts with a positive amount. A claim-mode payout would be added only by `Node.PotsoRewardClaim`, which no RPC reaches, so claim-mode epochs add no history entries.

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
- The node emits the reward events in [notifications.md](notifications.md). No webhook or push delivery exists in the node.

## CLI

```bash
nhb-cli potso reward history --addr nhb1... [--cursor N] [--limit M]
nhb-cli potso reward export  --epoch 199 > rewards-199.csv
```

- `nhb-cli potso reward claim` is retired: it prints an error that the node no longer serves `potso_reward_claim` and exits 1 without contacting the node (`reportRetired`, `cmd/nhb-cli/retired_cmd.go`).
- `--epoch` must be non-zero for `export`, so epoch 0 cannot be exported with the CLI.
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
