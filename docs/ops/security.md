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
  is trusted when `RPCTrustProxyHeaders = true` (any peer) or the peer address is
  listed in `RPCTrustedProxies`. A header from an untrusted peer is rejected with
  `403`. `RPCTrustedProxies` entries are matched as exact IP addresses, not
  CIDR ranges (`NewServer`, `isTrustedProxy` in `rpc/http.go`).
  **Behind the gateway.** `POST /v1/transactions/send` always sets
  `X-Forwarded-For` on the request to the node (the client's own header value,
  if any, followed by the remote address; `gateway/routes/transactions.go`).
  With the default `ignore`, the node therefore answers that route with `403`
  (`X-Forwarded-For header is not permitted`). To accept it, set
  `RPCProxyHeaders.XForwardedFor = "single"` and list the gateway's address in
  `RPCTrustedProxies` (or set `RPCTrustProxyHeaders = true`). In `single` mode a
  request whose header already holds a chain of several addresses, for example
  because the caller sent its own `X-Forwarded-For` through the gateway, is
  rejected (`X-Forwarded-For must contain exactly one address`).
- **JWT.** `[RPCJWT]` with `Enable`, `Alg` (`HS256` by default, or `RS256`),
  `HSSecretEnv` (name of the environment variable holding the HS256 secret),
  `RSAPublicKeyFile`, `Issuer` (required), `Audience` (at least one required) and
  `MaxSkewSeconds` (default 30 seconds of leeway). Tokens must carry a matching
  `iss` and `aud` and be unexpired. The server refuses to start with JWT
  disabled unless `RPCTLSClientCAFile` is set. Methods that need authentication
  (for example `nhb_sendTransaction`, `net_dial`, `sync_snapshot_export`) reply
  `401` without a valid token. Static bearer strings are not accepted; callers
  need a JWT signed by the configured key (the CLIs read it from
  `NHB_RPC_TOKEN`).
- **Mutual TLS.** Setting `RPCTLSClientCAFile` requires client certificates at the
  TLS layer. For methods that need authentication the node then accepts either a
  verified client certificate or a valid JWT.
- **Plaintext.** Without `RPCTLSCertFile`/`RPCTLSKeyFile`, the server starts only
  if `RPCAllowInsecure = true`, and then only on a loopback address
  (`RPCAllowInsecureUnspecified = true` also treats `0.0.0.0`/`::` as loopback).
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
