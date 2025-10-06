# Paymaster Budget Runbook

This runbook documents how operations teams manage POS sponsorship budgets, enforce daily caps, and monitor utilisation. The limits configured here align with the runtime enforcement in `core.PaymasterLimits` and the network configuration loader.【F:core/sponsorship.go†L92-L145】【F:config/global.go†L8-L56】

## 1. Budget sources

* **Global cap** – Upper bound for sponsored outflow across all merchants per UTC day.
* **Merchant cap** – Daily limit for a specific merchant; enforced before device-level checks.
* **Device cap** – Optional limit on the number of sponsored transactions per device per day.

The values are supplied through the `Global.Paymaster` section of the cluster configuration. Changes must be committed to the configuration repository and rolled out through CI before taking effect.【F:config/global.go†L8-L56】

## 2. Updating limits

1. **Stage the change**
   * Edit the environment overlay (for example `deploy/environments/prod/global.yaml`) to update `paymaster.global_daily_cap`, `paymaster.merchant_daily_cap`, or `paymaster.device_daily_tx_cap`.
   * Open a change request with the diff and obtain the required approvals.
2. **Apply to the cluster**
   * Deploy the update via the GitOps pipeline or run `kubectl apply -f deploy/environments/prod` from the release runner.
   * Watch the rollout status: `kubectl rollout status deploy/consensus`.
3. **Verify runtime limits**
   * Review the consensus node logs for the refreshed limits (`kubectl logs deploy/consensus | grep PaymasterLimits`).
   * Cross-check Prometheus metrics `pos_paymaster_global_remaining` and `pos_paymaster_merchant_remaining` to confirm they reflect the new caps.

## 3. Monitoring utilisation

1. **Dashboards**
   * Grafana dashboard `POS Sponsorship Budgets` charts the remaining global and merchant capacity alongside burn rate forecasts.
   * The dashboard reads from Prometheus metrics emitted by the consensus node’s state processor.【F:core/sponsorship.go†L231-L247】【F:core/sponsorship.go†L388-L410】
2. **Daily reports**
   * Export the paymaster usage CSV from the data warehouse and attach it to the daily operations report.
   * Investigate any merchant exceeding 80% of their allocation by opening an escalation ticket.

## 4. Alerts

1. **Threshold alerts**
   * Configure Prometheus alert rules for `pos_paymaster_global_remaining < (0.1 * pos_paymaster_global_cap)` and merchant equivalents.
   * Route alerts to the on-call rotation and tag the affected merchant.
2. **Anomaly alerts**
   * Set up alerting on `pos_paymaster_device_rejects` spikes to catch runaway devices.
   * Include a playbook link back to this runbook and the device attestation procedure.

## 5. Troubleshooting

* If new limits are not visible, confirm the configuration map rollout finished and the consensus node picked up the change (check `kubectl logs deploy/consensus | grep PaymasterLimits`).
* If Prometheus metrics are missing, ensure the scrape job `consensus` is healthy and the node exports the paymaster collector.
* For repeated merchant cap exhaustion, coordinate with risk to re-evaluate the merchant’s credit line before raising limits.
