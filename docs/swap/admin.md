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
does not read that variable. `nhb-cli rpc-token` prints a short-lived token
(default 10 minutes, at most 24 hours) signed with the secret the node's
`[RPCJWT]` section names (`HSSecretEnv`, `NHB_RPC_JWT_SECRET` in the shipped
`config.toml`), read from that environment variable or from standard input
with `--secret-stdin` (`cmd/nhb-cli/rpc_token.go`). Run it on the node host and
export the output as `NHB_RPC_TOKEN`.

`swap_voucher_reverse` and `swap_markReconciled` additionally need an on-chain
signature from a key holding `ROLE_SWAP_ADMIN` (below).

## Reversal policy

Rules of `applySwapVoucherReverseTransaction` (`core/swap_admin_tx.go`):

* Only vouchers in `minted` status can be reversed. `reconciled` vouchers
  return `swap: voucher not in minted state`; an already reversed voucher
  returns `swap: voucher already reversed`.
* Vouchers are minted onto the recipient's ordinary ZNHB account balance, so
  the reversal works on the same balances: the recipient's ZNHB balance must
  cover the voucher's `mintAmountWei`, otherwise `swap: insufficient balance to
  reverse voucher`. The voucher's token must be `ZNHB`.
* The amount moves from the recipient to the refund sink. The sink is the
  node's treasury address: the genesis admin wallet, replaced by
  `NHB_MASTER_TREASURY` when that variable is set, and the node's validator
  address when neither is configured (`core.NewNode`,
  `StateProcessor.SetSwapRefundSink`). Nothing is burned. A voucher whose
  recipient is the sink is refused (`voucher recipient is the refund sink`),
  because the reversal would move nothing.
* When the sink is the admin wallet, the amount also rejoins the Sale Pool: the
  Sale Pool balance grows by it and the cumulative sale distributed counter is
  lowered by it (not below zero). A sink that is any other address leaves both
  pool counters untouched.
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
| Anything else (for example a recipient that is the refund sink) | 500 | `failed to reverse voucher` |

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
moves from `minted` to `reconciled` (`Ledger.MarkReconciled`,
`native/swap/ledger.go`); a voucher that is already `reconciled` is left as it
is, so submitting a batch twice is harmless. An unknown ID
(`ledger: voucher not found`) or a reversed voucher (`ledger: voucher cannot be
reconciled`) makes the whole batch fail, and nothing in it is written. The RPC
simulates the transaction first, so these surface synchronously as a 500
`failed to mark vouchers reconciled` (a wrong signer is a 403 `swap:
unauthorized admin`). On success the block emits `swap.treasury.reconciled`
with `vouchers` and `observedAt`.

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
not affect voucher mints. The method is not gated by any on-chain role; the RPC
bearer credential is all it needs.

## `swap_burn_list`

Params: `startTs`, `endTs`, optional `cursor`, optional `limit` (default 50).
Result: `{"receipts": [...], "nextCursor": "..."}`. See
[treasury.md](treasury.md).

## Alerts to monitor

`swap.alert.limit_hit`, `swap.alert.velocity` and `swap.alert.sanction` are
emitted by the mint path; attributes are listed in
[risk-controls.md](risk-controls.md#events).
