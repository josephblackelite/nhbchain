# Governance Lifecycle

This page describes what the code in `native/governance/engine.go` and
`core/governance_tx.go` does. Governance state lives in the chain state trie;
every governance action is a signed native transaction applied identically by
every validator.

## Actions and transaction types

| Action | Tx type | RLP payload (`tx.Data`) | Who may send |
| --- | --- | --- | --- |
| Propose | `TxTypeGovPropose` (`0x27`) | `{Kind string, Payload string, Deposit *big.Int}` | Any account with enough ZNHB for the deposit. The signer is the proposer. |
| Vote | `TxTypeGovVote` (`0x28`) | `{ProposalID uint64, Choice string}` | Any account with non-zero POTSO weight (see [Voting](#voting)). The signer is the voter. |
| Finalize | `TxTypeGovFinalize` (`0x29`) | `{ProposalID uint64}` | Any account. |
| Queue | `TxTypeGovQueue` (`0x2A`) | `{ProposalID uint64}` | Any account. |
| Execute | `TxTypeGovExecute` (`0x2B`) | `{ProposalID uint64}` | Any account. |

Values are defined in `core/types/transaction.go`; the apply functions are in
`core/governance_tx.go`. There are no `gov_propose`, `gov_vote`, `gov_finalize`,
`gov_queue` or `gov_execute` RPC methods (`rpc/http.go` documents their
removal). Transactions are submitted through `nhb_sendTransaction` like any
other signed transaction; `nhb-cli gov ...` builds, signs and broadcasts them
(`cmd/nhb-cli/gov.go`, see the [devnet runbook](./devnet-runbook.md)). The only
governance RPC methods on the node are the read-only `gov_proposal` and
`gov_list` ([API](./api.md)). The gateway's compatibility table also lists the
names `gov_getProposal`, `gov_listProposals`, `gov_getTally`,
`gov_submitProposal`, `gov_vote` and `gov_deposit`; these are not node methods
and do not submit governance transactions ([API](./api.md)).

## Proposal kinds

`Proposal.Target` must be one of the following strings
(`native/governance/types.go`, dispatched in `Engine.SubmitProposal` and
`Engine.Execute`); anything else fails with `governance: unsupported proposal
kind`.

| Kind | Effect on execution |
| --- | --- |
| `param.update` | Writes each key/value of the payload object to the governance param store. Keys must be in the node's `AllowedParams`. |
| `param.emergency_override` | Identical code path to `param.update`. It is **not** faster: the same voting period, quorum, threshold and timelock apply. |
| `param.update_fee_rate` | Identical code path to `param.update`. The key its constant is named for is `protocol.feeRateBps` (validator: integer `<= 10000`); it must be in `AllowedParams`, and nothing reads it (see [params](./params.md)). |
| `policy.slashing` | Writes `slashing.policy.*` param-store keys. |
| `role.allowlist` | Grants and/or revokes roles (`SetRole` / `RemoveRole`). Disabled unless `AllowedRoles` is non-empty. |
| `treasury.directive` | Moves ZNHB from an allow-listed source account to recipients. Disabled unless `TreasuryAllowList` is non-empty. |
| `policy.swapPriceSigner` | Registers or revokes a swap price-proof signer for a provider. |
| `policy.buybackParams` | Sets the three treasury-buyback basis-point parameters. |
| `policy.swapRiskParams` | Sets the four redeem-side (swap-out burn) caps. |
| `policy.redemptionFeeParams` | Sets the redemption fee rate, floor and cap. |
| `policy.lendingRateSchedule` | Replaces the fixed-term borrow tenure-to-rate table. |
| `policy.lendingDepositRateSchedule` | Replaces the fixed-term deposit tenure-to-rate table. |

Payload schemas and validation rules are in [proposal types](../gov/proposal-types.md).
For the struct-based kinds the payload is decoded with `json.Unmarshal` into a
Go struct, so unknown JSON fields are ignored, not rejected. For the `param.*`
kinds every key must be in `AllowedParams` and have a validator; unknown keys
fail with `governance: parameter "<key>" not in allow-list` or `... missing
validation rule`.

## Policy configuration

The engine is configured from the node's `[governance]` TOML block
(`config.GovConfig`, `config/config.go`) via `GovConfig.Policy()`. Defaults
applied when a field is empty or zero (`config/config.go`):

| Key | Default | Meaning |
| --- | --- | --- |
| `MinDepositWei` | `"1000e18"` (1,000 ZNHB) | Minimum deposit. |
| `VotingPeriodSeconds` | `604800` (7 days) | Length of the voting window. |
| `TimelockSeconds` | `172800` (2 days) | Delay added after the voting window ends. |
| `QuorumBps` | `2000` | Minimum turnout, see [Tally](#tally). |
| `PassThresholdBps` | `5000` | Minimum yes ratio. |
| `AllowedParams` | `defaultAllowedGovernanceParams` (`config/config.go:25`) | Keys `param.*` proposals may set. A non-empty list in the TOML file replaces the default entirely. |
| `AllowedRoles` | empty (role proposals disabled) | Role names `role.allowlist` may grant or revoke. |
| `TreasuryAllowList` | empty (treasury directives disabled) | Bech32 source accounts for `treasury.directive`. |
| `BlockTimestampToleranceSeconds` | `0` (the node then uses `DefaultBlockTimestampTolerance`) | Block timestamp tolerance (`Node.applyTimestampTolerance`). |

These values come from the local config file, not from chain state. The
`gov.deposit.MinProposalDeposit`, `gov.tally.QuorumBps`, `gov.tally.ThresholdBps`
and `gov.timelock.DurationSeconds` param keys are validated by the engine but
nothing reads them back to change the running policy; see [params](./params.md).

## Submission

`Engine.SubmitProposal`:

1. Rejects an empty kind or payload, and validates the payload for the kind.
2. Rejects a negative deposit, and a deposit below `MinDepositWei`
   (`governance: deposit below minimum`).
3. Debits the deposit from the proposer's ZNHB balance
   (`governance: insufficient ZNHB balance for deposit`) and locks it in the
   governance escrow ledger.
4. Allocates the next proposal id and stores the proposal with status
   `voting_period`. There is no deposit-collection phase; `VotingStart` is the
   block timestamp, `VotingEnd = VotingStart + VotingPeriodSeconds`, and
   `TimelockEnd = VotingEnd + TimelockSeconds`, all fixed at submission.
5. Emits `gov.proposed` and appends an audit record.

## Voting

`Engine.CastVote` requires the proposal to be in `voting_period` and the block
time to be within `[VotingStart, VotingEnd]`. The choice is lower-cased and
must be `yes`, `no` or `abstain`. Voting power is the voter's `WeightBps` in
the POTSO weight snapshot of the most recently processed POTSO reward epoch
(`PotsoRewardsLastProcessedEpoch`, key `snapshots/potso/<epoch>/weights`). The
snapshot is looked up at vote time, so the epoch used is the latest processed
one when each vote is cast. Errors: `governance: potso snapshot unavailable`
when no epoch has been processed, and `governance: voter has zero voting
power` when the voter is not in the snapshot. A later vote from the same
address replaces the earlier one.

## Tally

`Engine.ComputeTally` sums `PowerBps` per choice.

- `turnoutBps` = yes + no + abstain power (the raw sum of ballots' basis-point
  weights, not divided by anything).
- `yesRatioBps` = `floor(yes * 10000 / (yes + no))`, or `0` when there are no
  yes/no votes. Abstain does not enter the ratio.
- The proposal passes when `turnoutBps >= quorumBps` and
  `yesRatioBps >= passThresholdBps`; otherwise it is rejected.

## Finalize, queue, execute

- **Finalize** requires status `voting_period` and `now >= VotingEnd`
  (`governance: voting still in progress` otherwise). It stores the tally and
  sets the status to `passed` or `rejected`, and settles the deposit:
  - passed: the deposit is credited back to the submitter;
  - rejected: if the node has an admin wallet configured, the deposit is
    credited to the admin wallet and (when the submitter is not the admin
    wallet) to the ZNHB Reward Pool ledger; with no admin wallet the deposit
    stays locked in the submitter's governance escrow.
- **Queue** requires status `passed` and not already queued, and sets
  `Queued = true`. It does not change `TimelockEnd`, which was fixed at
  submission.
- **Execute** requires status `passed`, `Queued`, and `now >= TimelockEnd`
  (`governance: timelock not yet elapsed`). It applies the payload, sets the
  status to `executed`, and emits `gov.executed`. A second execute fails with
  `governance: proposal <id> already executed`. If applying the payload fails
  the transaction fails and the proposal stays `passed`.

`ProposalStatus` also defines `deposit_period`, `failed` and `expired`, but no
code path in `native/governance` assigns them.

## Events

| Event | Attributes |
| --- | --- |
| `gov.proposed` | `id`, `proposer` (hex), `kind`, `deposit`, `votingStart`, `votingEnd`, `timelockEnd` (Unix seconds) |
| `gov.vote` | `id`, `voter` (hex), `choice`, `powerBps`, `timestamp` |
| `gov.finalized` | `id`, `status`, `turnoutBps`, `quorumBps`, `yesPowerBps`, `noPowerBps`, `abstainPowerBps`, `yesRatioBps`, `passThresholdBps`, `totalBallots` |
| `gov.queued` | `id`, `timelockEnd` |
| `gov.executed` | `id`, `status` |
| `gov.policy.invalid` | `reason` (defined for policy preflight failures, see [policy invariants](../gov/policy-invariants.md)) |

## Audit log

Each of proposed, vote, finalized, queued and executed appends a record
(`sequence`, `timestamp`, `event`, `proposalId`, `actor`, `details` JSON) at
state key `gov/audit/<sequence>` with the counter at `gov/audit-seq`
(`core/state/manager.go`). `AuditEventFailed` is defined but never appended.
No RPC method in this repository reads the audit log.

## Arbitration and paymaster settings

- Escrow arbitration realms are managed by holders of `ROLE_ESCROW_REALM_ADMIN`
  and are described in [arbitration governance](./arbitration-governance.md).
- `global.paymaster.AutoTopUp.*` values are node configuration
  (`config.PaymasterAutoTopUp`); they are not in the default `AllowedParams`
  and have no governance validator. The `paymaster.topUpFeeWei` param is the
  governed piece, see [params](./params.md).
