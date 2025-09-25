# POTSO Emissions Observability

The POTSO emissions dashboards give operations, treasury, and integrations
teams real-time insight into reward budgets, evidence processing, and webhook
health. Each dashboard is backed by Prometheus metrics exposed by the node and
can be imported directly into Grafana using the JSON exports stored in
`observability/grafana/dashboards/`.

## Emission schedule configuration

Emission targets are driven by a TOML schedule loaded from `config/potso-emissions.toml`. Each entry defines the epoch window, base pool, and optional decay multipliers:

```toml
[[schedule]]
start_epoch = 0
end_epoch   = 90
base_pool   = "500000000000000000000"  # 500 ZNHB expressed in wei
decay       = "linear"
decay_floor = "300000000000000000000"  # stop decaying at 300 ZNHB

[[schedule]]
start_epoch = 91
end_epoch   = 180
base_pool   = "450000000000000000000"
decay       = "step"
decay_steps = [
  { epoch = 120, pool = "400000000000000000000" },
  { epoch = 150, pool = "350000000000000000000" }
]
```

**Rules**

* Epoch ranges are inclusive on `start_epoch` and exclusive on `end_epoch`.
* Pools are encoded as decimal strings to preserve wei precision.
* When `decay = "linear"`, the pool decays evenly from `base_pool` to `decay_floor` across the window.
* When `decay = "step"`, each entry in `decay_steps` supersedes the current pool beginning at the specified `epoch`.

The node validates the schedule on boot; overlapping windows or negative pools reject configuration.

### Epoch math

Epochs advance when `emission_block_interval` blocks have elapsed. Default configuration maps one epoch to 720 blocks (≈1 hour). The scheduler applies:

```
epoch = floor((blockHeight - genesisEmissionBlock) / emission_block_interval)
```

At each rollover the next pool is fetched from the schedule. Dust from the previous epoch is added before payouts are computed, ensuring conservation of total supply.

### Cap and decay safeguards

The emission module enforces a global cap derived from the active schedule window. For each epoch:

1. Read the scheduled pool (post-decay) and add accumulated dust.
2. Clamp the result to the configured `max_epoch_cap`. If the schedule proposes a pool above the cap, `potso.emission.cap_adjustment` is logged with both values.
3. Apply decay multipliers produced by penalties (`potso_penalty_applied_total`). The decay is idempotent: `pool_after_penalties = max(decayed_pool, decay_floor)`.
4. Persist the resulting `epoch_pool_actual` and expose it via Prometheus (`potso_epoch_pool`).

When the actual pool diverges from the scheduled pool by more than 5%, the `POTSOEmissionCapApproach` alert fires to prompt investigation.

### Schedule artifacts

* **JSON snapshots** – every accepted schedule is serialised into `observability/snapshots/emission-schedule-<timestamp>.json` for audit.
* **Change logs** – `potso.audit` emits `emission_schedule_applied` entries with the diff hash, operator ID, and git commit reference used to deploy the change.

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

The corresponding Grafana JSON is stored at `observability/grafana/dashboards/potso-overview.json`. The exported screenshots in `observability/grafana/screenshots/potso-overview.png` illustrate the expected layout for runbook validation.

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

Artifacts:

* JSON export: `observability/grafana/dashboards/potso-emissions.json`
* Screenshot: `observability/grafana/screenshots/potso-emissions.png`

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

* JSON export: `observability/grafana/dashboards/potso-rewards.json`
* Screenshot: `observability/grafana/screenshots/potso-rewards.png`

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

## Runbooks

### Updating the emission schedule

1. Pull the latest schedule JSON snapshot and TOML template from `config/potso-emissions.toml`.
2. Draft changes in a feature branch and run `scripts/validate-emission-schedule.sh` to lint for overlaps or negative pools.
3. Present the diff to governance for approval. Capture the ticket ID.
4. Deploy by copying the TOML to validators, reloading the service, and confirming `potso.audit` emits `emission_schedule_applied` with the expected hash.
5. Validate dashboards by comparing against the updated screenshots and confirm `potso_epoch_pool` reflects the new pool.

### Responding to cap breach alerts

1. When `POTSOEmissionCapApproach` fires, open the Emissions & Caps dashboard and inspect `potso_epoch_pool` vs `potso_rewards_sum`.
2. Confirm whether penalties or unexpected dust accumulation drove the delta by reviewing `potso_penalty_applied_total` and `potso_rounding_dust`.
3. If the scheduled pool is incorrect, revert to the previous snapshot using the change log reference.
4. If penalties exhausted the pool, coordinate with compliance to review recent evidence and consider temporarily increasing `max_epoch_cap` (with governance approval).
5. Document the investigation in the incident tracker and update the schedule artifacts if a change was deployed.

### Webhook degradation during emissions

1. Cross-reference the Rewards Pipeline dashboard for the affected destination.
2. Use the alert playbook `POTSOFailedWebhookDelivery` for initial triage, then throttle retry workers with `scripts/potso-webhook-throttle.sh` if needed.
3. Notify external partners, share the relevant export checksums, and pause settlements until acknowledgements resume.
4. After recovery, confirm the webhook availability SLO was restored and close the incident with references to the Grafana screenshot timestamps.
