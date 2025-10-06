# Paymaster Sponsorship Guardrails

The paymaster module can now enforce daily budgets at multiple scopes to prevent runaway fee sponsorship. Operators can configure caps, monitor usage, and react to throttling events using the guidance in this document.

## Configuration

The global configuration file exposes three knobs under `[global.paymaster]`:

| Key | Description |
| --- | --- |
| `MerchantDailyCapWei` | Maximum NHB (wei) a merchant can sponsor across all devices in a UTC day. `0` disables the bound. |
| `DeviceDailyTxCap` | Maximum number of sponsored transactions a single device may submit per day. `0` disables the bound. |
| `GlobalDailyCapWei` | Network-wide NHB (wei) sponsorship budget per day. `0` disables the bound. |

Values accept the same integer formats as other monetary fields (for example `250000000000000000000`, `250e18`). Update the TOML file and restart consensusd to apply changes:

```toml
[global.paymaster]
MerchantDailyCapWei = "250e18"
DeviceDailyTxCap = 200
GlobalDailyCapWei = "1000e18"
```

## Budget Planning

When sizing caps consider:

* **Average ticket size**: Multiply the expected gas limit by the gas price to estimate per-transaction sponsorship cost.
* **Device distribution**: Set `DeviceDailyTxCap` slightly above the peak per-terminal volume to catch abuse without harming legitimate traffic.
* **Merchant spread**: Derive `MerchantDailyCapWei` by multiplying the average device spend by the number of active devices plus a safety margin.
* **Network aggregate**: Ensure `GlobalDailyCapWei` comfortably exceeds the sum of merchant budgets so a single participant cannot starve the fleet.

Example: if a POS transaction consumes ~25,000 gas at 1 gwei, each sponsorship costs `2.5e4 * 1e9 = 2.5e13 wei` (~0.000025 NHB). A merchant operating 40 lanes with a target of 1,500 transactions per lane could be capped at:

```
MerchantDailyCapWei = 40 lanes * 1,500 tx * 2.5e13 wei ≈ 1.5e18 wei (1.5 NHB)
DeviceDailyTxCap   = 2,000
GlobalDailyCapWei  = number_of_merchants * MerchantDailyCapWei * 1.2 safety factor
```

## Monitoring & Alerting

* The node emits a `paymaster.throttled` event whenever a sponsorship attempt exceeds a cap. Attributes include the scope (`merchant`, `device`, or `global`), the day, and limit metadata.
* Use the RPC method `transactions_sponsorshipCounters` to poll current usage. Example request:

```json
{
  "method": "transactions_sponsorshipCounters",
  "params": [{
    "merchant": "merchant-1",
    "deviceId": "device-12",
    "day": "2024-05-19"
  }]
}
```

The response returns per-scope budgets (`budgetWei`), actual charges (`chargedWei`), and transaction counts for the day.

Set alerts when usage approaches 80–90% of any cap so operators can increase limits or investigate abuse before throttling begins.

## Troubleshooting

If a merchant reports throttled terminals:

1. Check the latest `paymaster.throttled` events to confirm the scope and cap involved.
2. Query counters for the merchant/device/day via RPC to evaluate actual consumption.
3. Adjust the relevant cap(s) in `[global.paymaster]` if the budget is too conservative, or contact the merchant if volume looks abnormal.

Remember that caps reset at midnight UTC. Counter queries shortly after rollover should show zeroed metrics, confirming the guard reset.
