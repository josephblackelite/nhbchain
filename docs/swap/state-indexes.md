# Swap state indexes

The swap module persists voucher and provider metadata in the key-value store. The consensus query router relies on the following keys:

| Key | Stored value | Notes |
| --- | --- | --- |
| `swap/voucher/{providerTxId}` | `swap.VoucherRecord` | Queried via `QueryState("swap", "vouchers/{providerTxId}")`. Records include oracle metadata and mint status. |
| `swap/oracles` | `swap.ProviderStatus` | Served from node memory to preserve live oracle health and provider allow-list configuration. |

Voucher listings remain available through the existing RPC pagination endpoints; the query router focuses on point lookups for audit tooling.
