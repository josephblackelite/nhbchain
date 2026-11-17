# Lending state keys and query paths

Keys written by the lending module (`core/state/manager.go`,
`lending_refprice.go`, `lending_autodebit.go`, `lending_deposit_payout.go`; the trie
Keccak-256-hashes every KV key before storing it):

| Key | Stored value |
| --- | --- |
| `lending/market/<poolId>` | market record (includes `LastUpdateBlock` and `LastUpdateTimestamp`, the block time interest has been accrued to) |
| `lending/pools/index` | list of pool IDs |
| `lending/user/<poolId>:<20-byte address>` | user account (shares, collateral, debt, scaled debt, last supply block) |
| `lending/legacy-reconciled/<poolId>` | marker that the one-time sweep of lending positions still recorded on plain accounts into the pool's own records has finished for that pool (`LendingLegacyReconciled`, `core/state/manager.go`) |
| `lending/fees/<poolId>` | fee accrual (protocol and developer fees) |
| `lending/loan/<loanId>` | fixed-term loan |
| `lending/loanactive/...` | the borrower's active fixed-term loan ID per pool |
| `lending/deposit/<depositId>` | fixed-term deposit |
| `lending/refprice/last` | the last accepted reference-price record (`core/state/lending_refprice.go`) |
| `lending/autodebit/due/<day>` | fixed-term loan IDs due for an auto-debit attempt on that UTC day number (`core/state/lending_autodebit.go`) |
| `lending/autodebit/watermark` | last UTC day the auto-debit settlement finished (same file) |
| `lending/depositpayout/due/<day>` | fixed-term deposit IDs due for a payout attempt on that UTC day number (`core/state/lending_deposit_payout.go`) |
| `lending/depositpayout/watermark` | last UTC day the deposit payout settlement finished (same file) |

## Consensus query router

`StateProcessor.QueryState("lending", path)` and `QueryPrefix("lending", scope)`
(`core/query_router.go`):

| Call | Result |
| --- | --- |
| `QueryState("lending", "markets")` | JSON array of all markets. |
| `QueryPrefix("lending", "")` or `"markets"` | One record per market, keyed by `PoolID`. Any other scope returns `ErrQueryNotSupported`. |
| `QueryState("lending", "positions/<address>")` | JSON array of `{"poolId", "account"}`, one entry for each pool in which the address has a stored account. The address may be bech32 or `0x` hex. |

Any other path returns `ErrQueryNotSupported`. These results are raw stored
records: unlike `lending_getMarket`, they are not accrual-projected and carry no
computed APY fields.
