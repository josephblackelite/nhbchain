# Gateway and RPC Security Settings

## Gateway TLS enforcement

When `NHB_ENV` is not `dev`, the gateway exits at startup if any backend
endpoint is not `https://` (`cmd/gateway/main.go`, `ensureServiceConfig`).

There is no working migration path for `http://` endpoints: set every backend
endpoint to `https://` in the YAML file or the `NHB_GATEWAY_*_URL` variables.
`security.autoUpgradeHTTP` and `NHB_GATEWAY_AUTO_HTTPS` (the variable overrides
the file, and an unparseable value is a startup error) are read, but the
endpoint check in `ensureServiceConfig` runs first and exits on any `http://`
endpoint outside `dev`; in `dev`, `EnforceSecureScheme`
(`gateway/config/config.go`) leaves `http://` endpoints unchanged. The
`auto-upgraded <service> endpoint to HTTPS` log line is therefore never
reached in practice. `security.autoUpgradeHTTP` still counts as a sensitive
setting for the `auth.enabled` explicit-set rule.

The full list of gateway startup rules is in
[Gateway Overview](../gateway/overview.md#transport-security).

## Gateway rate limits behind a proxy

The gateway's rate limiter counts requests by the address the connection came
from and ignores `X-Forwarded-For` and `X-Real-IP` unless that peer is listed in
`trustedProxies` in the gateway config (see
[Rate limits](../gateway/overview.md#rate-limits)). Behind an ingress controller
or a load balancer the peer is the proxy, so a gateway started with an empty
`trustedProxies` list counts every client in one bucket per proxy address.
Ordinary traffic is then answered with `429` once that bucket is spent (the
`/rpc` compatibility endpoint allows 2 requests per second with a burst of 20
unless a `compat` entry in `rateLimits` says otherwise).

Treat these as preconditions for rolling out a gateway behind a proxy:

- List the address range of the ingress controller pods (or of the proxy hosts)
  in `trustedProxies`, and also the range of any load balancer in front of the
  controller that adds its own `X-Forwarded-For` entry. Use the narrowest range
  that covers those proxies; the gateway refuses an entry that covers every
  address. The sample Helm values for `staging` and `prod` leave the list empty
  and the chart refuses to render an ingress-enabled gateway until it is filled
  in (`--set 'trustedProxies={<range>}'` or the values file).
- The proxy must set or append the client address in `X-Forwarded-For`. The
  gateway takes the rightmost entry that is not a listed proxy and consults
  `X-Real-IP` only when there is no `X-Forwarded-For`, so a proxy that sets only
  `X-Real-IP` is not enough.
- The proxy must see the real client address. On Kubernetes that usually means
  keeping the source address on the controller's Service
  (`externalTrafficPolicy: Local`) or enabling the PROXY protocol between the
  load balancer and the controller; otherwise every client still arrives from
  the same address.
- After the rollout, confirm that two different clients are limited
  independently (the gateway's start-up log line about an empty
  `trustedProxies` list should be absent).

## RPC hardening (`cmd/nhb`)

These `config.toml` settings apply to the node's JSON-RPC server
(`rpc/http.go`, wired in `cmd/nhb/main.go`).

- **Client allowlist.** `RPCAllowlistCIDRs` restricts callers to the listed
  networks. A caller outside the list gets HTTP `403` (`client address not
  allowed`). An empty list allows everyone.
- **Reverse-proxy headers.** `RPCProxyHeaders.XForwardedFor` and
  `RPCProxyHeaders.XRealIP` accept `ignore` (the default) or `single`. With
  `ignore`, a request that carries the header is rejected with `403`. With
  `single`, the header is used only when the request comes from a trusted proxy
  and contains exactly one address (`X-Real-IP` must not contain a comma). A proxy
  is trusted only when the peer address is listed in `RPCTrustedProxies`.
  `RPCTrustProxyHeaders = true` does not trust any peer by itself: with an empty
  `RPCTrustedProxies` the node logs a warning at start and honours no forwarded
  address. A header from an untrusted peer is rejected with `403`.
  `RPCTrustedProxies` entries are matched as exact IP addresses, not CIDR ranges,
  and a forwarded value that is not an IP address is refused with `403`
  (`NewServer`, `resolveClient`, `parseClientAddr`, `isTrustedProxy` in
  `rpc/http.go`).
  **Behind the gateway.** `POST /v1/transactions/send` and the escrow lookup
  `GET /v1/consensus/wallet/escrows/{escrowID}` always set
  `X-Forwarded-For` on each request to the node (the client's own header value,
  if any, followed by the remote address; `forwardedForValue` in
  `gateway/routes/transactions.go`).
  With the default `ignore`, the node therefore answers those routes with `403`
  (`X-Forwarded-For header is not permitted`). To accept them, set
  `RPCProxyHeaders.XForwardedFor = "single"` and list the gateway's address in
  `RPCTrustedProxies`. In `single` mode a
  request whose header already holds a chain of several addresses, for example
  because the caller sent its own `X-Forwarded-For` through the gateway, is
  rejected (`X-Forwarded-For must contain exactly one address`).
- **JWT.** `[RPCJWT]` with `Enable`, `Alg` (`HS256` by default, or `RS256`),
  `HSSecretEnv` (name of the environment variable holding the HS256 secret),
  `RSAPublicKeyFile`, `Issuer` (required), `Audience` (at least one required) and
  `MaxSkewSeconds` (default 30 seconds of leeway). Tokens must carry a matching
  `iss` and `aud` and be unexpired. The server refuses to start with JWT
  disabled unless `RPCTLSClientCAFile` is set. Methods that need authentication
  (for example `nhb_sendTransaction`, `net_peers`, `p2p_peers`, `net_dial`,
  `net_ban`) reply `401` without a valid token. Static bearer strings are not
  accepted; callers need a JWT signed by the configured key (the CLIs read it
  from `NHB_RPC_TOKEN`). `nhb-cli rpc-token` mints an HS256 token for it, run on
  the node host: the secret is read from `NHB_RPC_JWT_SECRET` or, with
  `--secret-stdin`, from standard input; `--ttl` (default 10m, at most 24h),
  `--issuer` (default `nhb-rpc`) and `--audience` (default `wallets`) must match
  `[RPCJWT]` (`cmd/nhb-cli/rpc_token.go`).
- **Mutual TLS.** Setting `RPCTLSClientCAFile` requires client certificates at the
  TLS layer. For methods that need authentication the node then accepts either a
  verified client certificate or a valid JWT.
- **Plaintext.** Without `RPCTLSCertFile`/`RPCTLSKeyFile`, the server starts only
  if `RPCAllowInsecure = true`, and then only on a loopback address
  (`RPCAllowInsecureUnspecified = true` also treats `0.0.0.0`/`::` as loopback).
- **Retired methods.** `sync_snapshot_export`, `sync_snapshot_import`,
  `pos_sweepVoids`, `potso_reward_claim`, `reputation_verifySkill` and
  `tx_setSponsorshipEnabled` answer HTTP 410 (code `-32060`, `codeMethodDisabled`;
  `rpc/sync_handlers.go`, `rpc/http.go`, `rpc/potso_reward_handlers.go`,
  `rpc/reputation_handlers.go`); other methods that acted on an unsigned caller
  are disabled the same way. See
  [Snapshot onboarding](../validators/snapshot-onboarding.md) for how a new
  validator is brought up.
- **WebSocket and gRPC streams.** `RPCWebSocketOrigins` (host patterns, matched with
  `path.Match`; empty allows any origin), `RPCWebSocketMaxConnections` (default
  256) and `RPCWebSocketMaxPerIP` (default 8) bound the streams, the gRPC
  finality stream included; an IPv6 client counts as its /64. A WebSocket client
  over either cap gets HTTP `429` (`too many open streams`), a gRPC client the
  status `ResourceExhausted` (`rpc/ws.go`). `RPCAllowlistCIDRs` applies to the
  gRPC stream as well.
- **Read-query pool.** Expensive public reads are admitted through a small pool;
  see [RPC query limits](./rpc-query-limits.md).
- **Rate limits.** See [Validator Operations Runbook](./validator-runbook.md#rpc-hardening-and-transaction-quotas).

## Swap HMAC authentication

Public swap RPC methods are authenticated with HMAC when `[RPCSwapAuth]` defines
`Secrets` (a table of partner key to secret). The block name is `RPCSwapAuth` in
`config.toml`:

| Key | Default | Maximum |
| --- | ------- | ------- |
| `AllowedTimestampSkewSeconds` | 120 | 120 |
| `NonceTTLSeconds` | 600 | 600 |
| `NonceCapacity` | 4096 | 65536 |

`RateLimitWindowSeconds` and `PartnerRateLimits` (partner key to request count)
set per-partner limits. When any secret is configured the node requires
`[RPCSwapAuth.Persistence]` with `Backend = "leveldb"` and a `LevelDBPath`
(relative paths are resolved under `DataDir`), and exits otherwise. `config.Load`
also refuses `NetworkName = "mainnet"` without any `RPCSwapAuth.Secrets`.

`PartnerProviders` (partner key to a list of provider names) says whose vouchers
each partner may read back with `swap_voucher_get`, `swap_voucher_list` and
`swap_voucher_export`. Names are compared trimmed and in lower case, as the
provider allow-list compares them; `"*"` lets a key read every provider. A key
with no entry reads no voucher at all, and the node logs one configuration
warning per such key at start-up. `config.Load` refuses a `PartnerProviders` key
that is not in `Secrets` and a list with an empty provider name. Another
provider's voucher is answered exactly as one that does not exist.
