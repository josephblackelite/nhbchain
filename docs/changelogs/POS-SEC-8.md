# POS-SEC-8: TLS enforcement and replay hardening

## Summary

* Gateway and POS RPC servers now require TLS certificates. Plaintext is only
  allowed for loopback/dev when `--allow-insecure`/`RPCAllowInsecure` are
  explicitly set.
* Mutual TLS is supported end-to-end via configurable client CA bundles.
* Replay guards tightened: timestamp skew capped at 120 seconds, nonce TTL fixed
  at 10 minutes, and caches bounded per credential.
* Added transport security runbook covering certificate provisioning and header
  signing expectations.

## Upgrade Notes

1. Provision server certificates for both gateway and RPC listeners before
   upgrading.
2. Update configuration files with the new TLS fields (`security.tls*`,
   `RPCTLSClientCAFile`, `RPCAllowInsecure`).
3. Regenerate API clients to respect the stricter replay window (120s skew,
   10m nonce TTL).
