# Structured Event Reference

The node emits structured events alongside each successful transaction so indexers and
explorers can build real-time feeds without replaying the full EVM trace. This document
covers the native transfer feed along with the enriched staking lifecycle events.

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

## `stake.delegated`

The staking module emits `stake.delegated` whenever a delegator locks additional stake or
when the validator's position accrues reward shares. Events are published both for the
delegator and the validator so downstream indexers can reconcile share balances without
replaying the reward engine.

### Attributes

| Attribute | Type | Description |
| --- | --- | --- |
| `addr` | `string` | Bech32 NHB address whose stake shares were updated. |
| `sharesAdded` | `string` | Decimal string for the net increase in stake shares triggered by the delegation. |
| `newShares` | `string` | Post-transition share balance for the account. |
| `lastIndex` | `string` | Reward index checkpoint applied to the account after accrual. |
| `validator` | `string` | Validator address receiving the delegation. Present for delegator and validator events. |
| `amount` | `string` | Stake amount locked during the transition (wei). |
| `locked` | `string` | Delegator's total locked stake after the delegation. Only present on the delegator event. |

### Sample transaction receipt

```json
{
  "logs": [
    {
      "type": "stake.delegated",
      "attributes": {
        "addr": "nhb1x0d2w5jv0x3eh9h9yfpz9lfzjef8f7a3a6t6hm",
        "sharesAdded": "1200000000000000000",
        "newShares": "8500000000000000000",
        "lastIndex": "1003125000000000000",
        "validator": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "amount": "500000000000000000",
        "locked": "3500000000000000000"
      }
    },
    {
      "type": "stake.delegated",
      "attributes": {
        "addr": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "sharesAdded": "0",
        "newShares": "43000000000000000000",
        "lastIndex": "1003125000000000000",
        "validator": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "amount": "500000000000000000"
      }
    }
  ]
}
```

## `stake.undelegated`

When a delegator begins the unbonding process the protocol accrues outstanding reward shares
and emits `stake.undelegated` events for both parties. The payload mirrors `stake.delegated`
while also reporting the unbond release metadata.

### Attributes

| Attribute | Type | Description |
| --- | --- | --- |
| `addr` | `string` | Account address whose shares were checkpointed prior to unbonding. |
| `sharesRemoved` | `string` | Decimal string for the reduction in share balance (or `"0"` if only accrual occurred). |
| `newShares` | `string` | Updated share balance after the checkpoint. |
| `lastIndex` | `string` | Reward index applied during the transition. |
| `validator` | `string` | Validator address whose delegation was reduced. |
| `amount` | `string` | Amount scheduled to unbond (wei). |
| `releaseTime` | `string` | Unix timestamp when the unbond entry matures. Present on delegator events. |
| `unbondingId` | `string` | Unique identifier for the pending unbond entry. Present on delegator events. |

### Sample transaction receipt

```json
{
  "logs": [
    {
      "type": "stake.undelegated",
      "attributes": {
        "addr": "nhb1x0d2w5jv0x3eh9h9yfpz9lfzjef8f7a3a6t6hm",
        "sharesRemoved": "0",
        "newShares": "9100000000000000000",
        "lastIndex": "1012250000000000000",
        "validator": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "amount": "400000000000000000",
        "releaseTime": "1710028800",
        "unbondingId": "3"
      }
    },
    {
      "type": "stake.undelegated",
      "attributes": {
        "addr": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "sharesRemoved": "0",
        "newShares": "43800000000000000000",
        "lastIndex": "1012250000000000000",
        "validator": "nhb1vvcc0l3nqq6kfgynzxumv0y6s4am55l23evl7l",
        "amount": "400000000000000000"
      }
    }
  ]
}
```

## `stake.rewardsClaimed`

Claiming staking rewards mints ZNHB to the delegator, checkpoints the share index, and
increments the year-to-date emission counter. The node emits two receipts: the canonical
`stake.rewardsClaimed` event and a legacy alias `stake.claimed` with identical attributes for
backwards compatibility.

### Attributes

| Attribute | Type | Description |
| --- | --- | --- |
| `addr` | `string` | Delegator address receiving the minted rewards. |
| `minted` | `string` | Amount of ZNHB minted to the delegator (wei). |
| `periods` | `string` | Number of payout periods included in the claim window. |
| `lastIndex` | `string` | Reward index checkpoint after the claim. |
| `emissionYTD` | `string` | Calendar-year cumulative staking emission after minting. |
| `shares` | `string` | Current stake share balance for the delegator. |

### Sample transaction receipt

```json
{
  "logs": [
    {
      "type": "stake.rewardsClaimed",
      "attributes": {
        "addr": "nhb1x0d2w5jv0x3eh9h9yfpz9lfzjef8f7a3a6t6hm",
        "minted": "185000000000000000",
        "periods": "3",
        "lastIndex": "1034125000000000000",
        "emissionYTD": "185000000000000000",
        "shares": "9100000000000000000"
      }
    },
    {
      "type": "stake.claimed",
      "attributes": {
        "addr": "nhb1x0d2w5jv0x3eh9h9yfpz9lfzjef8f7a3a6t6hm",
        "minted": "185000000000000000",
        "periods": "3",
        "lastIndex": "1034125000000000000",
        "emissionYTD": "185000000000000000",
        "shares": "9100000000000000000"
      }
    }
  ]
}
```

Consumers can treat the alias event as a drop-in replacement for legacy pipelines while new
systems should subscribe to `stake.rewardsClaimed` going forward.
