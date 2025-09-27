# Validator Operations Runbook

## Clock Synchronization Guardrails

Validators must keep their host clocks disciplined to avoid timestamp drift. Blocks are
accepted only when their timestamps fall within a narrow window derived from the last
accepted block and the local wall clock. Set `BlockTimestampToleranceSeconds` in the
validator configuration (and governance policy) to the agreed tolerance—5 seconds by
default—and ensure every validator applies the same value. Nodes reject blocks that
arrive more than the configured tolerance ahead of the local clock or older than the
last accepted timestamp, so chrony/ntpd monitoring should alert on offsets approaching
half of the tolerance.

### Operational Checklist

- Monitor `chronyc tracking` or `timedatectl status` at least once per hour and alert if
  drift exceeds 2 seconds.
- Audit the deployed `config.toml` to confirm `BlockTimestampToleranceSeconds` matches
  the network governance policy before rotating validators.
- Investigate `block timestamp outside allowed window` errors immediately; they
  indicate either a validator clock skew or a faulty block producer replaying stale
  heights. Validate the last accepted timestamp from state before re-enabling signing.

## RPC hardening and transaction quotas

- Populate `RPCTrustedProxies` with the exact IPs of load balancers or ingress
  controllers that should be allowed to forward client IPs. Requests from other
  sources will ignore `X-Forwarded-For` headers even when proxied.
- Keep `RPCTrustProxyHeaders` disabled until the proxy tier is locked down and
  actively strips inbound forwarding headers. Only enable it after verifying the
  chain of custody in staging.
- Review the enforced per-source quota (five transactions per minute). Update
  tooling to surface HTTP 429 / `-32020` responses with retry guidance instead
  of blindly retrying.
- Align `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout`, and
  `RPCIdleTimeout` with upstream load-balancer/ingress settings to avoid idle
  disconnects. Document the final values in the deployment checklist.
- Set `[mempool] MaxTransactions` to a value that meets throughput expectations
  without exhausting memory. The sample configs ship with 5,000; smaller devnet
  clusters can lower this and update `NHB_MEMPOOL_MAX_TX` in the examples
  workspace for parity.
- Store TLS material in `RPCTLSCertFile` / `RPCTLSKeyFile` or enforce mutual TLS
  between the proxy and node. Rotate certificates on the same cadence as the
  proxy tier and track expirations in monitoring.
