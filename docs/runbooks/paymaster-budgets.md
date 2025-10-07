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

## 5. Automatic top-ups

Automatic top-ups ensure the paymaster never runs dry during busy settlement windows. The feature is disabled by default and is only active when the `auto_top_up` block is configured under `Global.Paymaster` with a `ZNHB` token, operator address, mint/approve roles, and rate limits.【F:config/types.go†L142-L186】【F:config/global.go†L101-L165】

1. **Configuration**
   * `min_balance_wei` – threshold that triggers a top-up when the on-chain balance drops below the value.
   * `top_up_amount_wei` – amount of ZNHB minted on each execution.
   * `daily_cap_wei` and `cooldown` – guardrails that limit aggregate minting and cadence.【F:core/state/paymaster_counters.go†L388-L444】【F:core/sponsorship.go†L571-L668】
   * `operator`, `approver_role`, and `minter_role` – governance controls that must be satisfied before minting occurs.【F:core/sponsorship.go†L604-L647】
2. **Execution flow**
   * During sponsorship evaluation, the state processor checks the current ZNHB balance and enforces the policy, aborting with explicit failure reasons if guardrails or roles are not satisfied.【F:core/sponsorship.go†L552-L647】
   * Successful executions persist the daily mint counter and last-run timestamp to prevent duplicate minting within the cooldown window.【F:core/state/paymaster_counters.go†L388-L444】【F:core/sponsorship.go†L642-L668】
3. **Observability**
   * Events: monitor `paymaster.autotopup` for both success and failure outcomes. The payload includes status, reason, amounts, and the observed balance.【F:core/events/sponsorship.go†L144-L187】
   * Metrics: Grafana dashboards should scrape `nhb_paymaster_autotopups_total{outcome="success"|"failure"}` and `nhb_paymaster_autotopup_amount_wei_total` to alert on unexpected minting or repeated failures.【F:observability/metrics.go†L205-L233】
4. **Operational procedures**
   * Rotate operators or update governance roles by amending the configuration and pushing a governed change; nodes reload the policy at block boundaries.【F:core/node.go†L214-L271】【F:core/state_transition.go†L140-L207】
   * When pausing the engine, set `enabled: false` in the config and redeploy; the scheduler stops minting immediately after the new policy is active.【F:config/global.go†L101-L165】【F:core/sponsorship.go†L556-L575】

## 6. Troubleshooting

* If new limits are not visible, confirm the configuration map rollout finished and the consensus node picked up the change (check `kubectl logs deploy/consensus | grep PaymasterLimits`).
* If Prometheus metrics are missing, ensure the scrape job `consensus` is healthy and the node exports the paymaster collector.
* For repeated merchant cap exhaustion, coordinate with risk to re-evaluate the merchant’s credit line before raising limits.
