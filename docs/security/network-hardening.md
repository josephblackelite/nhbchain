# Network Hardening Playbook

This document consolidates the controls and monitoring that keep NHBChain nodes
resilient against modern attacks. It augments the validator hardening steps in
[`release-process.md`](./release-process.md) and focuses specifically on network
boundaries, RPC services, and abuse handling.

## Threat Model

- **JSON-RPC & REST abuse** – Attackers attempt to bypass authentication,
  replay signed requests, or exhaust rate limits.
- **Distributed denial of service (DDoS)** – Floods directed at public RPC,
  WebSocket, or peer-to-peer listeners to degrade availability.
- **Credential stuffing & API key theft** – Compromise of merchant or partner
  credentials that grant transactional authority.
- **State divergence** – Malicious peers replay old handshakes or inject
  conflicting genesis data to isolate nodes.

Every mitigation below maps to at least one of these scenarios.

## RPC & Gateway Protection

1. **Mandatory authentication** – JSON-RPC methods that mutate chain state must
   sit behind HMAC authentication (`X-NHB-APIKEY`, `X-NHB-SIGNATURE`). Do not
   expose unauthenticated endpoints on public interfaces. The configuration
   examples in [`docs/networking/net-rpc.md`](../networking/net-rpc.md) show the
   required headers and expected error codes.
2. **Mutual TLS or private networking** – Validators should restrict RPC access
   to an API gateway that validates client certificates. Partners connecting
   over the internet must use mutual TLS; browser clients should go through a
   gateway that proxies requests on their behalf.
3. **Request validation** – Reject mismatched `chainId`, stale timestamps
   (±60 seconds), and nonce replays. The stock handlers already enforce these
   checks; ensure any custom middleware preserves them.
4. **Rate limiting** – Keep the defaults from `config.toml` (`RateMsgsPerSec=50`,
   `Burst=200`) for operator RPC. API gateways should additionally enforce per
   IP and per key quotas that align with commercial agreements.
5. **Audit logging** – Ship RPC access logs to your SIEM. Alert on spikes in
   401/403 responses, large payloads, or methods invoked outside policy.

## Application-Layer Safeguards

Add controls that make it impractical for attackers to tamper with request and
response payloads even if they can reach the gateway perimeter:

1. **Method allowlists** – Expose only the JSON-RPC and REST routes that your
   application requires. Deny-listing is insufficient; configure the gateway to
   reject any method not explicitly registered so fuzzing cannot surface
   development or admin handlers.
2. **Canonical signing** – Normalize headers (case, spacing) and payloads before
   computing HMAC signatures. This prevents smuggling attempts where attackers
   replay a signed payload with modified framing.
3. **Idempotency enforcement** – Require `Idempotency-Key` headers on every
   state-changing REST call. Persist recently used keys for at least 24 hours so
   replayed requests are dropped before hitting business logic.
4. **Deterministic error budgets** – Return opaque error codes to clients and
   avoid echoing raw stack traces. Detailed diagnostics belong in structured
   logs that never traverse the public network.
5. **Schema validation** – Validate JSON bodies against OpenAPI/JSON Schema
   definitions. Reject additional properties, enforce type constraints, and
   clamp numeric ranges to stop injection attempts.

## Peer-to-Peer Controls

1. **Signed handshakes** – NET-2A challenge/response prevents spoofing. Monitor
   for repeated `Record handshake violation` messages and ban offending peers.
2. **Nonce replay guard** – The in-memory nonce cache rejects replays within the
   10-minute window. Do not disable this guard; adjust the window only if
   memory pressure is measurable.
3. **Ban scoring** – Keep `BanScore=100` and `GreyScore=50` unless running in a
   permissioned lab. The defaults balance false positives against swift removal
   of abusive peers.
4. **Global throttles** – `MaxPeers=64` and `RateMsgsPerSec=50` keep aggregate
   bandwidth bounded. If you must raise these values, scale hardware and update
   DDoS protection policies accordingly.
5. **Ingress filtering** – Front validators with firewalls that allow P2P
   traffic from known peer ranges. Cloud deployments should leverage provider
   load balancers with connection limits to absorb SYN floods before they reach
   the node process.

## DDoS Resilience

- **Layer 3/4** – Use cloud provider DDoS protection (AWS Shield Advanced,
  Cloud Armor, etc.) or on-prem appliances. Always enable connection tracking
  and SYN cookies. Autoscale or rate limit at the edge rather than inside the
  node binary.
- **Layer 7** – Deploy a WAF in front of RPC/REST endpoints with rules that
  block malformed JSON, oversized payloads, and high request rates. Tie WAF
  alerts into the same incident response channel as validator telemetry.
- **Graceful degradation** – Configure gateways to shed non-critical traffic
  (public explorers, historical queries) before validator-to-validator calls.
  During sustained attacks, operators may temporarily restrict RPC to allowlisted
  partner IPs.

## Credential Hygiene

- Rotate API keys quarterly and immediately on any sign of compromise.
- Store secrets in a hardware security module (HSM), HashiCorp Vault, or
  another dedicated secret manager; never bake credentials into container
  images.
- Enable anomaly detection on authentication events—multiple failures from a
  single IP or key should trigger automated bans via `net_ban`.

## Future-Facing Improvements

- **Adaptive rate limiting** – Integrate risk scoring that raises or lowers
  per-key throughput based on behavioural history.
- **QUIC transport** – Evaluate QUIC for RPC to add native congestion control
  and mitigate head-of-line blocking during bursts.
- **Hardware attestation** – Tie validator admission to TPM-backed attestation
  (e.g., Intel SGX/DCAP, AWS Nitro) to harden against key exfiltration.
- **Centralised revocation registry** – Publish compromised peer IDs and API
  keys via on-chain governance so all operators can revoke access rapidly.

Track these items in the security backlog and revisit after each major release.

## Verification Checklist

Use this list before promoting infrastructure to production or public testnet:

- [ ] RPC endpoints enforce HMAC auth or mutual TLS and reject unauthenticated
      calls.
- [ ] Rate limiting is active at the node and edge layers.
- [ ] Gateways enforce allowlists for RPC/REST methods and reject unknown routes.
- [ ] REST write paths require idempotency keys and drop replays for 24 hours.
- [ ] JSON schemas validate inbound payloads and strip unexpected fields.
- [ ] P2P logs show successful NET-2A challenges with no unresolved violations.
- [ ] SIEM receives logs from RPC gateways, WAF, and node processes.
- [ ] DDoS mitigation plan tested within the last quarter.
- [ ] Emergency ban (`net_ban`) tested against a disposable peer and recovered
      automatically.

Maintain signed runbooks proving the checklist was executed; auditors and
governance committees may request evidence during incident reviews.
