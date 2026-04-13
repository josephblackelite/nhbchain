# Lending state indexes

The consensus query service exposes the canonical lending module data without going through the JSON-RPC adaptor. The router relies on the following indexes maintained by the `core/state` manager:

| Key | Stored value | Notes |
| --- | --- | --- |
| `lending/markets` | `[]*lending.Market` | Queried via `QueryState("lending", "markets")` or `QueryPrefix("lending", "markets")`. The prefix form streams each pool individually keyed by `PoolID`. |
| `lending/positions/{address}` | `[]struct{PoolID string; Account *lending.UserAccount}` | Materialised on-demand by enumerating pool IDs and loading the borrower’s account. Addresses accept Bech32 (`nhb1…`) or raw hex encodings. |

No additional denormalised indexes are required; the existing pool ID list allows the router to avoid a full trie walk.
