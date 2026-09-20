# Governance state indexes

## Consensus query paths

The consensus service (`consensus/service/query_server.go`, gRPC `QueryService`)
forwards `QueryState(namespace, key)` and `QueryPrefix(namespace, prefix)` to
`Node.QueryState` / `Node.QueryPrefix` (`core/node.go`). Both accept the
namespace `gov` or `governance`. `StateProcessor.QueryState`
(`core/query_router.go`) answers first; when it returns `ErrQueryNotSupported`
the node's `queryStateFallback` / `queryPrefixFallback` are tried.

| Call | Answered by | Result |
| --- | --- | --- |
| `QueryState("gov", "proposals/<id>")` | `queryGovernanceState` | JSON `governance.Proposal`. An unknown id returns an empty value, not an error. A non-numeric id fails with `gov: invalid proposal id`. |
| `QueryState("gov", "proposals/latest")` | `queryStateFallback` | `{"latest": <n>}`, the value of the proposal id counter (`gov/seq`). |
| `QueryState("gov", "tallies/<id>")` | `queryStateFallback` | `{"proposal_id", "status", "tally"}` where `tally` is `ComputeTally` over the votes stored so far and `status` is `passed` or `rejected` from that computation. This is a live projection: it is not the stored proposal status. An unknown id returns `{"proposal_id": <id>}`. |
| `QueryState("gov", "params")` | `queryStateFallback` | `{"policy": governance.ProposalPolicy, "params": {key: raw stored value}}`. `policy` is the node's in-memory `[governance]` policy (field names are the Go names, for example `MinDepositWei`, `AllowedParams`). `params` holds the param-store entries that exist for each key in `AllowedParams`, plus `staking.minimumValidatorStake`. |
| `QueryPrefix("gov", "params")` | `queryGovernancePrefix` | Only `staking.minimumValidatorStake`, and only when it has been set. The other allowed keys are not returned by this call. |

`governd` uses `proposals/<id>`, `proposals/latest` and `tallies/<id>` (see
[the governance service](../gov/service.md)).

## Raw state keys

Defined in `core/state/manager.go`:

| Key | Value |
| --- | --- |
| `gov/seq` | Last allocated proposal id. |
| `gov/proposals/<id>` | Stored proposal (decimal id). |
| `gov/votes/<id>/<voter hex>` | A voter's latest ballot. |
| `gov/vote-index/<id>` | List of the proposal's stored ballots (one per voter), read by `GovernanceListVotes` for tallies. |
| `gov/escrow/<address bytes>` | Deposit escrow balance for an address. |
| `gov/audit/<sequence>` and `gov/audit-seq` | Audit records and their counter. |
| `params/<key>` | Param-store value, the raw JSON text set by an executed proposal. |
| `snapshots/potso/<epoch>/weights` | POTSO weight snapshot that supplies voting power. |

Param-store values are returned verbatim; interpret each key with
[params](./params.md).
