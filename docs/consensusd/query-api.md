# Consensus Query API

`consensusd` exposes a read-only gRPC service, `consensus.v1.QueryService`
(`proto/consensus/v1/query.proto`, handlers in
`consensus/service/query_server.go`). It is registered on the same listener and
behind the same authentication as the consensus service; see
[consensusd Getting Started](getting-started.md).

## Service definition

| RPC | Request | Response |
| --- | --- | --- |
| `QueryState` | `namespace`, `key` (strings) | `value` (bytes), `proof` (bytes) |
| `QueryPrefix` | `namespace`, `prefix` (strings) | server stream of `key` (string), `value` (bytes), `proof` (bytes) |
| `SimulateTx` | `tx_bytes` (a serialized `consensus.v1.Transaction`) | `gas_used` (uint64), `gas_cost` (decimal string), `events` (`type` plus `attributes` map) |

Values are raw bytes; the namespaces below return JSON. The `proof` fields are
never populated by the current router (`core.QueryResult.Proof` is not set
anywhere), so they arrive empty.

Namespaces are matched case-insensitively after trimming whitespace, and `gov`
and `governance` are the same namespace. An unknown namespace or path returns
the error `ErrQueryNotSupported`.

## Namespaces

Implemented in `core/query_router.go` and, for the fallbacks, `core/node.go`
(`queryStateFallback`, `queryPrefixFallback`).

### `lending`

| Call | Result |
| --- | --- |
| `QueryState("lending", "markets")` | JSON array of `lending.Market`. |
| `QueryState("lending", "positions/{address}")` | JSON array of `{poolId, account}` for every pool where the address has a lending account. The address is Bech32 (`nhb1...`) or `0x` plus 40 hex characters. |
| `QueryPrefix("lending", "")` or `QueryPrefix("lending", "markets")` | One record per market: key = pool ID, value = JSON of the market. Any other prefix returns `ErrQueryNotSupported`. |

### `swap`

| Call | Result |
| --- | --- |
| `QueryState("swap", "vouchers/{id}")` | JSON of the stored voucher record for that identifier; an empty value if there is none. |
| `QueryState("swap", "oracles")` | JSON of the node's swap provider status (`Node.SwapProviderStatus`). |

There is no `QueryPrefix` for `swap`.

### `gov` / `governance`

| Call | Result |
| --- | --- |
| `QueryState("gov", "proposals/{id}")` | JSON of the proposal; an empty value if the ID does not exist. |
| `QueryState("gov", "tallies/{id}")` | `{"proposal_id", "status", "tally"}` computed from the stored votes; only `{"proposal_id"}` if the proposal does not exist. |
| `QueryState("gov", "params")` | `{"policy": <ProposalPolicy>, "params": {key: value}}` for every key in the governance policy's allowed-parameter list plus `staking.minimumValidatorStake`, for those that have a stored value. |
| `QueryPrefix("gov", "params")` | Key/value records for `staking.minimumValidatorStake` only, if it has a stored value (`StateProcessor.queryGovernancePrefix`, `core/query_router.go`). This call succeeds, so the node's wider fallback that lists every allowed parameter (`queryPrefixFallback`, `core/node.go`) is not reached; it returns a different, smaller set than `QueryState("gov", "params")`. Any other prefix returns `ErrQueryNotSupported`. |

## Transaction simulation

`SimulateTx` decodes `tx_bytes` as a `consensus.v1.Transaction` protobuf message
(`proto/consensus/v1/tx.proto`) and converts it with `codec.TransactionFromProto`
(`Node.SimulateTx`, `core/node.go`). It then runs
`ExecuteTransaction` on a copy of the current state at the chain's current
height and time, and discards the copy, so nothing is written. The response has
the execution's gas figures and the events it emitted. Native transaction types
report empty gas fields, because `executeTransaction` returns an empty
`SimulationResult` for them; `gas_used` and `gas_cost` carry whatever the
execution result contains.

## Limits

- Queries read the node's current state, not a historical height.
- `QueryPrefix` results are built in memory and then streamed.
