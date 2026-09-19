# Validator Operations Runbook

For onboarding a new validator see the
[validator onboarding guide](../validators/onboarding.md).

## Clock synchronization

Block timestamps are checked against the local clock and the previous block
(`Node.validateBlockTimestamp`, `core/node.go`):

- A block is rejected when its timestamp is later than `now + tolerance`, or
  earlier than the last accepted block's timestamp. The errors read
  `block timestamp outside allowed window: timestamp <ts> exceeds maximum <max> (now=<now> tolerance=<d>)`
  and `block timestamp outside allowed window: timestamp <ts> precedes minimum <min>`.
- The upper bound is skipped when historical blocks are being applied (sync).
- The tolerance is `BlockTimestampToleranceSeconds` in the `[governance]` section
  of `config.toml`. It defaults to 5 seconds, and `config.Load` forces 5 when
  `NetworkName` is `mainnet` or `nhbchain-1`. The value is also carried in the
  governance proposal policy.

Every validator needs a disciplined clock (for example chrony or ntpd) so its
`now` does not drift beyond the tolerance from its peers. When you see the
`block timestamp outside allowed window` error, compare the local clock with the
error's `now=` value and check the last accepted timestamp in state before
re-enabling signing.

## RPC hardening and transaction quotas

Settings are top-level keys in `config.toml` (`config/config.go`). Defaults come
from `config.Load`.

- **Trusted proxies.** `RPCTrustedProxies` lists the peer addresses whose
  `X-Forwarded-For` / `X-Real-IP` headers are honoured. `RPCTrustProxyHeaders =
  true` honours them from any peer, so leave it `false` unless the proxy tier is
  locked down. A forwarded header from an untrusted peer is rejected with `403`
  (see [Gateway and RPC Security Settings](./security.md#rpc-hardening-cmdnhb)).
- **Per-source transaction quota.** `RPCMaxTxPerWindow` (default `5`) is the
  number of `nhb_sendTransaction` requests allowed per `RPCRateLimitWindow`
  (default 60 seconds). `RPCMaxTxPerIP`, `RPCMaxTxPerIdentity` and
  `RPCMaxTxPerChain` default to `RPCMaxTxPerWindow`; `RPCMaxTxPerIdentityChain`
  defaults to the smaller of the identity and chain limits. Other methods are
  limited per source by the same limiter. A rejected call gets HTTP `429` with
  JSON-RPC code `-32020` (`transaction rate limit exceeded` or `RPC rate limit
  exceeded`). Hits are counted in `nhb_rpc_limiter_hits_total{scope,module,route}`.
- **Per-method overrides.** `RPCRouteRateLimits` is a table keyed by JSON-RPC
  method name. Each entry accepts `MaxTxPerWindow`, `MaxTxPerIP`,
  `MaxTxPerIdentity`, `MaxTxPerChain` and `MaxTxPerIdentityChain`; an unset or
  zero value falls back to the entry's `MaxTxPerWindow`, and that falls back to
  the global limit.

  ```toml
  [RPCRouteRateLimits."nhb_sendTransaction"]
  MaxTxPerWindow = 3
  MaxTxPerIP = 3
  ```

- **Timeouts.** `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout` and
  `RPCIdleTimeout` are in seconds and go straight to the Go `http.Server`; `0`
  means no timeout (the repository's `config.toml` sets all four to `0`). Match
  them to your load balancer's idle settings.
- **Mempool size.** `[mempool] MaxTransactions` defaults to 4,000 pending
  transactions; an unbounded pool needs `AllowUnlimited = true` together with
  `MaxTransactions = 0` ([Runtime Configuration Guardrails](./configuration.md#block-and-mempool-limits)).
- **TLS.** `RPCTLSCertFile` and `RPCTLSKeyFile` enable TLS on the RPC listener;
  `RPCTLSClientCAFile` additionally requires client certificates. Without
  certificates the listener runs only if `RPCAllowInsecure = true` and it is bound
  to a loopback address.
