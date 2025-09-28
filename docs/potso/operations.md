# POTSO Operations Guide

This note captures the runtime invariants and telemetry surfaced by the POTSO
heartbeat pipeline. It supplements the [abuse controls](abuse-controls.md) and
is aimed at validators and SREs operating public endpoints.

## Emission safety

Reward epochs can only be enabled when both of the following hold:

- `EpochLengthBlocks > 0`
- `EmissionPerEpoch > 0`

Configurations that attempt to enable epochs with zero emission are rejected at
validation time. This guarantees that once the module is "enabled" an emission
budget exists, eliminating the accidental "enabled but zero emissions" state.

When emissions are disabled (for example, during maintenance) the node records
any meter growth as a wash-engagement signal. These events appear on the metric
`potso_heartbeat_wash_total` labelled by epoch and participant address.

## Heartbeat abuse counters

To surface basic anti-abuse signals the node publishes the following Prometheus
series:

| Metric | Labels | Description |
|--------|--------|-------------|
| `potso_heartbeat_total` | `epoch`, `address` | Count of accepted heartbeats per address for the epoch. |
| `potso_heartbeat_rate_limited_total` | `epoch`, `address` | Heartbeats rejected because the per-address quota was exceeded. |
| `potso_heartbeat_unique_peers` | `epoch` | Number of distinct addresses that submitted heartbeats in the epoch. |
| `potso_heartbeat_avg_session_seconds` | `epoch` | Average session length derived from uptime deltas inside the epoch. |
| `potso_heartbeat_wash_total` | `epoch`, `address` | Meter increases recorded while emissions were disabled. |

Use these counters to alert on suspicious behaviour (e.g. high rate-limited
counts for a single address) and to track organic participation growth.

## Per-address rate limit

A rate limit of **1440 heartbeats per epoch** is enforced for every address by
default. This corresponds to one heartbeat per minute across a 24 hour epoch and
prevents automated wash engagement. The limit can be tuned by governance via the
engine parameters if future epochs deviate significantly from 24 hours.

Rate-limited submissions return a descriptive error to clients and increment
`potso_heartbeat_rate_limited_total`. Accepted heartbeats continue to enforce
the existing 60-second interval gate to guard against short-term replay spam.

## Dashboards

Ensure the new metrics are scraped by your Prometheus deployment. Suggested
panels include:

- **Heartbeat rate limiting**: plot `sum by (address) (increase(potso_heartbeat_rate_limited_total[5m]))` to surface abusers.
- **Unique peers per epoch**: graph `potso_heartbeat_unique_peers` to monitor
  validator participation.
- **Average session length**: track `potso_heartbeat_avg_session_seconds` to
  catch clients that drift from the expected cadence.

Combined with the existing emissions telemetry, these signals enable quick
triage when abuse or misconfiguration occurs.
