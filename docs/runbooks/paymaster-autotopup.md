# Paymaster Automatic Top-up Runbook

Automatic paymaster top-ups mint ZNHB when sponsored balances dip below the configured floor. This runbook explains how to triage
alerted activity, validate configuration, and restore healthy minting behaviour.

## Monitoring Signals

* **Metrics** – Prometheus counters `nhb_paymaster_autotopups_total{outcome="success"|"failure"}` and
  `nhb_paymaster_autotopup_amount_wei_total` expose the cadence and minted volume for the auto top-up engine.【F:observability/metrics.go†L311-L327】【F:observability/metrics.go†L451-L464】
* **Alerts** –
  * `PaymasterAutoTopUpSpikeWarning` (warning) fires when more than three successful executions land within five minutes, indicating
    a sudden jump in sponsored spend.【F:observability/alerts.yaml†L145-L156】
  * `PaymasterAutoTopUpSpikeCritical` (critical) fires when the success counter jumps above ten in five minutes, signalling possible
    abuse or runaway configuration and requiring immediate triage.【F:observability/alerts.yaml†L158-L169】
  * `PaymasterAutoTopUpCapSaturationWarning` (warning) triggers on the first observed failure in fifteen minutes, hinting that the
    configured cap may be binding.【F:observability/alerts.yaml†L171-L182】
  * `PaymasterAutoTopUpCapSaturationCritical` (critical) raises when more than three failures occur in fifteen minutes, confirming
    repeated cap hits that will soon block sponsored payments.【F:observability/alerts.yaml†L184-L195】
* **Dashboards** – The "Paymaster Automatic Top-ups" Grafana board visualises 24h minted volume, cap snapshots, utilisation, and failure
  counts. Update the constant variable `daily_cap_wei` to match production config so utilisation percentages are accurate.【F:observability/dashboards/paymaster-autotopup.json†L1-L220】

## Investigation Checklist

1. **Confirm alert context**
   * Review the spike panel to validate whether the surge is sustained or a single burst of catch-up mints.【F:observability/dashboards/paymaster-autotopup.json†L22-L108】
   * Check the "24h Minted vs Cap Snapshot" panel for how far current minting is from the configured limit before adjusting
     budgets.【F:observability/dashboards/paymaster-autotopup.json†L171-L214】
   * Inspect the failure count panel to distinguish between cap saturation and configuration mistakes.【F:observability/dashboards/paymaster-autotopup.json†L121-L170】
2. **Correlate with on-chain events**
   * Query recent `paymaster.autotopup` events (via the data warehouse or node logs) for the `reason` field. A value of
     `daily_cap_exceeded` confirms the cap is binding.【F:core/events/sponsorship.go†L144-L187】
   * Cross-reference merchant activity and POS throughput dashboards to ensure the demand spike originates from legitimate
     traffic.
3. **Validate configuration**
   * Compare the minted 24h volume against the configured `DailyCapWei` in the node configuration bundle. Update the dashboard
     variable if the cap changed recently.【F:config/global.go†L50-L109】
   * Ensure the consensus deployment picked up any recent limit adjustments (check `PaymasterLimits` logs on the node).【F:core/sponsorship.go†L96-L381】
4. **Mitigation options**
   * **Cap increase** – Coordinate with treasury/governance to raise `DailyCapWei` and redeploy configuration if legitimate
     demand is exceeding the budget. Document the change in the paymaster budget runbook.【F:docs/runbooks/paymaster-budgets.md†L1-L66】
   * **Pause minting** – Temporarily disable automatic top-ups by setting `enabled: false` in the auto-top-up block when abusive
     behaviour is suspected. Monitor the paymaster balance manually while the investigation continues.【F:config/global.go†L50-L109】
   * **Merchant throttling** – If a single merchant is generating runaway demand, adjust their sponsorship limits under
     `MerchantDailyCapWei` or disable sponsorship for the merchant until reconciliation completes.【F:core/sponsorship.go†L320-L381】

## Post-incident Actions

* File an incident report summarising minted totals, the trigger, and corrective actions taken.
* Backfill monitoring dashboards with the updated cap value and confirm alerts cleared within one evaluation window.
* Schedule a configuration review to ensure cooldowns and alert thresholds still align with expected volumes.
