# Transport Security for Gateway and RPC

> Applies to: the API gateway (`cmd/gateway`) and the node's JSON-RPC and gRPC listener (`cmd/nhb`, `rpc/http.go`).

Both listeners require TLS unless plaintext is explicitly allowed for local development, and both enforce TLS 1.2 or later when TLS is on.

## Gateway TLS and mutual TLS

Configure TLS in the gateway YAML file (`gateway/config/config.go`, `cmd/gateway/main.go`):

```yaml
listen: ":8080"
security:
  tlsCertFile: /etc/nhb/gateway/tls.crt
  tlsKeyFile: /etc/nhb/gateway/tls.key
  # Optional: require client certificates signed by this CA bundle.
  tlsClientCAFile: /etc/nhb/gateway/clients-ca.pem
  allowInsecure: false
  autoUpgradeHTTP: false
```

Pass the file with `-config <path>`. Relative TLS paths are resolved against the directory of the config file.

* `tlsCertFile` and `tlsKeyFile` must both be set. Setting one without the other, or only `tlsClientCAFile`, makes startup fail (`security.tlsCertFile and security.tlsKeyFile must both be provided when enabling TLS`).
* `tlsClientCAFile` turns on mutual TLS: the listener uses `RequireAndVerifyClientCert` against that bundle, so a client without a certificate that chains to it cannot connect.
* Without a certificate and key the gateway refuses to start unless `allowInsecure: true` or the `-allow-insecure` flag is given. Both forms additionally require `NHB_ENV=dev` (case-insensitive) and a `listen` address whose host is `localhost` or a loopback IP; otherwise the process exits. The default `listen` is `:8080`, which has an empty host and is not loopback, so a local plaintext run needs an explicit address such as `127.0.0.1:8080`. Startup prints a warning when it runs without TLS.
* Upstream service endpoints (`lendingd`, `swapd`, `governd`, `consensusd`) must use `https://` unless `NHB_ENV=dev`. With `autoUpgradeHTTP: true` (or `NHB_GATEWAY_AUTO_HTTPS=true`) `http://` endpoints are rewritten to `https://` instead of being rejected.

Client authentication on the gateway is, by default, a bearer JWT (`Authorization: Bearer <token>`), verified with `auth.hmacSecret` and applied only to the routes marked `RequireAuth` in `cmd/gateway/main.go` (`/v1/lending`, `/v1/swap`, `/v1/gov`, `/gov.v1.Msg` and `/v1/transactions`). The routes `/gov.v1.Query`, `/v1/consensus` and `/consensus.v1.ConsensusService` are not behind the JWT check. The token must be HMAC-signed (any `HS256`, `HS384` or `HS512` token is accepted; `parseToken` checks only that the method is HMAC), and issuer and audience are checked only when `auth.issuer` and `auth.audience` are non-empty. Clock skew defaults to and is capped at 2 minutes. Route scopes (`lending`, `swap`, `gov`) are read from the `scope` claim (`auth.scopeClaim`), and `/v1/transactions` needs a valid token but no scope (`gateway/middleware/auth.go`, `cmd/gateway/main.go`).

The `/rpc` JSON-RPC compatibility endpoint (mounted only when the compatibility mode is enabled: `-compat-mode` flag or `NHB_COMPAT_MODE`, resolved by `compat.ShouldEnable`) goes through the same authenticator as the routes above and a rate limit of its own (`compat`; 2 requests per second, burst 20, unless the configuration defines a `compat` entry). It carries no route scope of its own; instead each call is held to the scope of the service it maps to: `lending` for `lendingd`, `swap` for `swapd` and `gov` for `governd`, and none for `consensusd` (`compat.ScopeGuard` in `gateway/compat/compat.go`, wired in `cmd/gateway/main.go`). A call without the scope gets JSON-RPC error `-32004` (`insufficient scope`). A request that was not authenticated because authentication is off or the path is one of the open paths below is not held to scopes.

Authentication can be turned off or bypassed by configuration:

* `auth.enabled: false` makes the middleware pass every request through without any token check. If `auth.enabled` is omitted it defaults to `true`, but the gateway refuses to start (`ErrAuthEnabledNotConfigured`) when the config sets a TLS certificate, key or client CA, or `autoUpgradeHTTP`, and does not set `auth.enabled` explicitly (`gateway/config/config.go`, `cmd/gateway/main.go`).
* `auth.allowAnonymous: true` (which must be set explicitly, and requires at least one `auth.optionalPaths` entry beginning with `/`) lets requests whose path starts with any `optionalPaths` prefix skip the token check.
* With authentication enabled and an empty `auth.hmacSecret`, every token is rejected as invalid.

The gateway does not use the API-key scheme in [api-auth.md](./api-auth.md). For example, with mutual TLS:

```bash
curl https://gateway.example/v1/lending/markets \
  --cert client.pem --key client.key --cacert server-ca.pem \
  -H "Authorization: Bearer $TOKEN"
```

## Node RPC TLS and mutual TLS

The JSON-RPC, WebSocket and gRPC endpoints share one listener, configured in the node's TOML file (`config/config.go`):

```toml
RPCAllowInsecure = false
RPCTLSCertFile = "/etc/nhb/rpc/rpc.crt"
RPCTLSKeyFile = "/etc/nhb/rpc/rpc.key"
RPCTLSClientCAFile = "/etc/nhb/rpc/clients-ca.pem"
RPCAllowlistCIDRs = ["10.0.0.0/24", "192.168.10.0/24"]

[RPCProxyHeaders]
XForwardedFor = "single"
XRealIP = "ignore"

[RPCJWT]
Enable = true
Alg = "HS256"
HSSecretEnv = "NHB_RPC_JWT_SECRET"
Issuer = "nhb-rpc"
Audience = ["wallets"]
MaxSkewSeconds = 120
```

* Certificate and key: if both are empty and `RPCAllowInsecure` is false the server returns `TLS is required for RPC server; configure certificates or enable AllowInsecure`; if only one is set it returns `both TLS certificate and key paths must be provided`.
* `RPCAllowInsecure = true` permits plaintext only on a loopback listener. On any other address the server logs that it is refusing to start, returns `plaintext RPC is only permitted on loopback interfaces`, and has already incremented `nhb_security_insecure_binds_total{service="rpc",loopback="false"}`. A loopback plaintext bind increments the same counter with `loopback="true"`. Unlike the gateway, this does not depend on `NHB_ENV`.
* Wildcard binds (`0.0.0.0`, `::`) count as non-loopback unless `RPCAllowInsecureUnspecified = true` (intended for lab port-forwarding); the server then logs a message saying so.
* `RPCTLSClientCAFile` enables mutual TLS (`RequireAndVerifyClientCert`). A request presenting a verified client certificate satisfies the authentication check for methods that require it.
* `RPCAllowlistCIDRs` restricts client IPs for the JSON-RPC handler and the WebSocket endpoints (HTTP 403 otherwise). It does not apply to gRPC calls to `pos.v1.Realtime`, which are served before that check.
* `[RPCProxyHeaders]` accepts `ignore` (default; a request carrying the header is rejected) or `single` (one address, honoured only when the request comes from a peer in `RPCTrustedProxies`; a forwarded header from any other peer is refused, and `RPCTrustProxyHeaders = true` with an empty `RPCTrustedProxies` trusts nobody).
* `[RPCJWT]`: when enabled, `Issuer` and at least one `Audience` are required; `Alg` is `HS256` (default, secret read from the environment variable named by `HSSecretEnv`) or `RS256` (`RSAPublicKeyFile`); expiry and not-before are validated with `MaxSkewSeconds` of leeway (30 seconds if unset). Either JWT must be enabled or a client CA configured, or the server will not start. If the verifier cannot be built (for example the secret environment variable is empty), the server still starts and every method that requires authentication returns `JWT authentication misconfigured` with HTTP 401. Clients send `Authorization: Bearer <token>`.

The repository's `config.toml` keeps the node on `127.0.0.1:8545` with `RPCAllowInsecure = true` and no certificate, and relies on a TLS-terminating proxy in front of it (see the comment in that file).

After restarting, check the transport:

```bash
# Verify the TLS chain and negotiated protocol.
openssl s_client -connect rpc.example:8545 -servername rpc.example </dev/null
```

The only gRPC service registered is `pos.v1.Realtime` (`SubscribeFinality`). The other POS gRPC services are not registered. Because the server does not expose reflection, pass the proto file to `grpcurl`:

```bash
grpcurl -cacert server-ca.pem -cert client.pem -key client.key \
  -import-path proto -proto pos/realtime.proto \
  -d '{}' rpc.example:8545 pos.v1.Realtime/SubscribeFinality
```

## Replay guard for HMAC-signed swap requests

The swap methods that use API-key HMAC signing (see [api-auth.md](./api-auth.md)) are limited by `[RPCSwapAuth]`:

* `AllowedTimestampSkewSeconds`: default and maximum 120 seconds.
* `NonceTTLSeconds`: default and maximum 600 seconds.
* `NonceCapacity`: default 4096 and maximum 65536 nonces per API key.

Requests outside these bounds are rejected with HTTP 401.
