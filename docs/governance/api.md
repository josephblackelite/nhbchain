# Governance API Surface

Governance has two RPC read methods and five signed transaction types. There
are no `gov_propose`, `gov_vote`, `gov_finalize`, `gov_queue` or `gov_execute`
RPC methods on the node JSON-RPC endpoint: they were removed because they
trusted a client-supplied address (`rpc/governance_handlers.go`,
`rpc/http.go`).

**Gateway method names.** The gateway's JSON-RPC compatibility table
(`gateway/compat/mapping.go`, `DefaultMappings`, used by `cmd/gateway/main.go`
when compatibility mode is enabled; also listed in
[monolith to gateway](../migrate/monolith-to-gateway.md)) still maps the
method names `gov_getProposal`, `gov_listProposals`, `gov_getTally`,
`gov_submitProposal`, `gov_vote` and `gov_deposit` to `/v1/gov/...` HTTP paths
on `governd`. Those are not node RPC methods and they are not a governance
write path: `governd` (`services/governd`) has no HTTP handlers, it serves
gRPC only, and its write RPCs cannot change chain state (see
[governance service](../gov/service.md)). Submit governance transactions with
`nhb_sendTransaction` or `nhb-cli gov ...`.

## Read methods (JSON-RPC)

Both are handled in `rpc/governance_handlers.go` and take a single positional
parameter object.

### `gov_proposal`

Params: `[{"id": <uint64>}]`. `id` is required and must be non-zero
(`id is required`). An unknown id returns HTTP 404 with `codeInvalidParams` and
the message `proposal not found`.

```json
{"jsonrpc": "2.0", "id": 1, "method": "gov_proposal", "params": [{"id": 42}]}
```

The result is a `governance.Proposal` (`native/governance/types.go`) encoded
with the struct's JSON tags:

| Field | Type in JSON |
| --- | --- |
| `id` | number |
| `title`, `summary`, `metadata_uri` | strings (empty for proposals created by `TxTypeGovPropose`, which has no such fields) |
| `submitter` | bech32 string |
| `status` | number: `1` deposit_period, `2` voting_period, `3` passed, `4` rejected, `5` failed, `6` expired, `7` executed. Only `2`, `3`, `4` and `7` are ever assigned by the engine. |
| `deposit` | number (wei, unquoted) |
| `submit_time`, `voting_start`, `voting_end`, `timelock_end` | RFC 3339 timestamps |
| `target` | proposal kind string |
| `proposed_change` | the submitted payload as a JSON string |
| `queued` | boolean |
| `tally` | object, present once finalized, or computed on the fly while the proposal is in `voting_period` (never persisted for that case); omitted otherwise |

`tally` fields (`governance.Tally`), all numbers: `turnout_bps`, `quorum_bps`,
`yes_power_bps`, `no_power_bps`, `abstain_power_bps`, `yes_ratio_bps`,
`pass_threshold_bps`, `total_ballots`. `turnout_bps` is the sum of ballots'
weights and `yes_ratio_bps` excludes abstain (see [overview](./overview.md#tally)).

### `gov_list`

Params: `[]` or `[{"cursor": <uint64>, "limit": <int>}]`; both fields are
optional. `limit` defaults to `20` and is capped at `100`. Proposals are
returned newest first, starting at `cursor` (or the latest id when absent).

Result: `{"proposals": [<Proposal>...], "nextCursor": <uint64>}`. `nextCursor`
is omitted when there are no older proposals; pass it back as `cursor` to
continue.

## Write path (signed transactions)

Governance writes are submitted with `nhb_sendTransaction` (params:
`[<signed transaction>]`, authenticated like other write methods; the result is
the transaction hash string). The transaction types and `tx.Data` RLP payloads
are listed in [overview](./overview.md#actions-and-transaction-types). The
proposer, voter and every other actor is the transaction signer; there is no
`from` field.

`nhb-cli` builds and signs these transactions (`cmd/nhb-cli/gov.go`):

| Command | Flags | Sends |
| --- | --- | --- |
| `nhb-cli gov propose` | `--kind`, `--payload` (JSON or `@file`), `--key`, `--deposit` (wei, default `0`, accepts `1000e18` style) | `TxTypeGovPropose` |
| `nhb-cli gov vote` | `--id`, `--key`, `--choice yes\|no\|abstain` | `TxTypeGovVote` |
| `nhb-cli gov finalize` | `--id`, `--key` | `TxTypeGovFinalize` |
| `nhb-cli gov queue` | `--id`, `--key` | `TxTypeGovQueue` |
| `nhb-cli gov execute` | `--id`, `--key` | `TxTypeGovExecute` |
| `nhb-cli gov show` | `--id` | `gov_proposal` |
| `nhb-cli gov list` | `--cursor`, `--limit` | `gov_list` |

The CLI sets `GasLimit` to `100000` and `GasPrice` to `1` for these
transactions. Each command prints `Broadcasted governance <action>: <tx hash>`
for writes; the proposal id is not returned by the transaction. Use
`gov list` to find it.

## Errors

The engine's errors are surfaced by the failed transaction, prefixed with the
action (`govPropose:`, `govVote:`, `govFinalize:`, `govQueue:`, `govExecute:`).
Examples from `native/governance/engine.go`:

- `governance: unsupported proposal kind "<kind>"`
- `governance: payload must not be empty`
- `governance: parameter "<key>" not in allow-list`
- `governance: role allowlist proposals are disabled`
- `governance: treasury directives are disabled`
- `governance: deposit below minimum`
- `governance: insufficient ZNHB balance for deposit`
- `governance: proposal <id> not accepting votes`
- `governance: voting period closed`
- `governance: voter has zero voting power`
- `governance: potso snapshot unavailable`
- `governance: voting still in progress`
- `governance: proposal <id> not passed` / `not queued` / `already queued`
- `governance: timelock not yet elapsed`
- `governance: proposal <id> already executed`

For example payloads of each kind see [proposal types](../gov/proposal-types.md).
