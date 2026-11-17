# POS-SEC-8: TLS enforcement and replay hardening

## Summary

* Gateway and POS RPC servers now require TLS certificates. Plaintext is only
  allowed when explicitly enabled, and only on a loopback bind:
  * Gateway: `--allow-insecure`/`security.allowInsecure` requires both
    `NHB_ENV=dev` and a loopback listen address
    (`cmd/gateway/main.go`).
  * Node: `RPCAllowInsecure` does not require `NHB_ENV`, but the RPC server
    refuses to start plaintext on any non-loopback bind ("plaintext RPC is only
    permitted on loopback interfaces"; `rpc/http.go`). An unspecified bind
    (`0.0.0.0`) is treated as loopback only with `RPCAllowInsecureUnspecified`.
* Mutual TLS is supported end-to-end via configurable client CA bundles.
* Replay guards tightened: timestamp skew capped at 120 seconds, nonce TTL capped
  at 10 minutes, and the nonce cache capacity is bounded.
* Added transport security runbook covering certificate provisioning and header
  signing expectations.

## Upgrade Notes

1. Provision server certificates for both gateway and RPC listeners before
   upgrading.
2. Update configuration files with the new TLS fields (gateway `security.tlsCertFile`/`tlsKeyFile`/`tlsClientCAFile`;
   node `RPCTLSCertFile`, `RPCTLSClientCAFile`, `RPCAllowInsecure`).
3. Regenerate API clients to respect the stricter replay window (120s skew,
   10m maximum nonce TTL).
