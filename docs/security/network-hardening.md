# Network Hardening Playbook

This page lists the network-facing controls the node implements, with the configuration keys that drive them, followed by deployment guidance and a checklist. Transport (TLS/mTLS) settings are in [transport.md](./transport.md); HMAC request signing is in [api-auth.md](./api-auth.md); the peer handshake is in [handshake.md](./handshake.md).

## JSON-RPC server controls (`rpc/http.go`)

1. **Transport.** The server refuses to start without a TLS certificate and key unless `RPCAllowInsecure = true`, and even then it only serves plaintext on a loopback listener (see [transport.md](./transport.md)).
2. **Client allowlist.** `RPCAllowlistCIDRs` (CIDRs or single IPs) restricts which client addresses the JSON-RPC handler and the `/ws/pos/finality` and `/ws/explorer` WebSocket endpoints accept. Others get HTTP 403 (`client address not allowed`). The list is empty by default, which allows everyone. It is not applied to the gRPC `pos.v1.Realtime` service, which is dispatched before the JSON-RPC handler (`grpcHandler` in `rpc/http.go`).
3. **Authentication.** Some methods require either a verified TLS client certificate or a bearer JWT (`Authorization: Bearer <token>`); others are open reads. Methods that require it include `nhb_sendTransaction`, `tx_setSponsorshipEnabled`, `net_ban`, `sync_snapshot_export`, `sync_snapshot_import` and `potso_reward_claim`. The server will not start unless JWT is enabled (`[RPCJWT] Enable = true`) or a client CA is configured (`RPCTLSClientCAFile`). JWT validation: HS256 (secret read from the environment variable named by `HSSecretEnv`) or RS256 (`RSAPublicKeyFile`); issuer must match `Issuer`; the token's audience must match one of `Audience`; expiry and not-before are checked with a leeway of `MaxSkewSeconds` (30 s if unset). Failures return HTTP 401.
4. **Swap HMAC.** When `[RPCSwapAuth].Secrets` is set, the swap methods in `isPublicSwapMethod` also require an API key and HMAC signature; per-key quotas come from `PartnerRateLimits`.
5. **Client IP handling.** `X-Forwarded-For` and `X-Real-IP` are rejected by default (`[RPCProxyHeaders]` mode `ignore`). Mode `single` accepts exactly one address, and only when the request comes from a peer listed in `RPCTrustedProxies` (or `RPCTrustProxyHeaders = true`).
6. **Request size.** Bodies over 1 MiB are rejected with HTTP 413.
7. **Rate limiting.** A fixed-window counter is kept per client IP, per authenticated identity, per chain nonce and per identity+chain nonce, separately for each method. Limits come from `RPCMaxTxPerWindow`, `RPCMaxTxPerIP`, `RPCMaxTxPerIdentity`, `RPCMaxTxPerChain`, `RPCMaxTxPerIdentityChain`, per-method overrides in `RPCRouteRateLimits`, and the window `RPCRateLimitWindow` (seconds). If unset in the configuration, `config.Load` sets `RPCMaxTxPerWindow = 5` and the window to 60 seconds, and the other limits default from `RPCMaxTxPerWindow`; the repository's `config.toml` sets all five limits to 120 per 60 seconds. Exceeding a limit returns HTTP 429.

## Peer-to-peer controls (`p2p/`)

1. **Signed handshake.** Every connection must complete the handshake in [handshake.md](./handshake.md): chain ID, genesis hash, signature and nonce replay are checked, and violations ban the peer.
2. **Nonce replay guard.** 10 minute window, 100,000 entries.
3. **Reputation.** Peers are scored (`ReputationManager` in `p2p/reputation.go`): invalid block −20, spam −10, malformed message −5, heartbeat +1, uptime +2 per day, with a 10 minute decay half-life. A score at or below `-BanScore` bans the peer for the ban duration; at or below `-GreyScore` greylists it for two minutes. Defaults from `config/config.go` are `BanScore = 100` and `GreyScore = 50`. Persistent peers are never banned.
4. **Limits.** `MaxPeers` defaults to 64, `MaxInbound`/`MaxOutbound` default to `MaxPeers`, `MaxMsgBytes` to 1 MiB, and `MaxMsgsPerSecond` to 32 messages per second per peer. The shipped `config.toml` `[p2p]` section sets `RateMsgsPerSec = 50.0`, `Burst = 200.0`, `MaxPeers = 64`, `MaxInbound = 60` and `MaxOutbound = 30`.
5. **Manual ban.** The authenticated `net_ban` RPC takes `{"nodeId": "<id>", "secs": <n>}` (`secs` must not be negative) and bans a peer.

## Deployment guidance

These are suggestions for operators, not behavior the node enforces:

- Expose the RPC port only through a TLS-terminating proxy or with the node's own TLS, and restrict it with `RPCAllowlistCIDRs` or network rules. If a proxy in front of the node passes client addresses, list it in `RPCTrustedProxies` and set `[RPCProxyHeaders]` accordingly.
- Allow the P2P port only from the peers you expect, and keep the RPC port off the public interface unless it needs to be reachable.
- Keep JWT signing secrets and API-key secrets in the environment or a secret manager rather than in the configuration file. `[RPCJWT].HSSecretEnv` names the environment variable to read.
- Ship RPC access logs to your log system and alert on spikes of HTTP 401, 403 and 429.
- Track `nhb_security_insecure_binds_total` to see whether a plaintext listener was ever started.

## Verification checklist

- [ ] The RPC listener has a certificate and key configured, or is loopback-only behind a TLS proxy.
- [ ] Either `[RPCJWT] Enable = true` with a non-empty secret, or `RPCTLSClientCAFile` is set.
- [ ] `RPCAllowInsecureUnspecified` is `false`.
- [ ] `RPCTrustProxyHeaders`, `RPCTrustedProxies` and `[RPCProxyHeaders]` match the actual proxy in front of the node.
- [ ] `[RPCSwapAuth].Secrets` and a persistence backend are set where the swap methods are exposed (required for `NetworkName = "mainnet"`).
- [ ] Rate-limit values are set explicitly rather than left at defaults.
- [ ] `[p2p]` `BanScore`, `GreyScore`, `MaxPeers` and message-rate values are the intended ones.
- [ ] `net_ban` works against a disposable peer with a valid token.
