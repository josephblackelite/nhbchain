# Governance Security and Audit Notes

Facts about governance behavior that matter for review, each tied to code.
Lifecycle details are in [overview](./overview.md).

## Who is acting

Every governance action is a signed native transaction
(`TxTypeGovPropose`..`TxTypeGovExecute`, `core/governance_tx.go`). The
proposer and voter are the recovered transaction signer, never a payload field.
The transactions carry the chain id and account nonce like other native
transactions (`sendGovTx` in `cmd/nhb-cli/gov.go` sets `ChainID`, `Nonce` and
signs); the handlers increment the signer's nonce. Finalize, queue and execute
have no role or identity check: any funded account can send them.

## Voting power source

`Engine.CastVote` reads the weight of the voter from the POTSO weight snapshot
of the most recently processed reward epoch. `processPotsoRewardEpoch`
(`core/state_transition.go`) writes that snapshot to
`snapshots/potso/<epoch>/weights`, but only when `potso.ComputeRewards` returns
a weight snapshot (`outcome.WeightSnapshot != nil`; it can return none, for
example when the epoch's budget is zero or less, `native/potso/rewards.go`). The caller,
`maybeProcessPotsoRewards`, records the epoch as the last processed one
separately, after `processPotsoRewardEpoch` returns. If the snapshot was not
written, or no epoch has been processed yet, every vote fails with
`governance: potso snapshot unavailable` (`native/governance/engine.go`,
`Engine.CastVote`). The lookup happens when each vote is cast, so a vote uses
the latest processed epoch at that moment, not a snapshot fixed when the
proposal was created.

## State machine

`voting_period` -> (`Finalize`) `passed` or `rejected`; `passed` -> (`Queue`,
then `Execute` after `TimelockEnd`) `executed`. Requests that do not match the
current state fail with plain errors, for example `governance: proposal <id>
not accepting votes`, `... not in voting period`, `... not passed`,
`... already queued`, `... not queued`, `... already executed`. There is no
dedicated error type.

The statuses `deposit_period`, `failed` and `expired` exist in the enum but are
never assigned. A proposal that is never queued or executed stays `passed`
indefinitely; nothing expires it.

## Timelock

`TimelockEnd` is `VotingEnd + TimelockSeconds`, computed at submission from the
node's `[governance]` policy and stored on the proposal. `Execute` refuses to
run while `now < TimelockEnd`. `param.emergency_override` uses the same
`Execute` path, so it has the same timelock (`Engine.Execute`,
`native/governance/engine.go`). `Queue` does not delay execution any further
than `TimelockEnd`.

Execution is applied once: the status becomes `executed` and a second
`Execute` fails.

## Deposits

The deposit is debited from the proposer's ZNHB balance at submission and held
in `gov/escrow/<address>`. On finalize it is returned if the proposal passed,
and forfeited to the admin wallet (with a matching Reward Pool ledger credit
when the submitter is not the admin wallet) if it was rejected and the node has
an admin wallet. Without an admin wallet a rejected deposit stays in escrow.
There is no withdrawal, veto or slashing path in `native/governance`.

## Audit log and events

Audit records are appended for proposed, vote, finalized, queued and executed
(`Engine.appendAudit`) and stored at `gov/audit/<sequence>`. The `details` JSON
carries the applied effect, for example changed keys, granted roles, or
treasury transfers. There is no RPC method that reads the audit log. Events
emitted by the engine are listed in [overview](./overview.md#events).

## Tally reproducibility

The ballots for a proposal are stored at `gov/vote-index/<id>` and per voter at
`gov/votes/<id>/<voter hex>`. From those, with `yes`, `no`, `abstain` the sums
of `PowerBps` per choice:

- `turnoutBps = yes + no + abstain`
- `yesRatioBps = floor(yes * 10000 / (yes + no))`, `0` if `yes + no = 0`
- passed when `turnoutBps >= quorumBps` and `yesRatioBps >= passThresholdBps`

`quorumBps` and `passThresholdBps` are the values in the node's `[governance]`
policy at the time of the tally and are recorded in the finalized tally
(`quorum_bps`, `pass_threshold_bps`). The persisted tally on a finalized
proposal is the record of the outcome.

## Behaviors to be aware of

- The quorum-versus-threshold and minimum-voting-period cross-checks in
  `native/gov` are not registered on the engine that applies governance
  transactions; see [policy invariants](../gov/policy-invariants.md).
- Param values are stored as the raw JSON text submitted, and readers parse
  them as bare integers, so integer values must be submitted unquoted; see
  [params](./params.md#how-a-parameter-proposal-is-checked).
- `treasury.directive` debits and credits ZNHB account balances
  (`applyTreasuryDirective`) and does not touch the Sale Pool or Reward Pool
  ledgers.
- Governance policy (voting period, timelock, quorum, threshold, allow-lists)
  is read from each node's local config file. Validators need identical
  `[governance]` blocks.
