# Structured Event Reference

The node emits structured events alongside each successful transaction so indexers and
explorers can build real-time feeds without replaying the full EVM trace. This document
covers the new `transfer.native` event emitted for NHB and ZNHB balance movements.

## `transfer.native`

Every native transfer (both NHB and ZNHB) now publishes a single event describing the
asset that moved, the participants, and the transaction hash. The payload allows data
pipelines to distinguish between on-chain and off-ledger balances without additional
lookups.

### Attributes

| Attribute | Type | Description |
| --- | --- | --- |
| `asset` | `string` | Asset ticker for the balance movement (`NHB` or `ZNHB`). |
| `from` | `string` | Bech32 NHB address that debited the funds. |
| `to` | `string` | Bech32 NHB address that received the funds. |
| `amount` | `string` | Integer amount in wei for the transfer. |
| `txHash` | `string` | Hex-encoded transaction hash prefixed with `0x`. |

### Sample transaction receipt

```json
{
  "transactionHash": "0x98b4fca3b8e3c6f734944c6c287f66f724d9c917edc1818dc3f028de5a1a6a11",
  "status": "0x1",
  "logs": [
    {
      "type": "transfer.native",
      "attributes": {
        "asset": "ZNHB",
        "from": "nhb1md22g3r44mdjawcgq2fcl4h33y7quh3kn82w9s",
        "to": "nhb1s6w6aw2q7cl30c5p6q4pw6l8qaf3e8p0j6cdk3",
        "amount": "500000000000000000",
        "txHash": "0x98b4fca3b8e3c6f734944c6c287f66f724d9c917edc1818dc3f028de5a1a6a11"
      }
    }
  ]
}
```

The same schema is emitted for NHB transfers with `"asset": "NHB"`. Consumers can rely on
this attribute to direct events into the appropriate settlement pipeline.
