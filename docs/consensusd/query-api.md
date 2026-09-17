# Consensus Query API

The consensus daemon now exposes a read-only gRPC surface that mirrors the module state the in-process services previously consumed. This allows external operators to pull consistent snapshots without reaching into node memory.

## Service definition

The `consensus.v1.QueryService` gRPC interface provides three RPCs:

| RPC | Description |
| --- | --- |
| `QueryState(namespace, key)` | Fetch a single value for the supplied namespace/path. Returns the raw bytes alongside an optional Merkle proof (proofs are reserved for a future upgrade and currently omitted). |
| `QueryPrefix(namespace, prefix)` | Streams key/value records whose key falls beneath the provided namespace prefix. |
| `SimulateTx(tx_bytes)` | Executes a transaction against a copy of the latest state and returns gas usage, total gas cost and emitted events. |

All responses are byte-oriented so callers can choose their own decoding strategy (JSON, protobuf, etc.).

## Well-known namespaces

The query router recognises the following module paths. Additional namespaces can be added in follow-up releases without breaking existing consumers.

### Lending

| Path | Semantics |
| --- | --- |
| `lending/markets` | Returns a JSON array of `lending.Market` definitions. |
| `lending/positions/{address}` | Returns JSON describing the borrower’s open positions across all pools. Addresses may be Bech32 (`nhb1…`) or 20-byte hex. |

### Swap

| Path | Semantics |
| --- | --- |
| `swap/vouchers/{providerTxId}` | Returns the stored `swap.VoucherRecord` for the provider transaction identifier. |
| `swap/oracles` | Returns the current provider/oracle status JSON (including feed health) mirrored from the in-memory swap service. |

### Governance

| Path | Semantics |
| --- | --- |
| `gov/proposals/{id}` | Returns a JSON encoded `governance.Proposal`. |
| `gov/params` | Returns JSON containing the active governance proposal policy and the parameter store contents for all allowed keys. |
| `QueryPrefix("gov", "params")` | Streams individual parameter key/value pairs for incremental consumption. |

## Transaction simulation

`SimulateTx` expects the transaction encoded as a `consensus.v1.Transaction` protobuf message. Clients can leverage the generated stubs (Go/TypeScript) or marshal the message manually. The response includes:

- `gas_used`: Gas consumed by execution.
- `gas_cost`: Decimal string representing `gas_used * gas_price`.
- `events`: The structured event list emitted by the state processor.

Simulation mutates an ephemeral state copy so it is safe to invoke against production validators.

## Limits and proofs

- Proof generation is deferred to a subsequent milestone; the `proof` fields are currently empty slices.
- Prefix scans operate on module-maintained indexes to avoid walking the hashed trie. Large datasets (e.g. voucher history) should continue to use the bespoke pagination APIs.
- Queries always execute against the latest committed state snapshot. Consumers requiring historical consistency should pin the consensus height before querying.
