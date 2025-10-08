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
