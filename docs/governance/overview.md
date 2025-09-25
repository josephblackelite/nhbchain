# Governance Lifecycle

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

## Proposal Intake

## Deposit Escrow Collection

## Voting Period

Proposals enter the voting period as soon as the minimum deposit clears and
remain open until the `VotingEnd` timestamp recorded on-chain. Validators and
delegators may submit a single ballot per address selecting **yes**, **no**, or
**abstain**. Subsequent submissions overwrite the prior choice so wallets can
surface a "change vote" workflow without additional signing steps.

Ballot weight is derived from the POTSO composite engagement leaderboard to
ensure aligned incentives across staking and usage. Each vote pulls the
participant's basis-point share from the snapshot finalised at POTSO epoch
`E-1` (the most recently processed epoch) so that last-minute stake churn cannot
artificially inflate voting power. Addresses without weight in that snapshot
are rejected, preventing zero-power spam while still allowing abstentions from
eligible voters.

## Timelock Enforcement

## Execution and Archival
