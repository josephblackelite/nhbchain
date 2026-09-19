# Governance and swap admin RPC

Requests are JSON-RPC calls to the node's RPC endpoint; see
[`docs/api/rpc.md`](./api/rpc.md) for transport, authentication and error codes.

## Governance

### Read methods

Only two governance JSON-RPC methods exist: `gov_proposal` and `gov_list`
(`rpc/governance_handlers.go`). Neither requires auth.

Writes (propose, vote, finalize, queue, execute) are signed transactions sent
through `nhb_sendTransaction`. The former `gov_propose`, `gov_vote`,
`gov_finalize`, `gov_queue` and `gov_execute` RPC methods were removed; calling
them returns `unknown method` (`-32601`).

#### `gov_proposal`

Params: `[{"id": 7}]`. `id` must be non-zero (`id is required`). An unknown id
returns HTTP 404 with code `-32602` and message `proposal not found`. For a
proposal still in the voting period the node attaches a live tally to the
returned copy (`tally`); it is not persisted until finalization.

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "id": 7,
    "title": "",
    "summary": "",
    "metadata_uri": "",
    "submitter": "nhb1...",
    "status": 2,
    "deposit": 1000000000000000000000,
    "submit_time": "2024-01-01T00:00:00Z",
    "voting_start": "2024-01-01T00:00:00Z",
    "voting_end": "2024-01-08T00:00:00Z",
    "timelock_end": "2024-01-10T00:00:00Z",
    "target": "param.update",
    "proposed_change": "{\"fees.baseFee\":\"1000\"}",
    "queued": false,
    "tally": {
      "turnout_bps": 4200,
      "quorum_bps": 2000,
      "yes_power_bps": 3000,
      "no_power_bps": 1000,
      "abstain_power_bps": 200,
      "yes_ratio_bps": 7500,
      "pass_threshold_bps": 5000,
      "total_ballots": 3
    }
  }
}
```

Field names are the JSON tags of `governance.Proposal` and `governance.Tally`
(`native/governance/types.go`). `submitter` is a bech32 string, `deposit` a JSON
number in wei, times are RFC 3339. `title`, `summary` and `metadata_uri` exist on
the struct but the proposal transaction has no field that sets them.

`status` values (`ProposalStatus`):

| Value | Name |
| --- | --- |
| 0 | unspecified |
| 1 | deposit_period (defined; `SubmitProposal` never sets it) |
| 2 | voting_period |
| 3 | passed |
| 4 | rejected |
| 5 | failed |
| 6 | expired |
| 7 | executed |

#### `gov_list`

Params: `[]` or `[{"cursor": 7, "limit": 2}]`. Proposals are returned newest
first starting at `cursor` (or the latest id when omitted). `limit` defaults to 20
and is capped at 100. `nextCursor` is present only when older proposals remain.

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "proposals": [ { "id": 7, "status": 2 }, { "id": 6, "status": 3 } ],
    "nextCursor": 5
  }
}
```

(Each element is a full proposal object as above.)

### Write transactions

| Type | Value | `data` (RLP list) |
| --- | --- | --- |
| `TxTypeGovPropose` | `0x27` | `[kind string, payload string, deposit integer]` |
| `TxTypeGovVote` | `0x28` | `[proposalId uint64, choice string]` (`yes`, `no`, `abstain`) |
| `TxTypeGovFinalize` | `0x29` | `[proposalId uint64]` |
| `TxTypeGovQueue` | `0x2A` | `[proposalId uint64]` |
| `TxTypeGovExecute` | `0x2B` | `[proposalId uint64]` |

(`core/governance_tx.go`.) The proposer and voter are the transaction signer; no
payload field names an address. `nhb-cli gov propose|vote|finalize|queue|execute`
builds, signs and sends these (`cmd/nhb-cli/gov.go`); `--key` is the signer's key
file, `propose` also takes `--kind`, `--payload` (JSON or `@file`) and `--deposit`
(wei; `1000e18` shorthand accepted), `vote` takes `--id` and `--choice`. `nhb-cli
gov show --id N` and `gov list [--cursor N] [--limit N]` call the read methods.

### Rules enforced by the engine

From `native/governance/engine.go`:

* **Submit.** `kind` must be one of the supported kinds (below); the payload is
  validated per kind. A deposit at least `MinDepositWei` (default `1000e18`) is
  debited from the proposer's ZNHB balance and locked in governance escrow. Voting
  starts immediately: `voting_start` is the block timestamp, `voting_end` is
  `voting_start + VotingPeriodSeconds`, `timelock_end` is `voting_end +
  TimelockSeconds`.
* **Vote.** Only while `status` is voting and the block time is within the window.
  Voting power is the voter's `WeightBps` from the last processed POTSO reward
  epoch snapshot; a voter with zero power, or a chain with no snapshot, is
  rejected. A later vote from the same address replaces the earlier one.
* **Finalize.** Only after `voting_end`. `turnout_bps` is the sum of the voters'
  power for all three choices; the proposal passes when `turnout_bps >=
  QuorumBps` and `yes_ratio_bps >= PassThresholdBps`, where `yes_ratio_bps =
  yes * 10000 / (yes + no)` (abstain excluded); otherwise it is rejected. A passed
  proposal's deposit is returned to the submitter. A rejected proposal's deposit
  is credited to the configured admin wallet, when one is configured.
* **Queue.** Only a passed, not-yet-queued proposal.
* **Execute.** Only a passed and queued proposal whose `timelock_end` has
  elapsed; applies the payload and sets status `executed`.

Defaults from `config/config.go` (`[governance]` in `config.toml`):
`MinDepositWei = "1000e18"`, `VotingPeriodSeconds = 604800`, `TimelockSeconds =
172800`, `QuorumBps = 2000`, `PassThresholdBps = 5000`. `AllowedParams` restricts
the keys a `param.update` proposal may set; `AllowedRoles` restricts role
proposals.

Supported proposal kinds (`native/governance/types.go`): `param.update`,
`param.emergency_override`, `param.update_fee_rate`, `policy.slashing`,
`role.allowlist`, `treasury.directive`, `policy.swapPriceSigner`,
`policy.buybackParams`, `policy.swapRiskParams`, `policy.lendingRateSchedule`,
`policy.lendingDepositRateSchedule`, `policy.redemptionFeeParams`. A
`param.update` payload is a JSON object of allow-listed keys, for example
`{"fees.baseFee":"1000"}`.

Events emitted by the engine: `gov.proposed`, `gov.vote`, `gov.finalized`,
`gov.queued`, `gov.executed`, and `gov.policy.invalid` (`EventTypePolicyInvalid`,
emitted for proposals rejected during policy preflight).

## Swap administration

These methods require auth (`requireAuth`). `swap_voucher_reverse` and
`swap_markReconciled` only wrap a caller-supplied signature into a transaction
(`TxTypeSwapVoucherReverse` `0x4A`, `TxTypeSwapMarkReconciled` `0x4B`); the
signer's authority is checked by every validator when the transaction executes
(`core/swap_admin_tx.go`).

### `swap_limits`

Params: `["<bech32 address>"]`. Returns the address's minted totals for the
current day and month, plus remaining room and velocity information when the
corresponding caps are configured (`dayRemainingWei`, `monthRemainingWei` and
`velocity` are omitted when the cap is unset).

```json
{
  "address": "nhb1...",
  "day": {"bucket": "2024-04-01", "mintedWei": "1000000000000000000"},
  "dayRemainingWei": "9000000000000000000",
  "month": {"bucket": "2024-04", "mintedWei": "1500000000000000000"},
  "monthRemainingWei": "98500000000000000000",
  "velocity": {"windowSeconds": 600, "maxMints": 5, "observed": 2, "remaining": 3}
}
```

### `swap_provider_status`

Params: none (`[]`). Returns `allow` (the configured provider allow list),
`lastOracleHealthCheck` (unix seconds; `0` if none recorded) and, when oracle
feeds are reporting, `oracleFeeds[]`.

### `swap_voucher_reverse`

Params: `[{"providerTxId": "order-12345", "signature": "0x..."}]`.
`signature` is a hex-encoded 65-byte secp256k1 signature over
`keccak256("NHB_SWAP_VOUCHER_REVERSE_V1|providerTxId=<providerTxId>")` (the id is
trimmed), produced by a key holding the on-chain role `ROLE_SWAP_ADMIN`.

When the transaction executes, the voucher's minted amount is debited from the
voucher recipient's balance and credited to the node's configured swap refund
sink, and the voucher is marked reversed. It fails if the recipient's balance is
lower than the minted amount. The RPC returns `{"ok": true, "txHash": "0x..."}`
once the transaction is accepted into the mempool (reversal takes effect when a
block applies it). An already-reversed voucher returns `{"ok": true}` with no
`txHash`. Errors: 403 (`-32001`) unauthorized signer, 409 (`-32602`) voucher not
minted or insufficient recipient balance, 404 (`-32602`) unknown voucher.

### `swap_markReconciled`

Params: `[{"providerTxIds": ["order-12345"], "signature": "0x..."}]`.
`signature` is over `keccak256("NHB_SWAP_MARK_RECONCILED_V1|providerTxIds=" +
join(ids, ","))` where `ids` are trimmed, blank entries removed, in the submitted
order; signer must hold `ROLE_SWAP_ADMIN`. Returns `{"ok": true, "txHash":
"0x..."}`.

Other swap-admin methods in the dispatch table with the same auth requirement:
`swap_burn_list` (params `[startTs, endTs, cursor?, limit?]`, default limit 50),
`swap_setManualQuote` (params `[{"base","quote","rate","timestamp"?}]`) and
`swap_listPendingRedemptions`.
