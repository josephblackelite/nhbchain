# Runbook: p2pd Failover

This runbook covers recovering `p2pd` after a failure while preserving
consensus connectivity.

## 1. Detect

* Alert triggers for missing heartbeats or gRPC health failures.
* Logs from `consensusd` showing repeated reconnect attempts.
* Prometheus alerts on sustained `nhb_network_relay_queue_dropped_total` increases or elevated queue occupancy.

## 2. Validate Environment

1. Confirm the consensus node is healthy via `grpcurl` on `consensusd`.
2. Check system resources (`cpu`, `mem`, `disk`) on the host.
3. Verify TLS material and seed configuration files are intact.

## 3. Promote Standby

If a warm standby is available:

1. Update DNS or load balancer to direct traffic to the standby `p2pd`.
2. Ensure the standby has access to the latest peerstore directory.
3. Watch consensus logs for `p2pd` reconnect success.

## 4. Restart Primary

1. Stop the failing service: `systemctl stop p2pd` (or equivalent).
2. Rotate logs and clear temporary sockets if required.
3. Start the service: `systemctl start p2pd`.
4. Tail logs for successful peer dial and relay startup.

## 5. Post-Recovery Checks

* Confirm `consensusd` reports height progression.
* Run `grpcurl <p2pd>:9091 network.v1.NetworkService/ListPeers` with the issued TLS
  material to verify active peers. Use `-plaintext` only when the deployment has
  explicitly set `AllowInsecure = true` for a lab environment.
* Ensure rate limits and scoring metrics reset as expected.
* Inspect relay queue health:
  - `nhb_network_relay_queue_enqueued_total` vs `nhb_network_relay_queue_dropped_total` to confirm drop ratio is below the configured threshold.
  - `nhb_network_relay_queue_occupancy` should trend well under the configured queue size except during brief spikes.
  - Structured logs tagged `component=network_relay` emit warnings once the drop ratio breaches `network_security.RelayDropLogRatio` (default 0.1).
* Tune `network_security.StreamQueueSize` when sustained occupancy nears capacity. Increase the buffer gradually (e.g., +64) and redeploy both `p2pd` and `consensusd` so the client send queue stays aligned with the relay size.

## 6. Root Cause Analysis

* Review recent deployments or configuration changes.
* Inspect TLS certificate validity and expiry.
* Audit seed registry availability and latency.

Document findings and follow up with any configuration or automation updates.
