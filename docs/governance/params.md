# Governance Parameters

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

The following parameters are governable by default. Governance payloads must
respect the validation guidance for each key; values outside the documented
range are rejected during execution. All parameters are community-controlled via
open voting and should be interpreted as configuration levers, not investment
contracts. Adjustments do not create an expectation of profit and should be
considered in light of the risk notes below.

| Key | Description | Validation Guidance | Risk Notes & Disclosures |
| --- | --- | --- | --- |
| `gov.deposit.MinProposalDeposit` | Minimum deposit required to submit a proposal. Held in escrow to deter spam. | Unsigned integer in Wei. Must be non-negative and less than total supply to avoid overflow. | Deposits are anti-spam bonds only. They are returned or partially slashed per policy and never accrue yield or profit participation. |
| `gov.tally.QuorumBps` | Minimum participation (turnout) required for a proposal to be valid. | Integer basis points `0`–`10,000`. Runtime rejects values above `10,000`. | Low quorum may allow low-participation changes; high quorum can stall governance. Communicate changes to stakeholders before adoption. |
| `gov.tally.ThresholdBps` | Approval threshold for yes votes relative to active votes (yes + no). | Integer basis points `0`–`10,000`. Must be >= `5,000` to avoid trivial approvals. | Raising the threshold increases safety but may slow emergency responses. Lowering below 2/3 should include rationale and mitigation plan. |
| `gov.timelock.DurationSeconds` | Delay between proposal queueing and execution. | Unsigned integer seconds. Must be >= network minimum (default 86,400) and < 30 days to prevent overflow. | Short timelocks reduce review windows; long timelocks delay urgent fixes. Announce changes widely to integrators. |
| `potso.weights.AlphaStakeBps` | Proportion of POTSO rewards attributed to validator staking weight. | Integer basis points `0`–`10,000`. Values above bounds are rejected. | Adjusting weight influences validator incentives but does not guarantee return. Communicate redistributive effects to delegators. |
| `potso.rewards.EmissionPerEpochWei` | ZNHB emission allocated per epoch for POTSO incentives. | Unsigned integer in Wei. Must be `>= 0` and `< 9.22e18` to avoid overflow. | Higher emissions increase circulating supply and may dilute holders. Include monetary impact analysis when changing this value. |
| `fees.baseFee` | Minimum base fee charged for network transactions. | Unsigned integer in Wei per gas. Must be non-negative; typical range `0`–`1e15`. | Fee adjustments are for network sustainability. They do not represent revenue sharing and should be accompanied by usage impact notes. |
| `slashing.policy.enabled` | Toggles the on-chain slashing engine. | Boolean `true` or `false`. | Disabling slashing pauses treasury debits from evidence processing. Communicate mitigation plans before re-enabling. |
| `slashing.policy.maxPenaltyBps` | Maximum penalty applied per infraction in basis points. | Integer basis points `0`–`10,000`. | Higher caps increase deterrence but amplify downside risk for operators. Document rationale and thresholds. |
| `slashing.policy.windowSeconds` | Rolling window used to enforce slashing penalties. | Unsigned integer seconds between `60` and `2,592,000` (30 days). | Very short windows can miss repeated behaviour; long windows may extend incident response timelines. |
| `slashing.policy.evidenceTtlSeconds` | Maximum age of evidence accepted by the slashing module. | Unsigned integer seconds `>= windowSeconds` and `<= 7,776,000` (90 days). | Ensures stale evidence cannot trigger penalties indefinitely. Align TTL with compliance retention policies. |
| `slashing.policy.maxSlashWei` | Cap on the slash amount routed from treasury per infraction. | Unsigned integer in Wei `>= 0` and `< 9.22e18`. | Sets an upper bound on treasury exposure for any single event. Keep aligned with risk appetite and reserve levels. |
| `staking.minimumValidatorStake` | Minimum self-bond required for a validator to be eligible for selection. | Positive decimal integer in Wei representing ZNHB; governance may not set `0` or negative values. | Defaults to `1,000` ZNHB during migrations to ensure continuity. Raising the floor tightens validator admission and may reduce decentralisation; lowering it can increase validator set churn and affect selection fairness based on stake-weighted rotation. |
