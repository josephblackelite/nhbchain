-- Fee transparency canonical queries
-- Export settlement events into your analytical store (SQLite, ClickHouse) using the
-- ingestion recipe in docs/api/fees-query.md.

-- =============================================
-- Daily totals (SQLite)
-- =============================================
WITH fee_events AS (
    SELECT
        date(block_timestamp) AS fee_date,
        domain,
        fee_amount_native AS fee_native,
        fee_amount_usdc AS fee_usdc
    FROM fee_events
)
SELECT
    fee_date,
    SUM(fee_native) AS total_fee_native,
    SUM(fee_usdc) AS total_fee_usdc,
    SUM(fee_native + fee_usdc) AS total_fee_all
FROM fee_events
GROUP BY fee_date
ORDER BY fee_date DESC
LIMIT 30;

-- =============================================
-- Fee totals by domain (ClickHouse)
-- =============================================
SELECT
    toStartOfDay(block_timestamp) AS fee_day,
    domain,
    sum(fee_amount_usd) AS fee_total_by_domain
FROM fee_events
WHERE block_timestamp >= now() - INTERVAL 30 DAY
GROUP BY fee_day, domain
ORDER BY fee_day DESC, fee_total_by_domain DESC;

-- =============================================
-- Top merchants last 7 days
-- =============================================
SELECT
    merchant_id,
    anyLast(merchant_name) AS merchant_name,
    countDistinct(tx_id) AS tx_count,
    sum(fee_amount_usd) AS fee_total_usd,
    quantileExactWeighted(0.95)(fee_amount_usd, 1) AS fee_p95_usd,
    avg(fee_amount_usd) AS fee_avg_usd
FROM fee_events
WHERE block_timestamp >= now() - INTERVAL 7 DAY
GROUP BY merchant_id
ORDER BY fee_total_usd DESC
LIMIT 20;

-- =============================================
-- Free-tier burn down (ClickHouse)
-- =============================================
WITH grants AS (
    SELECT
        sum(grant_amount) AS total_granted
    FROM free_tier_grants
), burns AS (
    SELECT
        toStartOfHour(block_timestamp) AS burn_hour,
        sum(burn_amount) AS burned_amount
    FROM free_tier_burns
    WHERE block_timestamp >= now() - INTERVAL 30 DAY
    GROUP BY burn_hour
)
SELECT
    burn_hour,
    burned_amount,
    (SELECT total_granted FROM grants) - sum(burned_amount) OVER (ORDER BY burn_hour ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS remaining_allocation
FROM burns
ORDER BY burn_hour ASC;

-- =============================================
-- Route balance reconciliation (ClickHouse)
-- =============================================
SELECT
    toStartOfHour(snapshot_at) AS hour_bucket,
    wallet_label,
    sum(balance_usd) AS balance_usd
FROM wallet_balance_snapshots
WHERE wallet_label IN ('owner_nhb', 'znhb_proceeds', 'owner_usdc')
  AND snapshot_at >= now() - INTERVAL 14 DAY
GROUP BY hour_bucket, wallet_label
ORDER BY hour_bucket DESC;
