# Runbook: p2pd Failover

This runbook covers recovering `p2pd` after a failure while preserving
consensus connectivity.

## 1. Detect

* Alert triggers for missing heartbeats or gRPC health failures.
* Logs from `consensusd` showing repeated reconnect attempts.

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
* Run `grpcurl -plaintext <p2pd>:9091 network.v1.NetworkService/ListPeers` to verify
  active peers.
* Ensure rate limits and scoring metrics reset as expected.

## 6. Root Cause Analysis

* Review recent deployments or configuration changes.
* Inspect TLS certificate validity and expiry.
* Audit seed registry availability and latency.

Document findings and follow up with any configuration or automation updates.
