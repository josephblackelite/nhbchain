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

```toml
[RPCRouteRateLimits."nhb_sendTransaction"]
MaxTxPerWindow = 3
MaxTxPerIP = 3
```
- Align `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout`, and
  `RPCIdleTimeout` with upstream load-balancer/ingress settings to avoid idle
  disconnects. Document the final values in the deployment checklist.
- Set `[mempool] MaxTransactions` to a value that meets throughput expectations
  without exhausting memory. Nodes default to 4,000 pending transactions unless
  you opt into an unbounded queue by pairing `AllowUnlimited = true` with
  `MaxTransactions = 0`. Smaller devnet clusters can lower the ceiling in `config.toml`. The
  `NHB_MEMPOOL_MAX_TX` variable in `examples/.env.example` is not read by any
  code and has no effect on the node.
- Store TLS material in `RPCTLSCertFile` / `RPCTLSKeyFile` or enforce mutual TLS
  between the proxy and node. Rotate certificates on the same cadence as the
  proxy tier and track expirations in monitoring.
