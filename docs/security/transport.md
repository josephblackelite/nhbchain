# Transport Security for Gateway and RPC

> Applies to: API gateway (`cmd/gateway`), proof-of-stake RPC (`cmd/nhb`).

The gateway and POS RPC endpoints now refuse plaintext listeners by default. TLS
must be provisioned for every deployment except ephemeral local development
running on loopback interfaces.

## Gateway TLS and Mutual TLS

Configure TLS material in the gateway YAML configuration:

```yaml
security:
  tlsCertFile: /etc/nhb/gateway/tls.crt
  tlsKeyFile: /etc/nhb/gateway/tls.key
  # Optional: require client certificates signed by this CA bundle.
  tlsClientCAFile: /etc/nhb/gateway/clients-ca.pem
  allowInsecure: false
```

* `tlsCertFile` / `tlsKeyFile` must both be present. The process exits if one is
  missing.
* `tlsClientCAFile` enables mutual TLS. Clients must present a certificate that
  chains to the supplied bundle. The gateway rejects untrusted clients before
  hitting routing logic.
* `allowInsecure` only permits plaintext when **all** of the following hold:
  the process runs with `NHB_ENV=dev` *and* binds to a loopback address
  (`127.0.0.1` / `::1`). The runtime refuses to start if those conditions are
  not met. Pairing the config knob with `--allow-insecure` keeps certificates
  optional for local development, but the gateway logs a prominent warning and
  still exits when an operator attempts to use plaintext elsewhere. Production
  deployments must keep this `false`.

To launch the gateway with locally generated certificates:

```bash
openssl req -x509 -newkey rsa:4096 -keyout gateway.key -out gateway.crt \
  -days 365 -nodes -subj "/CN=gateway.local"
./bin/gateway --config /etc/nhb/gateway.yaml
```

For mutual TLS, create a client certificate signed by the same CA and supply it
with `curl`:

```bash
curl https://gateway.local/v1/lending/markets \
  --cert client.pem --key client.key \
  -H "X-Api-Key: $API_KEY" \
  -H "X-Timestamp: $(date +%s)" \
  -H "X-Nonce: $(uuidgen)" \
  -H "X-Signature: $(nhbctl sign-request ...)"
```

Header signing examples are documented in [API Replay Protection](./api-auth.md).
The HMAC window is bounded to ±120 seconds with a 10 minute nonce TTL.

## POS RPC TLS and Mutual TLS

Node operators must also provision TLS for the JSON-RPC + gRPC interface exposed
by `cmd/nhb`:

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
Audience = ["wallets", "custody"]
MaxSkewSeconds = 120
```

* Leaving `RPCAllowInsecure = false` forces TLS. The process exits if the key or
  certificate is missing.
* Set `RPCAllowInsecure = true` **only** for local development on loopback. The
  server refuses to bind to non-loopback addresses, logs a startup warning, and
  increments `nhb_security_insecure_binds_total{loopback="false"}` before
  exiting when a plaintext listener is attempted outside of loopback. Wildcard
  binds (`0.0.0.0`/`::`) are now treated as non-loopback unless paired with the
  `RPCAllowInsecureUnspecified = true` override for tightly controlled lab
  environments such as container port-forwarding, which emits a dedicated log
  message on startup when exercised.
* Populate `RPCTLSClientCAFile` to require mutual TLS from wallets, custodians,
  or proxies connecting to the RPC port.
* `RPCAllowlistCIDRs` restricts clients to specific subnets. Requests originating
  from outside these ranges are rejected before handler execution.
* `RPCProxyHeaders` opts into reverse-proxy header handling. Set
  `XForwardedFor = "single"` when terminating TLS behind a trusted proxy and list
  its address in `RPCTrustedProxies`. Leave both values as `ignore` to reject
  spoofed headers.
* `RPCJWT` enables signed bearer tokens. Operators should rotate the secret
  referenced by `HSSecretEnv` (or provide a PEM file via `RSAPublicKeyFile` when
  using RS256), and issue short-lived tokens with matching `Issuer`/`Audience`
  claims. Expiry and not-before checks apply with the configured skew window, so
  automation must refresh the bearer presented in `Authorization: Bearer <token>`
  before it expires.

After restarting the node, validate the transport:

```bash
# Verify TLS chain and negotiated protocol.
openssl s_client -connect rpc.local:8080 -servername rpc.local <<<'QUIT'
```

mTLS clients must send the client certificate pair, e.g. with `grpcurl`:

```bash
grpcurl -cacert clients-ca.pem \
  -cert client.pem -key client.key \
  -d '{}' rpc.local:8080 pos.Realtime/SubscribeFinality
```

## Replay Guard Behaviour

HMAC protected routes enforce tighter bounds:

* Maximum timestamp skew defaults to 120 seconds and is tunable via
  `swapAuth.allowedTimestampSkew` in the node configuration.
* Nonce TTL defaults to 10 minutes and is tunable via `swapAuth.nonceTTL`.
* Nonce caches: bounded LRU per API key to 65,536 entries.

These limits stop replay amplification while keeping retry budgets predictable.
Requests outside these windows return `401 Unauthorized`.
