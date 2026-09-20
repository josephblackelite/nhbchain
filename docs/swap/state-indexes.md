# Swap state keys and query paths

Keys written by the swap module (before the trie applies its own hashing;
`core/state/manager.go` `kvKey` Keccak-256-hashes every KV key):

| Key | Stored value | Source |
| --- | --- | --- |
| `swap/voucher/<providerTxId>` | voucher record (`swap.VoucherRecord`) | `native/swap/ledger.go` |
| `swap/voucher/index` | index of voucher IDs with creation times | `native/swap/ledger.go` |
| `swap/order/<orderId>` | used-nonce marker for replay protection | `core/state/manager.go` |
| `swap/oracle/signer/<lower-case provider>` | 20-byte price-proof signer address | `core/state/manager.go` |
| `swap/oracle/last/<BASE>` | last accepted price proof (rate, timestamp) | `core/state/manager.go` |
| `swap/risk/daily/<YYYY-MM-DD>/<hex address>`, `swap/risk/monthly/<YYYY-MM>/<hex address>`, `swap/risk/velocity/<hex address>`, `swap/risk/index/<hex address>` | mint-side risk counters | `native/swap/risk.go` |
| `swap/redeemrisk/...` | redeem-side risk counters | `native/swap/redeem_risk.go` |
| `swap/sanctions/audit/<hex address>` | list of sanctions failures | `native/swap/sanctions.go` |
| `swap/burn/<receiptId>`, `swap/burn/index` | burn receipts | `native/swap/redeem.go` |
| `swap/stable/...` | stable ledger records ([stable-ledger.md](stable-ledger.md)) | `native/swap/keys.go` |

## Consensus query router

`StateProcessor.QueryState("swap", path)` (`core/query_router.go`,
`querySwapState`) supports exactly one path:

| Path | Result |
| --- | --- |
| `vouchers/<providerTxId>` | JSON of the voucher record, or an empty result when it does not exist. |

The state processor returns `ErrQueryNotSupported` for any other swap path.
`Node.QueryState` (`core/node.go`) catches that error and calls
`queryStateFallback`, which answers the swap path `oracles` with the same JSON
that `Node.SwapProviderStatus()` returns (provider and oracle health); every
other swap path still fails as unsupported. `QueryPrefix` does not support the
`swap` namespace. Provider status is also available through the
`swap_provider_status` RPC method. Voucher listings are available through `swap_voucher_list` and
`swap_voucher_export`.
