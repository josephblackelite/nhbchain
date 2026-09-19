# Swap Admin RPC Reference

Reference for the admin-only swap methods: limits, provider status,
reversal, reconciliation, manual quotes.

## Authentication

Every method on this page requires an RPC credential accepted by
`requireAuthInto` (`rpc/http.go`): a valid JWT sent as
`Authorization: Bearer <jwt>` (validated per the `[RPCJWT]` section of
`config.toml`), or a verified client certificate when the server requires
client certificates. `nhb-cli` reads the token to send from the
`NHB_RPC_TOKEN` environment variable (`cmd/nhb-cli/main.go`); the node itself
does not read that variable.

`swap_voucher_reverse` and `swap_markReconciled` additionally need an on-chain
signature from a key holding `ROLE_SWAP_ADMIN` (below).

## Reversal policy

* Only vouchers in `minted` status can be reversed. `reconciled` vouchers
  return `swap: voucher not in minted state`
  (`core/swap_admin_tx.go` lines 225-232).
* The recipient's balance of the voucher token (ZNHB) must cover the voucher's
  `mintAmountWei`; otherwise `swap: insufficient balance to reverse voucher`.
* The amount moves from the recipient to the refund sink. The sink is the node's
  treasury address: the genesis admin wallet, replaced by `NHB_MASTER_TREASURY`
  when that variable is set, and the node's validator address when neither is
  configured (`core/node.go` lines 484-516). Nothing is burned.
* Both `swap_voucher_reverse` and `swap_markReconciled` build a real
  transaction (`TxTypeSwapVoucherReverse` `0x4A`, `TxTypeSwapMarkReconciled`
  `0x4B`) carrying the admin signature. The RPC call only enqueues it; state
  changes when a block includes it. The RPC bearer credential alone does not
  authorize the change.
* Both are blocked while the `swap` module is paused.

### `ROLE_SWAP_ADMIN`

The signer recovered from the signature must currently hold the role
(`core/swap_admin_tx.go` `recoverSwapAdminSigner`); otherwise
`swap: unauthorized admin`. The repository `config.toml` lists
`MINTER_NHB`, `ROLE_SWAP_PAYOUT_ATTESTOR`, `ROLE_ESCROW_REALM_ADMIN` and
`ROLE_LOYALTY_ADMIN` in `[global.Governance].AllowedRoles` and does not list
`ROLE_SWAP_ADMIN`. Genesis specs carry a `roles` map (`core/genesis/spec.go`).

## `swap_limits`

Returns the mint counters and remaining room for an address. Params:
`["<bech32 address>"]`.

```json
{"jsonrpc": "2.0", "id": 1, "method": "swap_limits", "params": ["nhb1..."]}
```

Result fields (`rpc/swap_admin_handlers.go`):

* `address`
* `day` - `{ "bucket": "YYYY-MM-DD", "mintedWei": "..." }` (UTC)
* `month` - `{ "bucket": "YYYY-MM", "mintedWei": "..." }` (UTC)
* `dayRemainingWei`, `monthRemainingWei` - only when the corresponding cap is set (> 0)
* `velocity` - only when both `VelocityWindowSeconds` and `VelocityMaxMints` are set: `{ "windowSeconds", "maxMints", "observed", "remaining" }`

The caps reported are the ones in the node's local `[swap.risk]` config.

## `swap_provider_status`

No parameters. Returns `allow`, `lastOracleHealthCheck`, and optionally
`oracleFeeds`. See [oracle.md](oracle.md#provider-status) for what those
values currently contain.

## `swap_voucher_reverse`

Params: one object `{ "providerTxId": "...", "signature": "0x..." }`.
`signature` is a 65-byte hex signature over

```
keccak256("NHB_SWAP_VOUCHER_REVERSE_V1|providerTxId=<providerTxId>")
```

(`core.SwapVoucherReverseSigningHash`; `providerTxId` trimmed).

```json
{"jsonrpc": "2.0", "id": 3, "method": "swap_voucher_reverse",
 "params": [{"providerTxId": "order-12345", "signature": "0x..."}]}
```

Result: `{"ok": true, "txHash": "0x..."}` once the transaction is accepted into
the mempool. `AddTransaction` simulates the state transition first, so most
failures return synchronously:

| Condition | HTTP | Message |
| --- | --- | --- |
| Voucher already reversed | 200 | result `{"ok": true}` (no `txHash`) |
| Signer lacks the role | 403 | `swap: unauthorized admin` |
| Not in `minted` state | 409 | `swap: voucher not in minted state` |
| Recipient balance too low | 409 | `swap: insufficient balance to reverse voucher` |
| Unknown `providerTxId` | 404 | `swap: voucher not found` |
| Anything else | 500 | `failed to reverse voucher` |

On success the block emits `swap.voucher.reversed` with `providerTxId`,
`admin`, `recipient`, `token`, `amountWei`, `observedAt`.

## `swap_markReconciled`

Params: one object `{ "providerTxIds": ["...", "..."], "signature": "0x..." }`.
`signature` signs

```
keccak256("NHB_SWAP_MARK_RECONCILED_V1|providerTxIds=<ids joined with ",">")
```

with each id trimmed, blank entries removed, and the original order kept
(`core.SwapMarkReconciledSigningHash`).

Result: `{"ok": true, "txHash": "0x..."}`. When applied, every listed voucher
that exists has its status set to `reconciled`, unknown IDs are skipped
silently, and the block emits `swap.treasury.reconciled` with `vouchers` and
`observedAt`.

## `swap_setManualQuote`

Sets a quote on the in-process manual oracle source. Params: one object.

```json
{"jsonrpc": "2.0", "id": 4, "method": "swap_setManualQuote",
 "params": [{"base": "USD", "quote": "ZNHB", "rate": "0.05", "timestamp": 1734000000}]}
```

* `base`, `quote`, `rate` are required; `rate` must parse as a positive
  decimal (`ManualOracle.SetDecimal`).
* `timestamp` is optional Unix seconds (must be >= 0); it defaults to the
  current time.
* Result: `{"ok": true, "base", "quote", "rate", "observedAt"}` with `base` and
  `quote` upper-cased and `observedAt` in RFC 3339.

The manual source only matters to the in-process `OracleAggregator`, which no
current code path reads (see [oracle.md](oracle.md#where-it-is-used)). It does
not affect voucher mints. The method is not gated by any on-chain role.

## `swap_burn_list`

Params: `startTs`, `endTs`, optional `cursor`, optional `limit` (default 50).
Result: `{"receipts": [...], "nextCursor": "..."}`. See
[treasury.md](treasury.md).

## Alerts to monitor

`swap.alert.limit_hit`, `swap.alert.velocity` and `swap.alert.sanction` are
emitted by the mint path; attributes are listed in
[risk-controls.md](risk-controls.md#events).

Submit a signed batch marking one or more vouchers as reconciled against treasury records. `signature` is a hex-encoded 65-byte secp256k1 signature over `keccak256("NHB_SWAP_MARK_RECONCILED_V1|providerTxIds=<comma-joined providerTxIds>")` (trimmed, blank entries removed, in the exact order submitted), produced by a key holding `ROLE_SWAP_ADMIN`.

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "swap_markReconciled",
  "params": [{"providerTxIds": ["order-12345", "order-12346"], "signature": "0x..."}]
}
```

Returns `{ "ok": true, "txHash": "0x..." }` the same way `swap_voucher_reverse` does.

### `swap_setManualQuote`

Publishes a manual override rate for a currency pair on the manual oracle tier. Manual rates sit at the bottom of the priority stack (see `docs/treasury/peg-policy.md`) and are the on-call circuit breaker used during custody outages or extreme volatility. Without a fresh call to this endpoint, the manual tier goes stale after `MaxQuoteAgeSeconds` and is skipped by the aggregator.

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "swap_setManualQuote",
  "params": [{"base": "USD", "quote": "ZNHB", "rate": "0.05", "timestamp": 1734000000}]
}
```

* `base` / `quote` – currency pair, e.g. `USD` / `ZNHB`.
* `rate` – decimal string, quote per base (must be positive).
* `timestamp` – optional Unix seconds; defaults to the current time when omitted.

Record the justification and incident ticket ID before invoking this command.

## Incident Response

* Spike in `swap.alert.velocity` – confirm PSP behaviour, temporarily raise `VelocityMaxMints` if needed, and log the change.
* Sanctions alert – freeze the account, notify compliance, and coordinate with the sanctions provider.
* Repeated provider rejections – verify the allow list matches the operational roster and update `[swap.providers]` if a new PSP is onboarded.

Maintain a weekly audit of reversal activity by exporting the voucher ledger and filtering for `status = reversed`.
