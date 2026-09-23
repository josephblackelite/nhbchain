# Network Security Playbook

This page maps the network surfaces of a node deployment to the controls the code provides. Detailed settings are in [transport.md](./transport.md), [network-hardening.md](./network-hardening.md), [api-auth.md](./api-auth.md) and [handshake.md](./handshake.md).

## Network surfaces

| Surface | Default address | Protection in code |
| --- | --- | --- |
| Peer-to-peer | `ListenAddress` (`127.0.0.1:6001` in the shipped `config.toml`; a config file the node creates on first run gets `:6001`, and an empty value makes the p2p server listen on `:0`, an OS-chosen port: `createDefault` in `config/config.go`, `NewServer` in `p2p/server.go`) | Signed handshake with replay guard and peer reputation. The `p2p` package does not use TLS, so peer traffic is plain TCP authenticated at the handshake, not encrypted. |
| Node JSON-RPC, WebSocket and gRPC (`pos.v1.Realtime`) | `RPCAddress` (`127.0.0.1:8545` in the shipped `config.toml`; `:8080` in a first-run config) | TLS, optional mutual TLS, bearer JWT or client certificate for protected methods, client allowlist, rate limits, query admission pool, WebSocket stream caps. |
| API gateway (`cmd/gateway`) | `listen` (`:8080` if unset) | TLS, optional mutual TLS, bearer JWT with route scopes on the authenticated routes and on the `/rpc` compatibility endpoint (can be disabled with `auth.enabled: false`, and bypassed on `auth.optionalPaths` with `auth.allowAnonymous`; see [transport.md](./transport.md)), per-route rate limits. |
| Prometheus metrics (`cmd/nhb`, `cmd/consensusd`) | `NHB_METRICS_ADDR` environment variable; no listener when it is unset | None: `/metrics` is unauthenticated, and the node logs a warning when the address is not loopback (`observability/metrics_server.go`). |
| Consensus daemon gRPC (`cmd/consensusd`) | `-grpc` flag, default `127.0.0.1:9090` | `[network_security]` (`buildConsensusServerSecurity`, `cmd/consensusd/main.go`): requires a shared secret and/or client-certificate authentication; server TLS is 1.2 or later, with `RequireAndVerifyClientCert` when `ClientCAFile` is set; plaintext only with `AllowInsecure = true`, the `-allow-insecure` flag and a loopback listener. |
| p2p daemon gRPC (`cmd/p2pd`) | `-grpc` flag, default `127.0.0.1:9091` | `[network_security]`: TLS, optional client certificates with an allowed-common-name list, and/or a shared secret. |

## Internal p2pd gRPC bridge

`network.BuildServerSecurity` (`network/security.go`) builds the p2pd server credentials from `[network_security]`:

- At least one authenticator is required: a shared secret (`SharedSecret`, `SharedSecretFile` or `SharedSecretEnv`, sent in the `authorization` metadata header unless `AuthorizationHeader` says otherwise) and/or client-certificate authentication (`ClientCAFile`, which requires `ServerTLSCertFile` and `ServerTLSKeyFile` and enables `RequireAndVerifyClientCert`, with `AllowedClientCommonNames`).
- Server TLS uses TLS 1.2 or later.
- Without TLS material, plaintext is possible only when `AllowInsecure = true` in the config, the `-allow-insecure` flag is passed, and the listener is on a loopback address.
- Reads are authenticated too unless `AllowUnauthenticatedReads = true`.

## Guidance

These are deployment suggestions, not behavior the code enforces:

- Allow the P2P, RPC and gateway ports only from the networks that need them, and keep the internal gRPC ports on loopback or a private network.
- Because peer traffic is not encrypted, do not rely on the P2P port for confidentiality.
- Keep validators, gateways and data stores in separate network segments, and require a VPN or jump host for administrative access.
- Enable the metrics listener (`NHB_METRICS_ADDR`; it is off by default) on a loopback or private address and collect the node's Prometheus metrics, including `nhb_p2p_handshakes_total`, `nhb_p2p_peer_score` and `nhb_p2p_peer_misbehavior`, and alert on unusual values.
- If a validator key may be compromised, stop the validator and coordinate with the other validators.
- Review firewall rules on a regular schedule.
