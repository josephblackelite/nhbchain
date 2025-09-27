# NHBChain Documentation Index

## Core Modules

* [Escrow & P2P Developer Guide](./escrow.md)
* [NHBCHAIN Escrow Gateway](../escrow/nhbchain-escrow-gateway.md)
* [Loyalty Module](./loyalty.md)
* [Staking & Delegation](./staking.md)

## Governance & Proposals

The governance module coordinates configuration changes across the network.

1. **Proposal Submission** – A proposer submits a `param.update` payload listing only allow-listed parameter keys and posts the required ZNHB deposit.
2. **Escrow Lock** – The deposit is debited from the proposer’s liquid ZNHB balance and locked under `gov/escrow/<address>` until the proposal completes.
3. **Voting Window** – Upon acceptance, the proposal enters the voting period immediately. `VotingStart` is the submission timestamp, `VotingEnd` is computed from the configured voting period, and `TimelockEnd` extends the execution window after a successful vote.
4. **Events & Indexing** – A `gov.proposed` event is emitted so wallets, dashboards, and indexers can track the proposal lifecycle from creation through timelock.

### RPC & transaction safeguards

- **Trusted proxies:** The HTTP RPC server only honours `X-Forwarded-For` when the
  remote IP is declared in `RPCTrustedProxies` and `RPCTrustProxyHeaders=true`.
  Leave the toggle off until a hardened proxy tier is in place.
- **Client quotas:** Each unique client source may submit five transactions per
  minute. Exceeding the quota yields HTTP 429 / `-32020`. Wallets and SDKs
  should treat this as a backoff signal and avoid retry storms.
- **Timeout/TLS posture:** `RPCReadHeaderTimeout`, `RPCReadTimeout`,
  `RPCWriteTimeout`, `RPCIdleTimeout`, `RPCTLSCertFile`, and `RPCTLSKeyFile`
  expose node-level controls that must mirror load-balancer settings. Operators
  running behind mutual TLS proxies should still set certificate paths so that
  probe tooling can exercise end-to-end TLS in lower environments.

## Identity & Username Directory

The identity subsystem introduces human-readable aliases, email discovery, avatars, and claimables for pay-by-username UX.

* [Identity Concepts & State Model](./identity.md)
* [JSON-RPC API Reference](./identity-api.md)
* [Gateway REST API](./identity-gateway.md)
* [Pay-by-Username & Email Flows](./pay-by-username.md)
* [Avatar Specification](./avatars.md)
* [CLI Usage (`nhb-cli id`)](./identity-cli.md)
* [Security, Privacy & Compliance Brief](./identity-security-compliance.md)
* [OpenAPI 3.1 Schema](./openapi/identity.yaml)
* [HTTP Examples](./examples/identity)

### 10-Minute Quickstart

1. **Register an alias** using the JSON-RPC method or `nhb-cli id register`.
2. **Link additional addresses** with `identity_addAddress` to support multi-device payouts.
3. **Upload an avatar** via the gateway and set it on-chain with `identity_setAvatar`.
4. **Bind a verified email** so friends can pay you even before knowing your alias.
5. **Test pay-by-username** in a wallet by resolving your alias and sending a small transfer.
6. **Simulate pay-by-email** by creating a claimable and claiming it with a second account.

For escrow integration details, see [escrow.md](./escrow.md). Contributions and feedback are welcome via governance proposals or the
engineering forum.
