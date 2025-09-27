# Alert Runbook

This runbook documents how to triage and resolve the primary SLO alerts emitted by the
NHB observability stack.

## ServiceErrorBudgetBurn

**Alert source**: `ops/prometheus/rules/slo.rules.yml`

**Trigger**: Five minute rolling error ratio for a service exceeds 2% for ten minutes.

**Dashboards**:
- Grafana &rarr; *NHB Services Overview* panel "5m Error Budget Consumption"
- Tempo trace search filtered by `service.name`

**Checks**:
1. Confirm the alert is real by correlating with `spanmetrics_calls_total` and `spanmetrics_error_count` for the affected service.
2. Inspect recent deployments or configuration changes for the service.
3. Use the Gateway request logs in Loki filtered by `service` and `trace_id` to pinpoint failing requests.

**Mitigations**:
- Roll back the most recent deployment if errors map to a release.
- Increase rate limits via Gateway configuration if the service is overloaded.
- If the upstream dependency is degraded, fail over to a healthy region and annotate the incident channel.

## ServiceLatencyRegression

**Alert source**: `ops/prometheus/rules/slo.rules.yml`

**Trigger**: p95 latency computed from the spanmetrics histogram stays above 750ms for ten minutes.

**Dashboards**:
- Grafana &rarr; *NHB Services Overview* panel "5m p95 Request Latency"
- Grafana Explore &rarr; Tempo traces with the slowest spans

**Checks**:
1. Validate latency regression using Grafana by drilling into the service time series.
2. Identify the span(s) contributing to the tail via Tempo exemplars.
3. Review infrastructure dashboards (CPU, memory, disk) for resource saturation.

**Mitigations**:
- Enable debug-level logging temporarily to capture additional context; remember to revert.
- Scale out the service if CPU saturation is observed.
- Engage dependent teams if external APIs are responsible for the slow spans.

## General Guidance

- Always acknowledge alerts in Alertmanager or PagerDuty to avoid duplicate pages.
- Update the incident timeline in `#sre-alerts` with findings and actions taken.
- After mitigation, verify that the error and latency SLO metrics trend back toward baseline.
- File a post-incident review if the alert persisted for more than 30 minutes or recurred within a week.
