# Governance state indexes

The governance module stores proposals and parameters in the shared key-value store. The consensus query router exposes the following paths:

| Key | Stored value | Notes |
| --- | --- | --- |
| `gov/proposals/{id}` | `governance.Proposal` | Queried via `QueryState`. IDs are decimal strings matching on-chain proposal identifiers. |
| `gov/params` | `struct{Policy governance.ProposalPolicy; Params map[string]string}` | Built from the current proposal policy (in memory) and the parameter store entries for all allowed keys. |
| Prefix `gov/params` | `[]QueryRecord` | `QueryPrefix("gov", "params")` streams individual parameter key/value pairs so clients can rebuild the store incrementally. |

Parameter values are returned verbatim; callers should interpret each key according to module documentation.
