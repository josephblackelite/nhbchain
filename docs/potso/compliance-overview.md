# POTSO Rewards Compliance Overview

This document explains the POTSO reward mechanics for auditors, regulators, and investors. The goal is to demonstrate that the
program distributes loyalty-style rebates rather than investment returns.

## Program Purpose

* **Objective:** reward merchants, validators, and ambassadors for on-chain engagement (staking, uptime, transaction volume).
* **Funding source:** pre-allocated ZapNHB treasury controlled by the foundation. No user deposits are pooled or re-invested.
* **Deterministic math:** epoch emissions, weight functions, and payout thresholds are configured on-chain via `config.toml` and
  cannot be modified retroactively for a closed epoch.

## Process Summary

1. During each block the node tracks engagement meters (uptime, escrow activity, etc.).
2. At the end of an epoch (`EpochLengthBlocks`) the node computes weights and ranks participants using the published parameters.
3. The budget for the epoch equals `EmissionPerEpochWei` (subject to the treasury balance). No leverage or compounding occurs.
4. Winners receive a payout proportional to their weight, provided it exceeds `MinPayoutWei`.
5. Settlement is performed either automatically (`auto` mode) or via signed claims (`claim` mode). In both cases the only funds
   that move are the budgeted ZapNHB held in the treasury account.

## Key Compliance Points

* **Non-investment contract:** rewards compensate for measurable activity (service provision, network uptime). Winners do not
  invest capital with an expectation of profit derived from others.
* **No user custody:** participants never transfer their tokens to POTSO for management. Claims simply release pre-earned
  rewards from the foundation treasury.
* **Transparent records:**
  * `PotsoRewardsGetClaim` and `PotsoRewardsHistory` expose every settlement with timestamps and modes.
  * `potso_export_epoch` produces immutable CSVs suitable for audit sampling.
  * Events (`potso.reward.ready`, `potso.reward.paid`) provide real-time attestations.
* **Controls:** claim signatures ensure only the rightful address can receive funds. Treasury shortfalls raise explicit errors
  and do not create liabilities to participants.

## Reporting & Disclosures

* **Financial statements:** treat payouts as operating expense (marketing/loyalty). The ledger includes epoch, address, amount,
  and mode for each entry.
* **Regulatory reporting:** the deterministic emission schedule and absence of user deposits simplify oversight—regulators can
  reproduce the weight calculations using public data.
* **Investor updates:** highlight that claim mode enables deferral of cash outflows until program goals are met, while still
  honouring earned rewards.

The combination of deterministic budgeting, explicit ledgers, and separation of user funds positions POTSO rewards as a
compliance-friendly loyalty mechanism rather than a securities offering.
