# JSON-RPC Highlights

## `nhb_getTransaction`

Returns a transaction summary with the asset inferred from the type. ZapNHB
(transfer type `TransferZNHB`) responses will include `"asset": "ZNHB"` so
explorers and wallets can distinguish token flows without re-simulating the
payload.

```json
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "hash": "0xabc123…",
    "type": "TransferZNHB",
    "asset": "ZNHB",
    "from": "nhb1…",
    "to": "nhb1…",
    "value": "0xde0b6b3a7640000"
  }
}
```

## `nhb_getTransactionReceipt`

Receipts now surface the asset for transfer logs and fee events so downstream
systems can render them unambiguously.

```json
{
  "id": 2,
  "jsonrpc": "2.0",
  "result": {
    "transactionHash": "0xabc123…",
    "status": "0x1",
    "logs": [
      {
        "event": "Transfer",
        "asset": "ZNHB",
        "from": "nhb1…",
        "to": "nhb1…",
        "value": "0xde0b6b3a7640000"
      },
      {
        "event": "FeeApplied",
        "asset": "NHB",
        "payer": "0x7f…",
        "fee": "0x38d7ea4c68000"
      }
    ]
  }
}
```

## Staking helpers

The staking surface now exposes read-only previews and a reward claim helper.
All three methods require the standard bearer token in the `Authorization`
header. Calls are rate limited using the same per-source window that guards
transaction submission and will reject requests with HTTP `429` and the
`staking rate limit exceeded` message once the limit is hit. If governance
pauses the staking module the methods return HTTP `503` with the
`staking module paused` error payload.

### `stake_previewClaim`

Returns the rewards currently payable for the supplied delegator alongside the
timestamp of the next eligible payout window.

```json
{
  "id": 3,
  "jsonrpc": "2.0",
  "method": "stake_previewClaim",
  "params": ["nhb1exampledelegator…"]
}
```

```json
{
  "id": 3,
  "jsonrpc": "2.0",
  "result": {
    "payable": "7425000000000000000000",
    "nextPayoutTs": 1719969600
  }
}
```

If the payout window has not elapsed the method returns a zero `payable` value
and the timestamp of the next payout window. When the module is paused the
response mirrors the claim helper below.

### `stake_getPosition`

Exposes the delegator’s current staking ledger snapshot so operators can check
shares, reward index, and payout timing without inspecting raw account state.

```json
{
  "id": 4,
  "jsonrpc": "2.0",
  "method": "stake_getPosition",
  "params": ["nhb1exampledelegator…"]
}
```

```json
{
  "id": 4,
  "jsonrpc": "2.0",
  "result": {
    "shares": "5000000000000000000",
    "lastIndex": "1500",
    "lastPayoutTs": 1717387200
  }
}
```

### `stake_claimRewards`

Claims accrued staking rewards and returns the total paid amount, the number of
reward periods settled, and the timestamp when the next payout becomes
available.

```json
{
  "id": 5,
  "jsonrpc": "2.0",
  "method": "stake_claimRewards",
  "params": ["nhb1exampledelegator…"]
}
```

```json
{
  "id": 5,
  "jsonrpc": "2.0",
  "result": {
    "paid": "7425000000000000000000",
    "periods": 2,
    "next_eligible": 1722561600
  }
}
```

Attempting to claim before the payout window elapses yields a `409` response
with the `stake: claim not yet due` message and a `next_eligible` hint in the
error `data` field. For example:

```json
{
  "id": 5,
  "jsonrpc": "2.0",
  "error": {
    "code": -32602,
    "message": "stake: claim not yet due",
    "data": {
      "next_eligible": 1719979200
    }
  }
}
```

When the module is paused the helper returns HTTP `503` with the `staking
module paused` message.

Error responses include:

* `503 Service Unavailable` with JSON-RPC code `-32050` (`codeModulePaused`) and
  the `staking module paused` message when governance pauses staking.
* `409 Conflict` with JSON-RPC code `-32602` (`codeInvalidParams`) and the
  `stake: claim not yet due` message when the payout window has not elapsed. The
  response includes a `next_eligible` hint in the error `data` field.
* `501 Not Implemented` with JSON-RPC code `-32000` (`codeServerError`) and the
  `staking not ready` message while rewards are still being enabled on the
  network.
* `400 Bad Request` with JSON-RPC code `-32602` (`codeInvalidParams`) and the
  `failed to claim staking rewards` message for malformed parameters or other
  validation failures.
