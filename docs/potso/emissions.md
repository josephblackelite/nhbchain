# POTSO Emissions Observability

The POTSO emissions dashboards give operations, treasury, and integrations
teams real-time insight into reward budgets, evidence processing, and webhook
health. Each dashboard is backed by Prometheus metrics exposed by the node and
can be imported directly into Grafana using the JSON exports stored in
`observability/grafana/dashboards/`.

## Dashboards

### POTSO Overview

* **Evidence Acceptance Rate** – `potso_evidence_accepted_total{type}` plotted
  as a per-second rate to confirm reporters are submitting records at the
  expected cadence. Sudden spikes or a single type dominating the graph should
  trigger abuse investigations.
* **Penalty Application Rate** – mirrors `potso_penalty_applied_total{type}`.
  This should roughly track the evidence rate. Divergence indicates penalty
  workers are lagging or rejecting submissions.
* **Active Epoch Pool** – the `potso_epoch_pool` gauge confirms treasury
  funding is sufficient for the current epoch.
* **Webhook Failures** – derived from
  `increase(potso_webhook_failures_total{destination}[1h])` to quickly identify
  unhealthy delivery targets.
* **Rounding Dust by Epoch** – the running total of
  `potso_rounding_dust{epoch}` to catch rounding regressions.

### POTSO Emissions & Caps

* **Reward Emissions by Epoch** – view `potso_rewards_sum{epoch}` to verify the
  expected emission curve.
* **Emission Pool vs Remaining Budget** – overlays the live
  `potso_epoch_pool` against the sum of `potso_rewards_sum`. When the remaining
  budget drops below 10% the `POTSOEmissionCapApproach` alert fires.
* **Rounding Dust Share** – calculates the proportion of dust relative to the
  pool to ensure the carry-over invariant stays below operational thresholds.
* **Penalty Pressure** – 15 minute increases of `potso_penalty_applied_total`
  by type show whether penalty workers are saturated.

### POTSO Rewards Pipeline

* **Evidence Intake vs Penalties** – compares the 5 minute rate of evidence and
  penalties to highlight backlog risk.
* **Rewards vs Dust** – overlays per-epoch reward sums and rounding dust so
  finance can confirm totals before publishing exports.
* **Webhook Failure Rate** – tracks `rate(potso_webhook_failures_total[5m])` per
  destination to reveal SLO breaches.
* **Latest Reward Snapshot** – table view combining the latest reward total and
  dust for each epoch. Use this to cross-check ledger exports prior to
  settlement runs.

## Alert Playbooks

### POTSOEvidenceSpike & POTSOEvidenceDelta

* **Trigger** – Evidence acceptance rate exceeds 10/sec for 5 minutes or more
  than 300 submissions land in 10 minutes.
* **Response** – Page the incident commander in #potso-ops. Verify reporter
  addresses against the allow list and inspect the fraud heuristics service.
  If legitimate, scale penalty workers. If abusive, temporarily block the
  reporter and coordinate with governance to apply penalties.

### POTSOFailedWebhookDelivery

* **Trigger** – More than five webhook failures accumulate within five
  minutes.
* **Response** – Notify the integrations on-call engineer. Check dispatcher
  logs for HTTP status codes, confirm downstream endpoints are reachable, and
  pause retries if a partner is degraded to avoid flooding.

### POTSOIdempotencyConflicts

* **Trigger** – Any delivery labelled `destination="duplicate"` fails within
  a 10 minute window.
* **Response** – Escalate to the webhook consumer immediately. Confirm the
  consumer is using the `deliveryId` as an idempotency key and that caches are
  cleared. Rebuild the retry queue once the consumer acknowledges the fix.

### POTSOEmissionCapApproach

* **Trigger** – Remaining budget falls below 10% of the active epoch pool for
  10 minutes.
* **Response** – Page treasury operations. Audit recent reward configuration
  changes, confirm treasury replenishment transfers are on schedule, and be
  ready to halt reward settlements if the cap is about to breach.

### POTSORoundingDustExceedsThreshold

* **Trigger** – Rounding dust exceeds one token on any epoch for 10 minutes.
* **Response** – Notify finance. Validate reward weight inputs, recalculate the
  affected epochs, and confirm dust is rolling into the next pool as expected.
  If dust is not draining, escalate to the core engineering lead.

### POTSOCapInvariantBreach

* **Trigger** – The sum of `potso_rewards_sum` exceeds `potso_epoch_pool`.
* **Response** – Treat as a sev-0. Emit `potso.alert.invariant_violation` with
  the breach details, stop payout processing, and page both treasury and core
  engineering. Roll back the epoch or patch the reward config before resuming
  settlements.

Keeping these dashboards and playbooks up to date ensures emission invariants
remain intact and incidents can be remediated quickly.
