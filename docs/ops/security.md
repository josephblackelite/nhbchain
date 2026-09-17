# Gateway TLS Enforcement

The gateway refuses to connect to plaintext JSON-RPC endpoints outside of the
`dev` environment. When `NHB_ENV` is set to any other value, service endpoints
must use `https://` URLs.

For operators that are in the process of migrating endpoints, set one of the
following configuration options to automatically upgrade existing `http://`
values to HTTPS:

- Set `security.autoUpgradeHTTP: true` in the gateway configuration file.
- Export `NHB_GATEWAY_AUTO_HTTPS=true` in the gateway environment.

When auto-upgrade is enabled the gateway will transparently rewrite the scheme
before proxying requests. This keeps production safe by enforcing TLS while
avoiding downtime during transition periods.

## RPC Authentication Hardening

Nodes now support multiple layers of RPC hardening beyond TLS:

* **Client allowlists.** Populate `RPCAllowlistCIDRs` in `config.toml` to restrict
  inbound connections to trusted subnets. Requests originating outside the list
  are rejected before handler execution.
* **Reverse proxy headers.** Only set `RPCProxyHeaders.XForwardedFor` or
  `RPCProxyHeaders.XRealIP` when the node sits behind a trusted proxy whose IP is
  listed in `RPCTrustedProxies`. The default (`ignore`) causes spoofed headers to
  fail the request.
* **JWT bearer tokens.** Configure `RPCJWT` with either an environment-derived
  HMAC secret (`HSSecretEnv`) or an RSA public key (`RSAPublicKeyFile`). Issued
  tokens must present matching `iss`/`aud` claims and remain within the
  configured skew window. Rotate signing material via your secret manager and
  mint short-lived JWTs for callers. Clients should fetch a fresh token from the
  issuer (for example by populating an `NHB_RPC_TOKEN` environment variable at
  start-up) and refresh before expiry—the node no longer accepts static bearer
  strings.
* **Mutual TLS.** Set `RPCTLSClientCAFile` to require client certificates from
  wallets, custodians, or gateways. When mTLS is enabled the node accepts either
  a valid client certificate or a JWT that satisfies the configured policy.

Swap HMAC authentication retains the existing defaults (±120 seconds skew,
10-minute nonce TTL) but can be tuned via the `swapAuth` block in `config.toml`
to match partner integration requirements. Keep nonce caches large enough to
handle burst traffic while preventing replay amplification.
