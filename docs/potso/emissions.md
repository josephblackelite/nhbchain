# POTSO Emissions Observability

The dashboards, JSON exports, and alert rules described below are real and
deployed (`observability/grafana/dashboards/`,
`observability/alerts/alert_rules.yaml`). **Most of the underlying metrics
are not currently recorded, though.** Of the metrics this document
references, only the heartbeat-related ones (used by
`potso_heartbeat_total` and friends, documented elsewhere) are actually
observed anywhere in the node's evidence/penalty/reward code paths.
`potso_evidence_accepted_total`, `potso_penalty_applied_total`,
`potso_epoch_pool`, `potso_rewards_sum`, `potso_rounding_dust`, and
`potso_webhook_failures_total` are all defined and registered with
Prometheus, but nothing currently calls their setter methods — every panel
and alert built on them below will read as flat zero and none of those
alerts can fire. (`potso_webhook_failures_total` specifically stays dormant
because nothing dispatches POTSO reward webhooks at all — see the "Not
currently available" note in [rewards-integration.md](rewards-integration.md).)
This section documents provisioned-but-inactive observability
infrastructure, not something you can currently rely on for real-time
insight — treat it as a reference for when these metrics get wired up, not
as an operational guide today.

## Emission configuration

The per-epoch reward pool is a single fixed value, not a decaying schedule:
`EmissionPerEpoch` under `[potso.rewards]` in the node's TOML config (see
`config/prod.toml`), expressed as a decimal wei string (e.g.
`EmissionPerEpoch = "1000000000000000000"` for 1 ZNHB/epoch). There is
currently no TOML-driven decay schedule, epoch-window configuration, or
schedule-snapshot/change-log system — changing the emission rate means
updating `EmissionPerEpoch` and redeploying config, the same as any other
node config value.

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

The corresponding Grafana JSON is stored at `observability/grafana/dashboards/potso-overview.json`.

### POTSO Emissions & Caps

* **Reward Emissions by Epoch** – view `potso_rewards_sum{epoch}` to verify the
  expected emission curve.
* **Emission Pool vs Remaining Budget** – overlays the live
  `potso_epoch_pool` against the sum of `potso_rewards_sum`. When the remaining
  budget drops below 10% the `POTSOEmissionCapApproach` alert fires.
* **Rounding Dust Share** – calculates the proportion of dust relative to the
  pool. Note: there is no automatic carry-forward of dust into the next
  epoch's pool — the reward config has an inert `CarryRemainder` flag that
  isn't currently wired to any carry-forward logic.
* **Penalty Pressure** – 15 minute increases of `potso_penalty_applied_total`
  by type show whether penalty workers are saturated.

Artifacts:

* JSON export: `observability/grafana/dashboards/potso-emissions-and-caps.json`

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

Artifacts:

* JSON export: `observability/grafana/dashboards/potso-rewards-pipeline.json`

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
* **Response** – Notify finance and validate reward weight inputs for the
  affected epochs. Dust does not currently carry forward automatically (see
  the caveat above) — if this alert ever fires it means dust is
  accumulating, not draining, so escalate to the core engineering lead
  rather than expecting it to self-resolve.

### POTSOCapInvariantBreach

* **Trigger** – The sum of `potso_rewards_sum` exceeds `potso_epoch_pool`.
* **Response** – Treat as a sev-0. Emit `potso.alert.invariant_violation` with
  the breach details, stop payout processing, and page both treasury and core
  engineering. Roll back the epoch or patch the reward config before resuming
  settlements.

Keep this reference current so these playbooks are ready to use the moment
the underlying metrics get wired into the real evidence/penalty/reward code
paths (see the caveat at the top of this document).

## Runbooks

### Changing the emission rate

1. Update `EmissionPerEpoch` under `[potso.rewards]` in the target
   environment's TOML config (see `config/prod.toml`).
2. Present the diff to governance for approval. Capture the ticket ID.
3. Deploy the updated config to validators and reload the service.
4. Confirm the change took effect by watching `potso_epoch_pool` on the
   POTSO Overview dashboard at the next epoch rollover.

### Responding to cap breach alerts

1. When `POTSOEmissionCapApproach` fires, open the Emissions & Caps dashboard and inspect `potso_epoch_pool` vs `potso_rewards_sum`.
2. Confirm whether penalties or unexpected dust accumulation drove the delta by reviewing `potso_penalty_applied_total` and `potso_rounding_dust`.
3. If the configured `EmissionPerEpoch` is wrong for current conditions, coordinate with governance on a corrected value (see above).
4. If penalties exhausted the pool, coordinate with compliance to review recent evidence.
5. Document the investigation in the incident tracker.
