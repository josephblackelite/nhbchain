# JSON-RPC reference (core methods)

This page covers the transport rules and the core account/transaction/staking
methods of the node's JSON-RPC server (`rpc/http.go`). Module-specific methods
(escrow, identity, loyalty, lending, ...) are documented with their modules.

## Transport

* `POST` a single JSON object to the node's RPC address (`RPCAddress` in
  `config.toml`; the code default is `:8080`, the repository `config.toml` sets
  `127.0.0.1:8545`). Batch arrays are not supported: the body is decoded into
  one request object (`RPCRequest` in `rpc/http.go`).
* Request shape: `{"jsonrpc":"2.0","id":1,"method":"...","params":[...]}`.
  `jsonrpc` may be omitted but must be `"2.0"` if present. `id` is decoded as a
  Go `int`, so it must be a JSON integer. `params` is always a JSON array.
* Maximum request body: 1 MiB (`maxRequestBytes`).
* Errors are JSON-RPC error objects returned with a non-200 HTTP status
  (`writeError`). For 5xx responses and `-32000` errors the `data` field is
  removed (`shouldScrubErrorDetails`).
* `X-Forwarded-For` is honoured only when the peer address is listed in
  `RPCTrustedProxies` and `RPCTrustProxyHeaders = true`. `RPCAllowlistCIDRs`
  restricts which client addresses may call the server at all.

### Authentication

Methods marked "auth" below call `requireAuth`: the request must carry
`Authorization: Bearer <JWT>` (verified against the `RPCJWT` settings), or, when
the server requires client certificates, present a verified client certificate.
Failures return HTTP 401 with code `-32001`. The messages come from
`rpc/http.go`: `missing Authorization header`, `Authorization header must use
Bearer scheme`, `invalid JWT`, `JWT authentication not configured`.

Methods that need auth include: `nhb_sendTransaction`, `tx_setSponsorshipEnabled`,
`nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`,
`nhb_getSwapStatus`, `nhb_checkSwapAllowance`, `swap_limits`,
`swap_provider_status`, `swap_burn_list`, `swap_voucher_reverse`,
`swap_markReconciled`, `swap_setManualQuote`, `swap_listPendingRedemptions`,
`buyback_submitRefPrice`, `lending_submitRefPrice`, `pos_sweepVoids`,
`engagement_register_device`, `engagement_submit_heartbeat`, `potso_stake_info`,
`potso_reward_claim`, `stake_getPosition`, `stake_previewClaim`.

### Error codes

| Code | Constant | Typical HTTP status |
| --- | --- | --- |
| -32700 | `codeParseError` | 400 |
| -32600 | `codeInvalidRequest` | 400 |
| -32601 | `codeMethodNotFound` | 404 |
| -32602 | `codeInvalidParams` | 400 (some handlers use 404/409) |
| -32000 | `codeServerError` | 500/503 |
| -32001 | `codeUnauthorized` | 401/403 |
| -32010 | `codeDuplicateTx` | |
| -32020 | `codeRateLimited` | 429 |
| -32030 | `codeMempoolFull` | 503 |
| -32040 | `codeInvalidPolicyInvariants` | |
| -32050 | `codeModulePaused` | 503 |
| -32060 | `codeMethodDisabled` | 410 |

Module handlers define additional codes (`-32021`..`-32025` escrow,
`-32041`..`-32045` claimable, and others in `rpc/*_handlers.go`). These codes are
reused across handlers, so a code alone does not identify the module:
`rpc/p2p_handlers.go` also uses `-32021`..`-32025`; `rpc/net_handlers.go` uses
`-32040` (invalid params), `-32041` (unknown peer) and `-32042` (peer banned),
overlapping `codeInvalidPolicyInvariants` and the claimable codes;
`rpc/sync_handlers.go` uses `-32060` (invalid params) and `-32061` (unavailable),
where `-32060` is also `codeMethodDisabled`. Use the method name and the error
message to tell them apart.

### Rate limits

Every method except `nhb_sendTransaction` is checked with `allowSource` before
dispatch; `nhb_sendTransaction` is checked after signature recovery, keyed also
by `chainId:nonce`. Buckets are per source IP, JWT subject, chain nonce and
identity+chain, per method. Defaults from `config/config.go`:
`RPCMaxTxPerWindow = 5` (and the same for the `PerIP`, `PerIdentity`, `PerChain`,
`PerIdentityChain` variants) per `RPCRateLimitWindow = 60` seconds; the repository
`config.toml` raises them to 120. `RPCRouteRateLimits` overrides limits per
method. A limited call returns HTTP 429, code `-32020` and message `RPC rate
limit exceeded` (or `transaction rate limit exceeded` for
`nhb_sendTransaction`). Hits are counted by `nhb_rpc_limiter_hits_total`.

## Transaction encoding

`nhb_sendTransaction` takes one parameter: a signed transaction object
(`types.Transaction`, `core/types/transaction.go`). The handler's DTO
(`handleSendTransaction`) accepts:

| Field | Encoding |
| --- | --- |
| `chainId` | integer, decimal string or `0x` hex string; must equal `0x4e4842` (`types.NHBChainID`) |
| `type` | integer transaction type (see [overview](../overview/README.md#transaction-types)) |
| `nonce`, `gasLimit`, `intentExpiry` | integer, decimal string or `0x` hex string (uint64) |
| `value`, `gasPrice`, `r`, `s`, `v` | integer, decimal string or `0x` hex string |
| `to`, `data`, `paymaster`, `intentRef` | **base64** strings (Go `[]byte` JSON encoding), not hex |
| `merchantAddr`, `deviceId`, `refundOf` | strings |
| `paymasterR`, `paymasterS`, `paymasterV` | as `r`, `s`, `v` |

`gasLimit` must be greater than zero and `gasPrice` greater than zero. `to` is
the 20-byte address. The signature is secp256k1 over the transaction hash (see
[signing](../transactions/signing.md)); `s` must be in the lower half of the curve
order and `v` is `27` or `28`.

The result is the transaction hash as a `0x`-prefixed string. An identical
transaction already known from the same sender returns the same hash.
Rejections: `invalid transaction` (`-32602`, 400), `mempool full` (`-32030`, 503),
`nonce N has already been used; current account nonce is M` (`-32602`, 400).

```json
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
      "gasPrice": "0x3b9aca00",
      "r": "0x<32-byte r>",
      "s": "0x<32-byte s>",
      "v": "0x1c"
    }
  ]
}
```

`to` above is the base64 form of the 20-byte address
`5c9d4cde23f68cd2209a2f5eaf0a1d34ac3e5f2a`. The `r`/`s`/`v` values must be a real
signature over the hash of exactly these fields. See
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md).

## `nhb_getBalance`

Params: `["<bech32 address>"]`. Returns the account summary
(`BalanceResponse`, `rpc/http.go`). Big integers are JSON numbers.

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

Optional fields: `delegatedValidator`, `pendingUnbonds[]` (`id`, `validator`,
`amount`, `releaseTime`), `unbondingCompletesAt`, `validatorRegisteredAt`. `nonce`
is the account nonce; a transaction with a lower nonce is rejected.

## `nhb_getTransaction`

Params: `["<tx hash>"]`. Returns `null` if the hash is unknown. Otherwise
(`TransactionResult`, `rpc/types.go`) all numeric fields are hex strings and
addresses are bech32:

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

## `nhb_getTransactionReceipt`

Params: `["<tx hash>"]`. Returns `null` if the hash is unknown. The receipt is
not a stored record: `buildReceiptResult` re-simulates the transaction with
`SimulateTx` and converts the resulting events to logs. `status` is always
`"0x1"`, including for a transaction whose simulation fails.

```json
{
  "transactionHash": "0x...",
  "blockHash": "0x...",
  "blockNumber": "0x1a",
  "status": "0x1",
  "gasUsed": "0x...",
  "logs": [
    {
      "event": "Transfer",
      "asset": "ZNHB",
      "from": "nhb1...",
      "to": "nhb1...",
      "txHash": "0x...",
      "value": "0xde0b6b3a7640000"
    }
  ]
}
```

Each log is a flat string map. For events of type `transfer.native` the `event`
key is `Transfer`, `amount` is renamed `value` (hex); for `fees.applied` the
`event` key is `FeeApplied` and `grossWei`/`feeWei`/`netWei` are renamed
`gross`/`fee`/`net` (hex). Other events use their event type as `event`. If the
simulation yields no logs and the type has an asset label, a fallback `Transfer`
log is synthesised. See [events](./events.md) for event attributes.

## Staking methods

`stake_previewClaim` and `stake_getPosition` require auth, are rate limited under
the message `staking rate limit exceeded` (HTTP 429), and return HTTP 503 with
code `-32050` and `staking module paused` while the staking module is paused.
Params: `["<bech32 address>"]`.

`stake_previewClaim` returns `{"payable": "<wei>", "nextPayoutTs": <unix>}`. When
no full payout period has elapsed since `StakeLastPayoutTs`, `payable` is `"0"`
and `nextPayoutTs` is the end of the current period (period length is the
governed `staking.payoutPeriodDays`, default 30).

`stake_getPosition` returns `{"shares": "...", "lastIndex": "...",
"lastPayoutTs": <unix>}` from the account's stake ledger fields.

`stake_delegate`, `stake_undelegate`, `stake_claim` and `stake_claimRewards` are
disabled. They return HTTP 410, code `-32060`, with a message directing the
caller to sign and submit `TxTypeStake`, `TxTypeUnstake`, `TxTypeStakeClaim` or
`TxTypeStakeClaimRewards` through `nhb_sendTransaction`
(`rpc/stake_handlers.go`).

## Other methods

Governance reads (`gov_proposal`, `gov_list`) and swap administration are in
[`docs/api.md`](../api.md). Fee queries are in [fees-query](./fees-query.md).
The full method table is the `switch` in `handle` (`rpc/http.go`); anything not
listed there returns `unknown method <name>` (`-32601`, HTTP 404).
