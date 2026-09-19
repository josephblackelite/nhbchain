# JSON-RPC reference (core methods)

This page covers the transport rules and the core account/transaction/staking
methods of the node's JSON-RPC server (`rpc/http.go`). Module-specific methods
(escrow, identity, loyalty, lending, ...) are documented with their modules.

Returns a transaction summary with the asset inferred from the type. ZapNHB
(transfer type `TransferZNHB`) responses will include `"asset": "ZNHB"` so
explorers and wallets can distinguish token flows without re-simulating the
payload. `nhb_getTransaction` needs no token. Methods that mutate state or expose
privileged data require the bearer token in the `Authorization` header;
`nhb_sendTransaction` is one of them (see the
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md#authenticated-submission)
guide for full header context).

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

## Sending ZNHB via `nhb_sendTransaction`

Wallet integrations submit signed ZNHB transfers through the privileged
`nhb_sendTransaction` RPC using the `TransferZNHB (0x10)` transaction type. Fetch
the next nonce from `nhb_getBalance` before signing so the payload aligns with
validator expectations. The `NHB_RPC_TOKEN` referenced in the examples below must
be a short-lived JWT issued by your infrastructure with the issuer/audience
configured under `RPCJWT`; refresh the token before it expires so the server
accepts the request. `nhb_getBalance` itself does not require the token, so the
header in the first request below is optional:

```jsonc
// Request
// Authorization: Bearer <NHB_RPC_TOKEN>
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "nhb_getBalance",
  "params": ["nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"]
}

// Response
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "address": "nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
    "balanceNHB": 10000000000000000,
    "balanceZNHB": 2500000000000000000,
    "nonce": 42
  }
}
```

With the nonce in hand, sign the envelope and forward the full JSON-RPC request
from trusted infrastructure. Attach `Authorization: Bearer <NHB_RPC_TOKEN>` to
the HTTP headers (see the
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md#authenticated-submission)
walkthrough for the complete header list). The RPC layer enforces the bearer
token via `requireAuth` in `rpc/http.go`, which rejects requests missing the
header or using the wrong scheme. `to` and `data` are byte fields, so they are
sent as base64 strings (the `to` below is the 20-byte address
`0x5c9d4cde23f68cd2209a2f5eaf0a1d34ac3e5f2a` in base64); the numeric fields
accept a number, a decimal string or a `0x` hex string.

```bash
curl https://validator.nhbchain.example/rpc \
  -H "Authorization: Bearer ${NHB_RPC_TOKEN}" \
  -H "Content-Type: application/json" \
  --data '{
    "id": 2,
    "jsonrpc": "2.0",
    "method": "nhb_sendTransaction",
    "params": [
      {
        "chainId": "0x4e4842",
        "type": 16,
        "nonce": 42,
        "to": "XJ1M3iP2jNIgmi9erwodNKw+Xyo=",
        "value": "0xde0b6b3a7640000",
        "gasLimit": "0x61a8",
        "gasPrice": "0x1",
        "data": "",
        "r": "<signature r>",
        "s": "<signature s>",
        "v": "<signature v>"
      }
    ]
  }'
```

The same request as a JSON-RPC body. `r`, `s` and `v` are placeholders: the
signature must be produced over the transaction hash described in the
[wallet builder guide](../sdk/wallets.md).

```json
// Authorization: Bearer <NHB_RPC_TOKEN>
{
  "id": 2,
  "jsonrpc": "2.0",
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": "0x4e4842",
      "type": 16,
      "nonce": 42,
      "to": "XJ1M3iP2jNIgmi9erwodNKw+Xyo=",
      "value": "0xde0b6b3a7640000",
      "gasLimit": "0x61a8",
      "gasPrice": "0x1",
      "data": "",
      "r": "<signature r>",
      "s": "<signature s>",
      "v": "<signature v>"
    }
  ]
}
```

`to` above is the base64 form of the 20-byte address
`5c9d4cde23f68cd2209a2f5eaf0a1d34ac3e5f2a`. The `r`/`s`/`v` values must be a real
signature over the hash of exactly these fields. See
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md).

## `nhb_getBalance`

Two read-only staking methods are enabled, `stake_previewClaim` and
`stake_getPosition`. Both require the standard bearer token in the
`Authorization` header. Calls are rate limited using the same per-source window
that guards transaction submission and are rejected with HTTP `429` and the
`staking rate limit exceeded` message once the limit is hit. If the staking
module is paused the methods return HTTP `503`, code `-32050` and the
`staking module paused` message. The reference is
[`docs/staking/staking.md`](../staking/staking.md).

### `stake_previewClaim`

Returns the rewards payable now for the supplied delegator alongside the
timestamp of the next payout.

```json
// Authorization: Bearer <NHB_RPC_TOKEN>
{
  "id": 3,
  "jsonrpc": "2.0",
  "method": "stake_previewClaim",
  "params": ["nhb1exampledelegator…"]
}
```

```json
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "address": "nhb1...",
    "balanceNHB": 10000000000000000,
    "balanceZNHB": 2500000000000000000,
    "stake": 0,
    "lockedZNHB": 0,
    "pendingStakingRewards": 0,
    "username": "",
    "nonce": 42,
    "engagementScore": 0,
    "validatorRegistered": false
  }
}
```

If no full payout period has elapsed since the last payout, `payable` is `"0"`.

## `nhb_getTransaction`

Returns the delegator's reward accounting fields (`StakeShares`,
`StakeLastIndex`, `StakeLastPayoutTs`).

```json
{
  "hash": "0x...",
  "type": "TransferZNHB",
  "asset": "ZNHB",
  "blockHash": "0x...",
  "blockNumber": "0x1a",
  "from": "nhb1...",
  "to": "nhb1...",
  "value": "0xde0b6b3a7640000",
  "nonce": "0x2a",
  "gasLimit": "0x61a8",
  "gasPrice": "0x3b9aca00",
  "input": "0x"
}
```

`type` is the name from `formatTxType`; `asset` is set by `assetLabel` (`NHB` for
`Transfer` and the NHB lending types, `ZNHB` for `TransferZNHB`, the ZNHB lending
types and `BuyZNHB`, and `NHB` for `RedeemNHB`; it is omitted for every other type).

### Disabled methods: `stake_claimRewards`, `stake_delegate`, `stake_undelegate`, `stake_claim`

These four methods no longer do anything. Every call is answered with HTTP `410
Gone`, JSON-RPC code `-32060` (`codeMethodDisabled`) and a message telling the
caller to sign a `TxTypeStake`, `TxTypeUnstake`, `TxTypeStakeClaim` or
`TxTypeStakeClaimRewards` transaction and submit it with `nhb_sendTransaction`
(`rpc/stake_handlers.go`). In particular, `stake_claimRewards` no longer returns
`minted`, `periods`, `aprBps` or `nextEligibleTs`, and there is no `409` response
for a claim that is not yet due from this method. Rewards are claimed with a
`TxTypeStakeClaimRewards` (`0x34`) transaction; see
[`docs/staking/staking.md`](../staking/staking.md).
