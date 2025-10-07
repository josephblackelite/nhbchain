# Paymaster mint controls

The paymaster auto top-up flow mints ZNHB directly into sponsorship accounts when balances fall below the configured floor. This document summarises the governance, rate limits, and monitoring that protect the pathway.

## Governance requirements

* **Configuration gatekeeping** – Automatic minting is off by default. Operators must explicitly set the `auto_top_up` block in the global configuration, including the ZNHB token, minimum balance, mint amount, cooldown, and daily cap, before the runtime enables the feature.【F:config/types.go†L142-L186】【F:config/global.go†L101-L165】
* **Role separation** – The runtime enforces that the configured operator address holds both the minting and approval roles before a top-up is attempted. Missing roles produce explicit failure reasons and no tokens are minted.【F:core/sponsorship.go†L604-L647】
* **Policy reload** – Updated policies propagate through the node state transition pipeline at block boundaries, ensuring governed changes take effect atomically across the cluster.【F:core/state_transition.go†L140-L207】【F:core/node.go†L214-L271】

## Rate limiting and execution safeguards

* **Minimum balance trigger** – Minting only executes when the paymaster balance drops below the configured threshold and a positive top-up amount is provided.【F:core/sponsorship.go†L576-L599】
* **Daily cap accounting** – Each paymaster tracks the amount minted per UTC day in state; attempts that would breach the cap are rejected and logged with a `daily_cap_exceeded` reason.【F:core/state/paymaster_counters.go†L388-L444】【F:core/sponsorship.go†L600-L613】
* **Cooldowns** – The most recent top-up timestamp is stored and checked before the next mint to prevent back-to-back executions inside the cooling window.【F:core/state/paymaster_counters.go†L418-L444】【F:core/sponsorship.go†L613-L625】

## Monitoring and auditability

* **Events** – Every execution (success or failure) emits a `paymaster.autotopup` event with the paymaster address, token, minted amount, resulting balance, and failure reason when relevant. Subscribe the security logging pipeline to capture these events for audit trails.【F:core/events/sponsorship.go†L144-L187】
* **Metrics** – Prometheus counters `nhb_paymaster_autotopups_total` and `nhb_paymaster_autotopup_amount_wei_total` track outcomes and aggregate minted volume. Alert on unexpected spikes or consecutive failures to catch abuse early.【F:observability/metrics.go†L205-L233】【F:observability/metrics.go†L300-L317】
* **Test coverage** – Automated tests cover success paths, cooldown enforcement, missing role failures, and daily cap checks. Extend these tests when adding new guardrails.【F:tests/paymaster/autofund_test.go†L45-L215】【F:tests/paymaster/autofund_test.go†L217-L357】

## Operational response

1. **Pause minting** – Set `enabled: false` in the `auto_top_up` configuration block and redeploy to halt automatic issuance while an incident is investigated.【F:config/global.go†L101-L165】【F:core/sponsorship.go†L556-L575】
2. **Rotate credentials** – Update the operator address or required roles in configuration and push the governed change. The runtime persists the update at the next block boundary, invalidating old keys immediately.【F:core/sponsorship.go†L604-L647】【F:core/state_transition.go†L140-L207】
3. **Audit trail review** – Inspect recent `paymaster.autotopup` events and Prometheus counters to quantify minted volume and determine whether rate limits held.【F:core/events/sponsorship.go†L144-L187】【F:observability/metrics.go†L205-L233】
