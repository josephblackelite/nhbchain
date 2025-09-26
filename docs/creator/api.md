# Creator Module API

The module is exposed through authenticated JSON-RPC endpoints. All calls require the standard RPC auth token and accept one JSON object as the sole parameter.

## `creator_publish`

Registers new content under the caller’s address.

```jsonc
// request
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "creator_publish",
  "params": [
    {
      "caller": "nhb1...",
      "contentId": "beatdrop-001",
      "uri": "ipfs://Qm...",
      "metadata": "{\"title\":\"Friday Drop\"}"
    }
  ]
}
```

```jsonc
// response
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "id": "beatdrop-001",
    "creator": "nhb1...",
    "uri": "ipfs://Qm...",
    "metadata": "{\"title\":\"Friday Drop\"}",
    "publishedAt": 1712083200,
    "totalTips": "0",
    "totalStake": "0"
  }
}
```

## `creator_tip`

Transfers NHB from the caller to the target creator and credits their payout ledger.

Parameters:

| Field | Type | Notes |
| --- | --- | --- |
| `caller` | string | Fan address (Bech32). |
| `contentId` | string | Previously published ID. |
| `amount` | string | Decimal NHB amount in wei. |

Response body includes the latest pending and lifetime payout tallies.

## `creator_stake`

Locks NHB behind a creator. The engine mints staking yield into the payout ledger using the configured basis points rate. The response returns the updated stake position, minted reward, and ledger snapshot.

## `creator_unstake`

Unlocks a fan’s stake and returns the funds to their liquid balance. Any remaining position is echoed back so clients can update UI state.

## `creator_payouts`

Inspects or claims the creator’s payout ledger.

Parameters:

| Field | Type | Notes |
| --- | --- | --- |
| `caller` | string | Creator address. |
| `claim` | bool | Optional. When true the pending amount is credited to the creator account and zeroed in the ledger. |

Response example:

```jsonc
{
  "pending": "4200000000000000000",
  "totalTips": "8400000000000000000",
  "totalYield": "210000000000000000",
  "lastPayout": 1712087200,
  "claimed": "4200000000000000000"
}
```

## Error Semantics

All creator endpoints return `-32602` (`codeInvalidParams`) for validation failures such as malformed addresses, empty content IDs, or non-positive amounts. Runtime issues (e.g. insufficient balance) are also surfaced as `codeInvalidParams` with a descriptive message to keep client logic simple.

## Event Consumption

Every RPC action emits one or more events accessible through the node’s event log stream:

- Publish → `creator.content.published`
- Tip → `creator.content.tipped`, `creator.payout.accrued`
- Stake → `creator.fan.staked`, `creator.payout.accrued`
- Unstake → `creator.fan.unstaked`
- Claim → `creator.payout.accrued`

Indexers should subscribe to these types to power discovery views, feed the `/examples/creator-studio` UI, and surface real-time insights for devnet demos.
