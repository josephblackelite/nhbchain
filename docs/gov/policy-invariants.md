# Governance Policy Invariants

This page lists which bounds the code enforces on governance proposals and
where. It distinguishes checks that run on every validator when a governance
transaction is applied from checks that exist in the codebase but are not
wired into that path.

## Enforced when a governance transaction is applied

These run inside `Engine.SubmitProposal` / `Engine.Execute`, which every
validator executes through `StateProcessor.governanceEngine`
(`core/governance_tx.go`).

| Rule | Where |
| --- | --- |
| `gov.tally.ThresholdBps` value must be `5000`-`10000`. | `validatorForParam` (`native/governance/engine.go`) |
| `gov.tally.QuorumBps` value must be `<= 10000`. | `validatorForParam` |
| `gov.timelock.DurationSeconds` value must be `3600`-`2592000`. | `validatorForParam` |
| `policy.slashing`: `windowSeconds` `60`-`2592000`, `maxPenaltyBps <= 10000`, `evidenceTtlSeconds` non-zero, `>= windowSeconds` and `<= 7776000`, `maxSlashWei <= 9223372036854775807`. | `parseSlashingPolicyPayload` |
| `treasury.directive`: `TreasuryAllowList` must be non-empty, `source` must be on it, at least one transfer, each amount positive, source balance must cover the total. | `parseTreasuryDirectivePayload`, `applyTreasuryDirective` |
| `role.allowlist`: roles must be in `AllowedRoles`; `MINTER_ZNHB` is never grantable. | `parseRoleAllowlistPayload` |
| Ordering and range checks for `policy.swapRiskParams`, `policy.redemptionFeeParams`, `policy.buybackParams`, and the lending schedules. | `native/governance/engine.go`; see [proposal types](./proposal-types.md) |

The three `gov.*` keys above are only reachable when the operator has added
them to `AllowedParams`, which the default list does not do. Even then, they
are validated and stored but no code reads them to change the running quorum,
threshold or timelock; those come from the `[governance]` block of the node's
config file (see [params](../governance/params.md)).

`[governance] QuorumBps` and `PassThresholdBps` are not checked against each
other or against a floor when the engine is configured
(`Engine.SetPolicy`, `GovConfig.Policy`), and `VotingPeriodSeconds` has no
minimum there either.

## Preflight cross-check that is not wired into transaction execution

`native/gov` (`PreflightPolicyApply`) merges a proposed change to
`gov.tally.QuorumBps` / `gov.tally.ThresholdBps` over the current settings and
runs `config.ValidateConfig` (`config/validate.go`) on the result. That
function rejects:

- quorum below the pass threshold (`governance: quorum_bps < pass_threshold_bps`);
- a voting period below `MinVotingPeriodSeconds` = `3600`
  (`governance: voting_period_seconds too small`);
- the other global limits it checks (slashing window bounds, mempool and block
  sizes, staking, fee and loyalty settings).

`Engine.preflightPolicyDelta` calls it through a `PolicyValidator` callback,
and emits `gov.policy.invalid` when it fails. The callback is registered only
by `Node.newGovernanceEngine` (`core/node.go`), which serves read paths such as
`gov_proposal`, `gov_list` and the `tallies/<id>` query. The engine that
applies governance transactions (`StateProcessor.governanceEngine` in
`core/governance_tx.go`) does not call `SetPolicyValidator`, so with no
validator registered `preflightPolicyDelta` returns without checking. As
written, the quorum-versus-threshold and minimum-voting-period checks are
therefore not enforced by consensus. `buildPolicyDelta` also only builds
quorum and threshold deltas; the slashing, mempool and block delta types in
`native/gov` are never populated by the engine.

The same `ValidateConfig` is run against `[global.*]` settings at startup by
`cmd/consensusd/main.go`, which is where `[global.Governance]`'s voting-period
floor of `3600` seconds applies. `[global.Governance]` is a separate block
from `[governance]` (`config/types.go`); the engine does not read it.
