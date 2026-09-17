# Fees Query API Examples

This guide demonstrates how to export fee settlement data for the network-wide transparency
reporting pipeline. Use these snippets to hydrate the SQL queries documented in
[`docs/queries/fees.sql`](../queries/fees.sql).

## Prerequisites

- Access to an NHB archival RPC endpoint (`https://rpc.nhbchain.dev` or your self-hosted replica).
- `jq` or a similar JSON processor for command-line examples.

## Query Fee Totals (JSON-RPC)

```bash
curl -s -X POST https://rpc.nhbchain.dev \
  -H "Authorization: Bearer $NHB_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "jsonrpc": "2.0",
        "id": "fees",
        "method": "fees_listTotals",
        "params": [{"domain": "'"$DOMAIN"'"}]
      }' | jq '.result'
```

Returns per-wallet gross/fee/net totals (`domain`, `wallet`, `grossWei`, `feeWei`, `netWei`) for the
requested domain. Persist the response to object storage before loading it into your analytics
database. For point-in-time status instead of accumulated totals, use `fees_getMonthlyStatus`
(network-wide monthly usage window) or `fees_getTransferStatus` (per-address free-tier eligibility,
params: `{"address": "nhb1..."}`).

### Event attribute reference

The `fees.applied` payload now exposes additional fields to help downstream systems
reason about the free-tier window and revenue routing:

| Attribute | Description |
| --- | --- |
| `ownerWallet` | Hex-encoded 20-byte address that accrued the fee. |
| `freeTierApplied` | `true` when the transaction consumed the free tier rather than paying MDR. |
| `freeTierLimit` | Monthly allowance in transactions for the payer's domain. |
| `freeTierRemaining` | Transactions remaining in the current UTC month after processing the event. |
| `usageCount` | Post-increment counter for the payer within the active month. |
| `windowStartUnix` | Unix timestamp (seconds) for the start of the billing month applied to the event. |
| `feeBps` | Effective MDR basis points applied when a fee was charged. |

Older attributes (`payer`, `grossWei`, `feeWei`, `netWei`, etc.) remain unchanged.

## Loading into SQLite

```bash
sqlite3 fees.db <<'SQL'
CREATE TABLE IF NOT EXISTS fee_events (
  tx_id TEXT PRIMARY KEY,
  block_timestamp DATETIME,
  domain TEXT,
  merchant_id TEXT,
  merchant_name TEXT,
  fee_amount_native REAL,
  fee_amount_usdc REAL,
  fee_amount_usd REAL
);
.mode json
.import fee-events.json fee_events
SQL
```

## Loading into ClickHouse

```bash
clickhouse-client --secure --query "
CREATE TABLE IF NOT EXISTS fee_events (
  tx_id String,
  block_timestamp DateTime64(3, 'UTC'),
  domain LowCardinality(String),
  merchant_id String,
  merchant_name String,
  fee_amount_native Decimal(38, 18),
  fee_amount_usdc Decimal(38, 6),
  fee_amount_usd Decimal(38, 6)
) ENGINE = MergeTree()
ORDER BY (block_timestamp, domain);
"

clickhouse-client --secure --query "INSERT INTO fee_events FORMAT JSONEachRow" < fee-events.ndjson
```

Once ingested, you can run the transparency queries directly or drive the Grafana dashboard via the
`clickhouse-datasource` plugin.
