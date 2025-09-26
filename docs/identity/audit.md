# Identity Audit & Compliance

Identity operations on NHBChain are intentionally observable so that custodians, gateways, and regulators can reconstruct user journeys without accessing raw PII. This note describes the emitted event streams, retention guidance, and recommended review procedures.

## Event Streams

| Event | Purpose | Payload Highlights |
| --- | --- | --- |
| `identity.alias.set` | First-time alias registration. | `alias`, `address` |
| `identity.alias.renamed` | Alias string changed for an existing owner. | `old`, `new`, `address` |
| `identity.alias.avatarUpdated` | Avatar reference updated. | `alias`, `address`, `avatarRef` |
| `claimable.created` | Pay-by-email claimable funded. | `id`, `payer`, `token`, `amount`, `recipientHint`, `deadline` |
| `claimable.claimed` | Claimable settled to a recipient. | `id`, `payer`, `payee`, `token`, `amount`, `recipientHint` |
| `claimable.cancelled` / `claimable.expired` | Funds returned to the payer. | `id`, `payer`, `token`, `amount` |

All events are appended to the node state log and exposed via:

* JSON-RPC `events_stream` (see Observability docs) – suited for real-time ingestion.
* Block logs – every committed block includes emitted events in execution order.
* Gateway webhooks – operators can relay selected events to merchants or compliance tooling.

## Retention & Access

* **Node state** – full nodes implicitly retain the entire event log; archival nodes should keep at least 18 months to satisfy typical KYC/AML retention windows.
* **Gateway logs** – store email verification records for 18 months. Hashes only; purge on DSAR unless subject to legal hold.
* **Wallet telemetry** – avoid storing raw emails. Persist claimable IDs, hint hashes, and timestamps instead.
* **Access control** – production RPC endpoints require bearer tokens. Restrict webhook URLs to trusted systems and sign payloads (HMAC SHA-256 recommended).

## Regulator Guidance

* **PII minimisation** – on-chain data excludes plaintext email; regulators inspecting the chain see only salted hashes and alias metadata.
* **Lawful disclosure** – when compelled, operators can map salted hashes back to email addresses using gateway logs. Document the salt rotation schedule to prove uniqueness.
* **Abuse monitoring** – maintain dashboards tracking alias registrations per IP, verification retries, and claim velocity per payer. Alert on anomalies (e.g., >20 failed verifications/hour from one IP, bursts of high-value claims sharing the same hint).
* **Incident response** – if an alias is compromised, governance can freeze or reassign by submitting a governance proposal. Wallets should surface alias `UpdatedAt` and event history to end users.
* **Audit trails** – retain the RPC request metadata (caller IP, authenticated user) for identity mutations. Pair with event logs to reconstruct end-to-end changes.

For more operational controls see [identity-security-compliance.md](./identity-security-compliance.md) and the platform observability runbooks.
